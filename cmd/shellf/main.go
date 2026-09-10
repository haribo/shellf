package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shellf/internal/agent"
	"shellf/internal/engine"
	"shellf/internal/inventory"
	"shellf/internal/lang"
	"shellf/internal/orchestrator"
	"shellf/internal/project"
	"shellf/internal/proto"
	"shellf/internal/report"
	"shellf/internal/transport"
)

func main() {
	// Agent mode: hidden, invoked on each target after being pushed over SSH.
	if len(os.Args) > 1 && os.Args[1] == "__agent" {
		sockDir := "" // optional: a workdir to open the control channel in
		if len(os.Args) > 2 {
			sockDir = os.Args[2]
		}
		if err := agent.ServeOn(os.Stdin, os.Stdout, engine.ShellExecutor{}, sockDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Resident agent: detached loop over file requests in a workdir (ADR-0005).
	// Args: __agent-resident <workdir> [ttl-seconds].
	if len(os.Args) > 2 && os.Args[1] == "__agent-resident" {
		ttl := 2 * time.Hour // default; overridden by the arg
		if len(os.Args) > 3 {
			if secs, err := strconv.Atoi(os.Args[3]); err == nil && secs > 0 {
				ttl = time.Duration(secs) * time.Second
			}
		}
		self, _ := os.Executable()
		if err := agent.ServeResident(os.Args[2], self, engine.ShellExecutor{}, ttl); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// The escalated half of a tree transfer (ADR-0044). The agent re-invokes itself through
	// the executor so `as <user>` applies to the placement, which used to be done from the
	// agent's own process and therefore ignored the escalation entirely (#390).
	//
	// Bounded on purpose: paths and flags, no socket, no control host, no plan, no def.
	// Whatever reads these arguments may be running as root.
	if len(os.Args) > 3 && os.Args[1] == "__sync-scan" {
		if err := agent.SyncScan(os.Args[2], os.Args[3], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 3 && os.Args[1] == "__sync-commit" {
		del := len(os.Args) > 4 && os.Args[4] == "--delete"
		if err := agent.SyncCommit(os.Args[2], os.Args[3], del, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Bridge: copies this session's stdin/stdout to the detached agent's Unix socket
	// (ADR-0031). Hidden, launched by the control host over SSH for the duration of a
	// job that needs the channel. It dies with its session by design.
	if len(os.Args) > 2 && os.Args[1] == "__bridge" {
		if err := agent.Bridge(os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Run a plan file against an inventory file.
	if len(os.Args) > 1 && os.Args[1] == "run" {
		runCmd(os.Args[2:])
		return
	}

	// Report current-vs-desired state per host, without acting (ADR-0013).
	if len(os.Args) > 1 && os.Args[1] == "status" {
		statusCmd(os.Args[2:])
		return
	}

	// Clean shellf agents and files off the targets.
	if len(os.Args) > 1 && os.Args[1] == "clean" {
		cleanCmd(os.Args[2:])
		return
	}

	// Print the build version (set via -ldflags at release; "dev" otherwise).
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(versionLine())
		return
	}

	fmt.Fprint(os.Stderr, "usage:\n"+
		"  shellf run --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--secret-file n=path] [--dry-run] [--insecure] <plan.shellf>\n"+
		"  shellf status --inventory <hosts.shellf> [--insecure] <plan.shellf>\n"+
		"  shellf clean --inventory <hosts.shellf> [--insecure] [target...]\n"+
		"  shellf version\n")
	os.Exit(2)
}

// version is the build version, overridden at release via
// `-ldflags "-X main.version=<tag>"`; "dev" for a local build.
var version = "dev"

func versionLine() string { return "shellf " + version }

// checkParallel refuses a fan-out width the operator typed and that cannot mean anything.
//
// `flag.Int` cannot tell "absent" from "explicitly 0" — both arrive as 0 — so the flag set
// is asked which flags were actually provided. The distinction matters: an unset knob
// takes the default, while a typed `--parallel 0` is a mistake worth naming rather than
// absorbing. It is never read as "unlimited" (#462).
func checkParallel(fs *flag.FlagSet, n int) {
	provided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "parallel" {
			provided = true
		}
	})
	if provided && n < 1 {
		fmt.Fprintf(os.Stderr, "--parallel must be at least 1, got %d\n", n)
		os.Exit(2)
	}
}

// multiFlag collects a repeatable string flag, in the order given.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// planInputs are the flags that describe **what a plan sees**, plus the connection knobs
// for reaching the hosts it names. `run` and `status` both read a plan, so both register
// exactly these; what differs between the two commands is modes (`--dry-run`), never
// inputs.
//
// One definition rather than two, because two drifted: `status` shipped without `--vars`,
// `--set`, `-v` and `--agent-ttl`, so the command whose whole job is answering "what would
// this plan see?" could not be handed what the plan sees (#640). `clean` does not read a
// plan and keeps its own three flags.
type planInputs struct {
	inv        *string
	vars       *string
	sets       kvFlags
	secretFile kvFlags
	secretEnv  kvFlags
	insecure   *bool
	knownHosts *string
	agentTTL   *time.Duration
	parallel   *int
	limits     multiFlag
	verbose    *bool
	asJSON     *bool
}

// registerPlanInputs declares them on fs. The pointers are read after fs.Parse.
func registerPlanInputs(fs *flag.FlagSet, limitWhat string) *planInputs {
	in := &planInputs{}
	in.inv = fs.String("inventory", "", "inventory file (required)")
	in.vars = fs.String("vars", "", "vars file: global `name = value` bindings")
	fs.Var(&in.sets, "set", "override a variable, k=v (repeatable); wins over --vars and plan bindings")
	fs.Var(&in.secretFile, "secret-file", "secret from a file, name=path (repeatable); redacted in output")
	fs.Var(&in.secretEnv, "secret-env", "secret from an env var, name=VAR (repeatable); redacted in output")
	in.insecure = fs.Bool("insecure", false, "skip host-key verification (dev only)")
	in.knownHosts = fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	in.agentTTL = fs.Duration("agent-ttl", 0, "resident agent inactivity TTL before it self-erases (0 = 2h)")
	in.parallel = fs.Int("parallel", 0, "hosts dialled at once (0 = 16); 1 serialises the fan-out")
	fs.Var(&in.limits, "limit", "restrict the "+limitWhat+" to a host or group (repeatable); narrows the plan, never extends it")
	in.verbose = fs.Bool("v", false, "trace the control host's decisions on stderr, and report every command run on the target")
	in.asJSON = fs.Bool("json", false, "report as JSON on stdout (diagnostics stay on stderr)")
	return in
}

// runCmd: shellf run <plan.shellf> --inventory <hosts.shellf> [--dry-run] [flags].
func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	in := registerPlanInputs(fs, "run")
	// Modes, not inputs: these say what `run` does with the plan, which is the one thing
	// `status` has no equivalent of.
	dryRun := fs.Bool("dry-run", false, "decide and preview without mutating")
	// `--check` was the old name (ADR-0035). Accepting it silently would keep two
	// spellings alive; this only exists to say what to type instead.
	oldCheck := fs.Bool("check", false, "")
	_ = fs.Parse(args) // flag.ExitOnError already exits on a parse error

	// Named after Parse, when the repeatable flags have collected their values.
	invPath, varsPath := in.inv, in.vars
	sets, secretFiles, secretEnvs := in.sets, in.secretFile, in.secretEnv
	insecure, knownHosts, agentTTL := in.insecure, in.knownHosts, in.agentTTL
	parallel, verbose, asJSON, limits := in.parallel, in.verbose, in.asJSON, in.limits

	// Before anything is read: a wrong flag must be the error the operator sees, not a
	// missing file that happens to be reported first.
	checkParallel(fs, *parallel)

	if msg := removedFlag(*oldCheck); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}

	if fs.NArg() < 1 || *invPath == "" {
		fmt.Fprintln(os.Stderr, "usage: shellf run --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--secret-file n=path] [--dry-run] [--insecure] <plan.shellf>")
		os.Exit(2)
	}

	baseVars, setVars, err := loadGlobals(*varsPath, sets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	secrets, secretValues, err := loadSecrets(secretFiles, secretEnvs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range secrets { // secrets win, like --set (ADR-0018)
		setVars[k] = v
	}
	plan, defsSrc, validate, err := project.Load(fs.Arg(0), *invPath, baseVars, setVars)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inv, err := loadInventory(*invPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mode := "apply"
	if *dryRun {
		mode = "check" // the engine mode keeps its internal name
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	channelFor := controlChannel(fs.Arg(0), plan, inv, baseVars, setVars)

	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		if h.Local { // reached on the control host, no SSH (ADR-0027)
			return transport.Local{Channel: channelFor(alias)}
		}
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure, AgentTTL: *agentTTL,
			Trace:   tracer(*verbose, secretValues),
			Channel: channelFor(alias), // nil when the plan asks nothing: no bridge
		}
	}

	opt := orchestrator.Options{Parallel: *parallel, Limit: limits, Verbose: *verbose, ValidateArgs: validate}
	printReports(orchestrator.Run(plan, inv, self, mode, dial, baseVars, setVars, defsSrc, opt), secretValues, *asJSON)
}

// controlChannel builds the per-host control-host server (ADR-0031), or returns a
// function yielding nil when the plan asks the control host for nothing — no bridge is
// opened then.
//
// Shared by `run` and `status` on purpose. `status` runs each def's `observe`, and an
// observe may call a primitive — `file.template` renders there to decide whether the
// destination is in sync (#334). Wiring this in `run` alone made `status` report
// `err.agent` for every template, which is how it was found.
func controlChannel(planPath string, plan []orchestrator.Block, inv inventory.Inventory,
	baseVars, setVars map[string]string) func(alias string) func(io.Reader, io.WriteCloser) error {

	// What the plan may ask for (ADR-0034 §5 → ADR-0031 §3). Derived from the plan
	// before anything is sent: the channel serves this set and refuses the rest by
	// name, which is what keeps an imported def from reading ~/.ssh.
	// The plan is inside the project by the time a run reaches here — loadPlanPackage
	// refuses otherwise — so this cannot fail for a reason the operator has not been told
	// about already.
	root, err := project.Root(planPath)
	if err != nil {
		root = filepath.Dir(filepath.Dir(planPath))
	}
	assetsDir := filepath.Join(root, project.DirAssets)
	var allSteps []proto.Step
	for _, b := range plan {
		allSteps = append(allSteps, b.Steps...)
	}
	// The plan's steps are the whole source of the allow-list: a def may not name a
	// control-host file (ADR-0043), so there is nothing to extract from the def sources.
	declared := lang.ControlResources(allSteps)
	// A render names a declared template since #392, so the allow-list answers on its
	// own: nothing asks the control host for anything when it is empty.
	needsChannel := len(declared) > 0

	// One channel per host: rendering substitutes over *that host's* environment
	// (ADR-0024), and the variables never leave the control host (ADR-0018).
	return func(alias string) func(io.Reader, io.WriteCloser) error {
		if !needsChannel {
			return nil
		}
		host, _ := inv.Resolve(alias)
		// The same table the target's own steps resolve against (#540, ADR-0053): plan-side
		// values bare, the host's own under `inventory.`. A template rendered here used to
		// see host fields bare, which is the divergence this issue removes — `~{domain}` and
		// `domain` must not disagree either.
		env := orchestrator.HostEnv(alias, host, inv, baseVars, setVars)
		allow := orchestrator.NewAllowed(assetsDir, declared)
		allow.Render = func(content string, scope map[string]string) (string, error) {
			// The call site wins over the host environment: that is what `with { }`
			// means (ADR-0022), and a def's own params are more local still.
			return lang.Template(content, func(n string) (string, bool) {
				if v, ok := scope[n]; ok {
					return v, true
				}
				v, ok := env[n]
				return v, ok
			})
		}
		return func(r io.Reader, w io.WriteCloser) error {
			c := proto.NewConnRW(r, w)
			if err := c.Handshake(); err != nil {
				return err
			}
			return orchestrator.Serve(c, allow)
		}
	}
}

// removedFlag reports what to type instead of a flag ADR-0035 removed, or "" when none
// was passed. The old name is not accepted — this only replaces "unknown flag" with
// something actionable, the same way a renamed instruction does.
func removedFlag(oldCheck bool) string {
	if oldCheck {
		return "unknown flag --check — renamed to --dry-run (ADR-0035)"
	}
	return ""
}

func loadInventory(invPath string) (inventory.Inventory, error) {
	src, err := os.ReadFile(invPath)
	if err != nil {
		return inventory.Inventory{}, err
	}
	inv, err := lang.ParseInventory(string(src))
	if err != nil {
		return inventory.Inventory{}, fmt.Errorf("%s: %v", invPath, err)
	}
	// Structural faults are caught here, once, before any plan is parsed or any host
	// dialled — a group member no host declares used to surface as an SSH handshake
	// failure against an empty address (#451).
	if err := inv.Validate(); err != nil {
		return inventory.Inventory{}, fmt.Errorf("%s: %v", invPath, err)
	}
	return inv, nil
}

// kvFlags collects repeatable --set k=v flags.
type kvFlags []string

func (k *kvFlags) String() string     { return strings.Join(*k, ",") }
func (k *kvFlags) Set(v string) error { *k = append(*k, v); return nil }

// loadGlobals builds the two variable tables: baseVars from the --vars file
// (lower precedence), setVars from --set (highest). Per-host inventory vars sit
// between them, layered at orchestration time.
func loadGlobals(varsPath string, sets kvFlags) (baseVars, setVars map[string]string, err error) {
	baseVars = map[string]string{}
	if varsPath != "" {
		src, rerr := os.ReadFile(varsPath)
		if rerr != nil {
			return nil, nil, rerr
		}
		baseVars, err = lang.ParseVars(string(src))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", varsPath, err)
		}
	}
	setVars = map[string]string{}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("--set expects k=v, got %q", kv)
		}
		setVars[k] = v
	}
	return baseVars, setVars, nil
}

// loadSecrets reads secret values from files (`--secret-file name=path`) and env
// vars (`--secret-env name=VAR`) — never from the command line (ADR-0018). It
// returns the name→value map (to merge into the highest-precedence tier) and the
// list of non-empty values to redact from all output.
func loadSecrets(files, envs kvFlags) (secrets map[string]string, values []string, err error) {
	secrets = map[string]string{}
	for _, kv := range files {
		name, path, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("--secret-file expects name=path, got %q", kv)
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, nil, fmt.Errorf("--secret-file %s: %v", name, rerr)
		}
		secrets[name] = strings.TrimRight(string(b), "\r\n") // drop a trailing newline
	}
	for _, kv := range envs {
		name, envvar, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("--secret-env expects name=VAR, got %q", kv)
		}
		secrets[name] = os.Getenv(envvar)
	}
	for _, v := range secrets {
		if v != "" {
			values = append(values, v)
		}
	}
	return secrets, values, nil
}

// tracer builds the transport's diagnostic callback, or nil when `-v` was not given.
//
// Masking happens here rather than in the transport: the CLI is what knows the run's
// secrets. A diagnostic channel that prints what the report masks would be worse than no
// diagnostic channel at all (#461). stderr, so a report on stdout stays parseable —
// including under `--json`.
func tracer(on bool, secrets []string) func(string, ...any) {
	if !on {
		return nil
	}
	return func(format string, a ...any) {
		fmt.Fprintln(os.Stderr, "· "+report.Redact(fmt.Sprintf(format, a...), secrets))
	}
}

// cleanCmd: shellf clean --inventory <hosts.shellf> [target...]. Kills resident
// agents and removes shellf's /tmp files on each target (all hosts if no target).
func cleanCmd(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	invPath := fs.String("inventory", "", "inventory file (required)")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	_ = fs.Parse(args) // flag.ExitOnError already exits on a parse error
	if *invPath == "" {
		fmt.Fprintln(os.Stderr, "usage: shellf clean --inventory <hosts.shellf> [--insecure] [target...]")
		os.Exit(2)
	}
	// Through loadInventory like every other command, rather than re-reading and
	// re-parsing here: the duplicate path was one Validate call short, so `clean` was
	// the one command that still accepted a malformed inventory (#451).
	inv, err := loadInventory(*invPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Targets: positional args (hosts or groups), or every host if none given.
	targets := fs.Args()
	if len(targets) == 0 {
		for name := range inv.Hosts {
			targets = append(targets, name)
		}
	}
	// A target nobody declared is refused here too: `shellf clean nope` used to expand
	// to no alias, clean nothing and exit 0, which reads exactly like a target that was
	// already clean (#451).
	var aliases []string
	seen := map[string]bool{}
	for _, t := range targets {
		members, known := inv.Members(t)
		if !known {
			fmt.Fprintf(os.Stderr, "unknown target: the inventory declares no host or group named %q\n", t)
			os.Exit(1)
		}
		for _, a := range members {
			if !seen[a] {
				seen[a] = true
				aliases = append(aliases, a)
			}
		}
	}

	anyErr := false
	for _, alias := range aliases {
		h, _ := inv.Resolve(alias)
		if h.Local { // a local host pushes nothing, so there is nothing to clean (ADR-0027)
			fmt.Printf("  %s: nothing to clean (local)\n", alias)
			continue
		}
		s := transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure,
		}
		if err := s.Clean(); err != nil {
			fmt.Printf("  %s: %v\n", alias, err)
			anyErr = true
		} else {
			fmt.Printf("  %s: cleaned\n", alias)
		}
	}
	exitFor(anyErr)
}

// statusCmd: shellf status --inventory <hosts.shellf> <plan.shellf>. Reports
// each declared resource's current-vs-desired state, read-only (ADR-0013).
func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	// The same inputs `run` takes, from the same definition: `status` answers "what would
	// this plan see?", which it cannot do without being handed what the plan sees (#640).
	// It has no mode of its own — reading the state without acting is the whole command.
	in := registerPlanInputs(fs, "sweep")
	_ = fs.Parse(args)

	invPath := in.inv
	insecure, knownHosts := in.insecure, in.knownHosts
	parallel, verbose, asJSON, limits := in.parallel, in.verbose, in.asJSON, in.limits
	checkParallel(fs, *parallel)

	if fs.NArg() < 1 || *invPath == "" {
		fmt.Fprintln(os.Stderr, "usage: shellf status --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--insecure] <plan.shellf>")
		os.Exit(2)
	}
	secrets, secretValues, err := loadSecrets(in.secretFile, in.secretEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Exactly `run`'s layering (ADR-0018): globals from --vars, then --set on top, then
	// secrets, which win. Passing an empty base here is what made a plan using `${k}` fail
	// to resolve under `status` while applying cleanly under `run`.
	base, setVars, err := loadGlobals(*in.vars, in.sets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range secrets {
		setVars[k] = v
	}
	plan, defsSrc, validate, err := project.Load(fs.Arg(0), *invPath, base, setVars)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inv, err := loadInventory(*invPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// `status` needs the channel too: an `observe` may call a primitive (#334).
	// The secrets sit in the --set layer, exactly as `run` merges them (ADR-0018):
	// a template naming a secret must render in `status` too, or `status` reports an
	// error on a plan that applies cleanly.
	channelFor := controlChannel(fs.Arg(0), plan, inv, base, setVars)

	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		if h.Local { // reached on the control host, no SSH (ADR-0027)
			return transport.Local{Channel: channelFor(alias)}
		}
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure, AgentTTL: *in.agentTTL,
			Trace:   tracer(*verbose, secretValues),
			Channel: channelFor(alias),
		}
	}
	// `status` refuses an unknown target like `run` does. The render stays pure — the
	// exit code is the caller's call, so a report string keeps one job (#451).
	reports := orchestrator.Run(plan, inv, self, "status", dial, base, setVars, defsSrc, orchestrator.Options{Parallel: *parallel, Limit: limits, Verbose: *verbose, ValidateArgs: validate})
	// The verdict comes from the renderer that produced the report, as it does for `run`:
	// asking a second function is how `status` came to exit 0 over a fleet where every host
	// was unreachable (#615).
	if *asJSON {
		out, anyErr, err := report.JSON(reports)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(report.RedactJSON(out, secretValues))
		exitFor(anyErr)
		return
	}
	text, anyErr := report.Status(reports)
	fmt.Print(report.Redact(text, secretValues))
	exitFor(anyErr)
}

// allUnknownTargets reports whether every block failed on an unknown target — the shape
// `orchestrator.Run` returns when it refuses a plan before executing it.

// printReports writes the finished report and sets the exit code. The rendering is
// internal/report (#491); what stays here is the part a package cannot own — stdout and
// the process.
func printReports(reports []orchestrator.BlockReport, secrets []string, asJSON bool) {
	out, anyErr, err := report.Render(reports, secrets, asJSON)
	if err != nil {
		// The report is what the operator came for, so a render that cannot finish is
		// fatal — but the decision is here, where the process lives, not in the package
		// that builds the string (#641).
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(out)
	exitFor(anyErr)
}

func exitFor(isErr bool) {
	if isErr {
		os.Exit(1)
	}
}
