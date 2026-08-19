package main

import (
	"encoding/json"
	"errors"
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
	"shellf/internal/module"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
	"shellf/internal/std"
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

// runCmd: shellf run <plan.shellf> --inventory <hosts.shellf> [--dry-run] [flags].
func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	invPath := fs.String("inventory", "", "inventory file (required)")
	varsPath := fs.String("vars", "", "vars file: global `name = value` bindings")
	var sets, secretFiles, secretEnvs kvFlags
	fs.Var(&sets, "set", "override a variable, k=v (repeatable); wins over --vars and plan bindings")
	fs.Var(&secretFiles, "secret-file", "secret from a file, name=path (repeatable); redacted in output")
	fs.Var(&secretEnvs, "secret-env", "secret from an env var, name=VAR (repeatable); redacted in output")
	dryRun := fs.Bool("dry-run", false, "decide and preview without mutating")
	asJSON := fs.Bool("json", false, "report as JSON on stdout (diagnostics stay on stderr)")
	verbose := fs.Bool("v", false, "trace the control host's decisions on stderr (connection, agent, workdir, timing)")
	// `--check` was the old name (ADR-0035). Accepting it silently would keep two
	// spellings alive; this only exists to say what to type instead.
	oldCheck := fs.Bool("check", false, "")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	agentTTL := fs.Duration("agent-ttl", 0, "resident agent inactivity TTL before it self-erases (0 = 2h)")
	parallel := fs.Int("parallel", 0, "hosts dialled at once (0 = 16); 1 serialises the fan-out")
	var limits multiFlag
	fs.Var(&limits, "limit", "restrict the run to a host or group (repeatable); narrows the plan, never extends it")
	_ = fs.Parse(args) // flag.ExitOnError already exits on a parse error

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
	plan, defsSrc, err := loadPlanPackage(fs.Arg(0), *invPath, baseVars, setVars)
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

	opt := orchestrator.Options{Parallel: *parallel, Limit: limits}
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
	root, err := projectRoot(planPath)
	if err != nil {
		root = filepath.Dir(filepath.Dir(planPath))
	}
	assetsDir := filepath.Join(root, dirAssets)
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
		env := mergeVars(baseVars, host.Vars, setVars)
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

// mergeVars layers the variable tables the way the orchestrator does: globals, then the
// host's own, then --set (ADR-0022 precedence).
func mergeVars(base, host, set map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range []map[string]string{base, host, set} {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
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

// loadPlanPackage loads the plan file together with its package — every other
// `*.shellf` file in the same directory (ADR-0014), so user defs written in a
// sibling file resolve by name. Returns the plan and the concatenated user def
// source to ship to the agent. baseVars is enriched in place with plan bindings.
func loadPlanPackage(planPath, invPath string, baseVars, setVars map[string]string) (orchestrator.Plan, map[string]string, error) {
	planSrc, err := os.ReadFile(planPath)
	if err != nil {
		return nil, nil, err
	}
	root, err := projectRoot(planPath)
	if err != nil {
		return nil, nil, err
	}
	libs, err := packageLibs(root)
	if err != nil {
		return nil, nil, err
	}
	imports, err := readImports(planPath, string(planSrc))
	if err != nil {
		return nil, nil, err
	}
	plan, defs, err := lang.ParsePackage(string(planSrc), libs, imports, baseVars, setVars, stdSignatures())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", planPath, err)
	}
	// A call cycle is a writing error, refused from reading the files (ADR-0030 §6). Here
	// and not in `lang`, because the graph spans two sets no single package sees: these
	// user defs, and the stdlib, which `lang` cannot import. The evaluator keeps its own
	// guard, but it fires on the target, after earlier steps have already acted (#311).
	if err := lang.CheckCycles(defs, cycleResolver(defs)); err != nil {
		return nil, nil, fmt.Errorf("%s: %v", planPath, err)
	}
	// `file.template` steps are NOT resolved here: they render per host, in the
	// orchestrator, over each host's env (ADR-0024). See templateRenderer.
	//
	// `dir.copy` IS resolved here: its bytes are control-side and identical for
	// every host, so it expands once into dir-ensure + file-put steps (ADR-0028).
	// No control-side expansion left: `file.template` stopped being one in #334, and
	// `dir.copy` is a def over `~dir.sync` since #335. A plan now reaches the agent as
	// written.
	return plan, defSource(defs), nil
}

// readSubPackage reads one sub-package directory into libs, keyed `<name>/<file>`.
// A directory holding no `.shellf` file is ignored (it is content, not code — a
// `templates/` or `html/` tree next to a plan is ordinary). A directory that does hold
// code but nests another one is refused: ADR-0032 fixes exactly one dot per name, so a
// second level would produce `a.b.c`.
func readSubPackage(parent, name string, libs map[string]string) error {
	sub := filepath.Join(parent, name)
	entries, err := os.ReadDir(sub)
	if err != nil {
		return err
	}
	var code []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".shellf") {
			code = append(code, e)
		}
	}
	if len(code) == 0 {
		return nil // content directory, not a sub-package
	}
	for _, e := range entries {
		if e.IsDir() {
			return fmt.Errorf("%s: a sub-package may not contain a directory (%q) — one level only, ADR-0033", sub, e.Name())
		}
	}
	for _, e := range code {
		src, err := os.ReadFile(filepath.Join(sub, e.Name()))
		if err != nil {
			return err
		}
		libs[name+"/"+e.Name()] = string(src)
	}
	return nil
}

// readImports resolves each `import <alias> "<spec>"` in the plan to the def
// sources of that package. A spec with `@version` is a remote git module
// (ADR-0016), resolved through shellf.lock + the module cache; otherwise it is a
// local directory relative to the plan file (ADR-0015).
func readImports(planPath, planSrc string) (map[string][]string, error) {
	imps, err := lang.ScanImports(planSrc)
	if err != nil {
		return nil, err
	}
	if len(imps) == 0 {
		return nil, nil
	}
	// A local import path is relative to the plan file (ADR-0015, unchanged). The lock is
	// not: it pins what the *project* depends on, like go.sum, so it belongs at the root
	// rather than among the plans. ADR-0038 did not foresee this; recorded in the PR.
	dir := filepath.Dir(planPath)
	lockDir := dir
	if root, err := projectRoot(planPath); err == nil {
		lockDir = root
	}
	lock, err := module.LoadLock(lockDir)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	lockChanged := false
	for _, imp := range imps {
		if spec, remote := module.ParseSpec(imp.Path); remote {
			srcs, changed, err := module.ResolveLocked(spec, moduleCache(), lock)
			if err != nil {
				return nil, fmt.Errorf("import %q: %v", imp.Alias, err)
			}
			out[imp.Alias] = srcs
			lockChanged = lockChanged || changed
			continue
		}
		importDir := filepath.Join(dir, imp.Path)
		info, err := os.Stat(importDir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("import %q: %q is not a directory", imp.Alias, imp.Path)
		}
		srcs, err := shellfSources(importDir)
		if err != nil {
			return nil, err
		}
		out[imp.Alias] = srcs
	}
	if lockChanged {
		if err := module.SaveLock(lockDir, lock); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// moduleCache is where fetched remote modules live, content-addressed by SHA
// (ADR-0016): ~/.cache/shellf/modules.
func moduleCache() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "shellf", "modules")
}

// shellfSources reads every `*.shellf` file in a directory (an imported package).
func shellfSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var srcs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".shellf") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, string(src))
	}
	return srcs, nil
}

// packageLibs reads every def package under `<root>/defs/` (ADR-0038 §2). Each
// `defs/<name>/` is one package, and its files are keyed `<name>/<file>` — the same key
// shape ADR-0033 already uses for sub-packages, so the parser qualifies the defs
// `<name>.<def>` with no further work.
//
// A plan's siblings are no longer defs. That is the convenience ADR-0014 §1 bought and
// this layout removes: a def is addressed by name, so it lives where the name says.
func packageLibs(root string) (map[string]string, error) {
	defsDir := filepath.Join(root, dirDefs)
	entries, err := os.ReadDir(defsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil // a project may have no defs of its own
		}
		return nil, err
	}
	libs := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			if strings.HasSuffix(e.Name(), ".shellf") {
				return nil, fmt.Errorf("%s: a def belongs to a package directory — "+
					"move it to defs/<package>/%s (ADR-0038)", filepath.Join(defsDir, e.Name()), e.Name())
			}
			continue
		}
		if err := readSubPackage(defsDir, e.Name(), libs); err != nil {
			return nil, err
		}
	}
	return libs, nil
}

// Project layout (ADR-0038). A plan lives in `<root>/plans/`, so the root is its parent —
// and `defs/`, `assets/` and `inventories/` hang off the same root. Directory names, not
// a marker file: the layout already identifies the project, and a second mechanism to say
// the same thing is one to keep in sync.
const (
	dirPlans  = "plans"
	dirDefs   = "defs"
	dirAssets = "assets"
)

// projectRoot returns the directory holding the layout, given the invoked plan.
//
// The error is most of this function's value: it is the first thing anyone running shellf
// outside a project will see, so it names the layout rather than reporting a file that
// could not be found somewhere inside the loader.
func projectRoot(planPath string) (string, error) {
	abs, err := filepath.Abs(planPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if filepath.Base(dir) != dirPlans {
		return "", fmt.Errorf("%s is not inside a shellf project: a plan lives in `plans/`, "+
			"beside `defs/`, `assets/` and `inventories/` (ADR-0038)", planPath)
	}
	return filepath.Dir(dir), nil
}

// cycleResolver is the lookup a run uses — a package user def first, so an
// `override def` wins, then the stdlib (ADR-0014). Giving the cycle check the same order
// is the point: it must see the graph the run will walk, or it misses the case where a
// user def redirects a stdlib one back into itself.
func cycleResolver(defs map[string]lang.Def) lang.DefResolver {
	return func(name string) (lang.Def, bool) {
		if d, ok := defs[name]; ok {
			return d, true
		}
		return std.Lookup(name)
	}
}

// defSource maps each resolved instruction name to its def source, for the
// per-host Request — bare for the local package, qualified `alias.def` for
// imports (ADR-0014/0015).
func defSource(defs map[string]lang.Def) map[string]string {
	m := make(map[string]string, len(defs))
	for name, d := range defs {
		m[name] = d.Source
	}
	return m
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

// stdSignatures resolves an instruction's parameter names from the embedded stdlib —
// signatures live with the defs, self-hosting, so adding a def needs no parser-side edit
// (#107).
//
// There is no builtin table beside it any more. It held `file.copy` and `dir.copy` from
// when both were Go transformations; both became defs, and the stale entry shadowed the
// real signature — `dir.copy(%"src", dst, "sha256")` was documented and refused, because
// the table still said two parameters while the def had grown `compare` (#414). A
// signature written in two places is a signature that drifts.
func stdSignatures() lang.InstructionSig {
	return func(name string) ([]lang.Param, int, bool) {
		if def, ok := std.Lookup(name); ok {
			required := 0
			for _, p := range def.Params {
				if p.Default == nil {
					required++
				}
			}
			// The def's own parameters, types included: a signature written twice is a
			// signature that drifts (#414), and the type is what ADR-0045 checks against.
			return def.Params, required, true
		}
		return nil, 0, false
	}
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

// redact masks every non-empty secret value with `***` (by value, so it catches
// a secret wherever it surfaces — a label, a report, an echoed stdout). ADR-0018.
func redact(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
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
		fmt.Fprintln(os.Stderr, "· "+redact(fmt.Sprintf(format, a...), secrets))
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
	invPath := fs.String("inventory", "", "inventory file (required)")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	// `status` sweeps the fleet exactly like `run` does, so it takes the same knobs.
	parallel := fs.Int("parallel", 0, "hosts dialled at once (0 = 16); 1 serialises the fan-out")
	var limits multiFlag
	fs.Var(&limits, "limit", "restrict the sweep to a host or group (repeatable)")
	asJSON := fs.Bool("json", false, "report as JSON on stdout (diagnostics stay on stderr)")
	var secretFiles, secretEnvs kvFlags
	fs.Var(&secretFiles, "secret-file", "secret from a file, name=path (repeatable); redacted in output")
	fs.Var(&secretEnvs, "secret-env", "secret from an env var, name=VAR (repeatable); redacted in output")
	_ = fs.Parse(args)
	checkParallel(fs, *parallel)

	if fs.NArg() < 1 || *invPath == "" {
		fmt.Fprintln(os.Stderr, "usage: shellf status --inventory <hosts.shellf> [--insecure] <plan.shellf>")
		os.Exit(2)
	}
	secrets, secretValues, err := loadSecrets(secretFiles, secretEnvs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	base := map[string]string{}
	plan, defsSrc, err := loadPlanPackage(fs.Arg(0), *invPath, base, secrets)
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
	// `secrets` sits in the --set layer here, exactly as `run` merges it (ADR-0018):
	// a template naming a secret must render in `status` too, or `status` reports an
	// error on a plan that applies cleanly.
	channelFor := controlChannel(fs.Arg(0), plan, inv, base, secrets)

	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		if h.Local { // reached on the control host, no SSH (ADR-0027)
			return transport.Local{Channel: channelFor(alias)}
		}
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure,
			Channel: channelFor(alias),
		}
	}
	// `status` refuses an unknown target like `run` does. The render stays pure — the
	// exit code is the caller's call, so a report string keeps one job (#451).
	reports := orchestrator.Run(plan, inv, self, "status", dial, base, secrets, defsSrc, orchestrator.Options{Parallel: *parallel, Limit: limits})
	if *asJSON {
		out, _ := reportJSON(reports)
		fmt.Print(redactJSON(out, secretValues))
		exitFor(anyBlockError(reports))
		return
	}
	fmt.Print(redact(statusReport(reports), secretValues))
	exitFor(anyBlockError(reports))
}

// anyBlockError reports whether any block failed as a whole (an unknown target), as
// opposed to a per-host outcome.
func anyBlockError(reports []orchestrator.BlockReport) bool {
	for _, blk := range reports {
		if blk.Err != nil {
			return true
		}
	}
	return false
}

// statusReport renders the per-host state report: one line per resource, with a
// `current → desired` diff on each field that has drifted. Pure (returns the
// text) so it is unit-testable without capturing stdout.
func statusReport(reports []orchestrator.BlockReport) string {
	var b strings.Builder
	for _, blk := range reports {
		fmt.Fprintf(&b, "on %s:\n", blk.Target)
		// Block error and empty block, rendered as in reportText (#451).
		if blk.Err != nil {
			fmt.Fprintf(&b, "  ! %v\n", blk.Err)
			continue
		}
		if len(blk.Hosts) == 0 {
			fmt.Fprintf(&b, "  (no hosts)\n")
			continue
		}
		for _, h := range blk.Hosts {
			if h.Err != nil {
				fmt.Fprintf(&b, "  %s: unreachable (%v)\n", h.Host, h.Err)
				continue
			}
			fmt.Fprintf(&b, "  %s:\n", h.Host)
			for _, s := range h.Response.Results {
				statusStep(&b, s, "    ")
			}
		}
	}
	return b.String()
}

func statusStep(b *strings.Builder, s proto.StepResult, indent string) {
	switch {
	case len(s.Fields) > 0:
		fmt.Fprintf(b, "%s%s:\n", indent, s.Label)
		for _, f := range s.Fields {
			if f.Converged {
				fmt.Fprintf(b, "%s  %s: %s\n", indent, f.Name, orDash(f.Current))
			} else {
				fmt.Fprintf(b, "%s  %s: %s → %s\n", indent, f.Name, orDash(f.Current), f.Desired)
			}
		}
	case s.Tag == "action":
		fmt.Fprintf(b, "%s%-28s action (no observable state)\n", indent, s.Label)
	default: // control-flow wrappers, questions, check errors — one line, no recursion
		label := s.Category
		if s.Tag != "" {
			label += "." + s.Tag
		}
		fmt.Fprintf(b, "%s%-28s %s\n", indent, s.Label, label)
		// An error with no message is a dead end: `err.agent` alone sends the operator
		// looking at the target when the cause may be on their own machine. `run`
		// already prints it; `status` was silent.
		if s.Category == "err" && s.Shell != nil && strings.TrimSpace(s.Shell.Stderr) != "" {
			fmt.Fprintf(b, "%s  ! %s\n", indent, strings.TrimSpace(s.Shell.Stderr))
		}
	}
	for _, sub := range s.Sub {
		statusStep(b, sub, indent+"  ")
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func printReports(reports []orchestrator.BlockReport, secrets []string, asJSON bool) {
	if asJSON {
		// stdout carries the report and nothing else, or it is not parseable. Anything
		// diagnostic belongs on stderr.
		out, anyErr := reportJSON(reports)
		fmt.Print(redactJSON(out, secrets))
		exitFor(anyErr)
		return
	}
	text, anyErr := reportText(reports)
	fmt.Print(redact(text, secrets))
	exitFor(anyErr)
}

// jsonReport is the machine-readable shape of a run. It is a contract the moment it
// ships, so it carries a version: a consumer can detect a change instead of discovering
// it as a parse error in production (#459).
type jsonReport struct {
	Version int         `json:"version"`
	Blocks  []jsonBlock `json:"blocks"`
}

type jsonBlock struct {
	Target string     `json:"target"`
	Error  string     `json:"error,omitempty"` // the block could not run at all (#451)
	Hosts  []jsonHost `json:"hosts"`
}

type jsonHost struct {
	Host    string             `json:"host"`
	Error   string             `json:"error,omitempty"` // unreachable, or a resolution failure
	Halted  bool               `json:"halted,omitempty"`
	Results []proto.StepResult `json:"results,omitempty"`
}

// jsonVersion is bumped when the shape changes in a way a consumer would notice.
const jsonVersion = 1

// reportJSON renders the same run reportText does, as JSON, and reports whether any host
// errored. The two must agree on that boolean: a consumer branching on the exit code and
// a human reading the prose have to see the same run.
func reportJSON(reports []orchestrator.BlockReport) (string, bool) {
	out := jsonReport{Version: jsonVersion, Blocks: make([]jsonBlock, 0, len(reports))}
	anyErr := false

	for _, blk := range reports {
		jb := jsonBlock{Target: blk.Target, Hosts: []jsonHost{}}
		if blk.Err != nil {
			jb.Error = blk.Err.Error()
			anyErr = true
			out.Blocks = append(out.Blocks, jb)
			continue
		}
		for _, h := range blk.Hosts {
			jh := jsonHost{Host: h.Host}
			if h.Err != nil {
				jh.Error = h.Err.Error()
				anyErr = true
				jb.Hosts = append(jb.Hosts, jh)
				continue
			}
			jh.Results, jh.Halted = h.Response.Results, h.Response.Halted
			for _, st := range h.Response.Results {
				// Same rule as the text renderer: a caught error is not a failed run
				// (ADR-0009, #356).
				anyErr = anyErr || (st.Category == "err" && !st.Caught)
			}
			jb.Hosts = append(jb.Hosts, jh)
		}
		out.Blocks = append(out.Blocks, jb)
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		// Nothing here can fail to marshal — every field is a plain Go value — but a
		// silent empty report would be worse than a loud one.
		fmt.Fprintf(os.Stderr, "rendering the JSON report: %v\n", err)
		os.Exit(1)
	}
	return string(b) + "\n", anyErr
}

// redactJSON masks secrets in encoded JSON.
//
// `redact` alone is not enough here, and the gap is not theoretical: JSON escapes quotes,
// backslashes and newlines, so a secret containing any of them is simply *not present*
// verbatim in the encoded bytes — the plain ReplaceAll walks straight past it. Each secret
// is therefore masked in both forms, raw and as JSON would write it.
func redactJSON(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, "***")
		if enc, err := json.Marshal(sec); err == nil && len(enc) >= 2 {
			s = strings.ReplaceAll(s, string(enc[1:len(enc)-1]), "***")
		}
	}
	return s
}

// reportText renders the run/check report and reports whether any host errored.
// Pure (returns the text) so it is unit-testable without capturing stdout.
func reportText(reports []orchestrator.BlockReport) (string, bool) {
	var b strings.Builder
	anyErr := false
	for _, blk := range reports {
		fmt.Fprintf(&b, "on %s:\n", blk.Target)
		// A block that could not run at all: no host to attach an outcome to, so the
		// reason goes on the block. Without this the block printed its header and
		// nothing else, and the run exited 0 (#451).
		if blk.Err != nil {
			fmt.Fprintf(&b, "  ! %v\n", blk.Err)
			anyErr = true
			continue
		}
		// A target that resolves to nobody is a legitimate no-op, and it says so: an
		// empty block reads exactly like a block where everything converged.
		if len(blk.Hosts) == 0 {
			fmt.Fprintf(&b, "  (no hosts)\n")
			continue
		}
		for _, h := range blk.Hosts {
			if h.Err != nil {
				var re *orchestrator.ResolveError
				if errors.As(h.Err, &re) {
					fmt.Fprintf(&b, "  %s: %v\n", h.Host, h.Err) // resolution error, not unreachable
				} else {
					fmt.Fprintf(&b, "  %s: unreachable (%v)\n", h.Host, h.Err)
				}
				anyErr = true
				continue
			}
			fmt.Fprintf(&b, "  %s:\n", h.Host)
			for _, s := range h.Response.Results {
				stepText(&b, s, "    ")
				// A caught error is not a failed run: `?` means the plan handles it,
				// and it did (ADR-0009). Counting it made `shellf run … && …` never
				// succeed for any plan using the language's own error handling (#356).
				anyErr = anyErr || (s.Category == "err" && !s.Caught)
			}
			if h.Response.Halted {
				fmt.Fprintf(&b, "    (halted)\n")
			}
		}
	}
	// Every target was refused, so no block was executed. Say it: the report above is a
	// list of names, and nothing distinguishes it from a run that did work.
	if len(reports) > 0 && allUnknownTargets(reports) {
		fmt.Fprintf(&b, "nothing ran: fix the target name(s) above, or the inventory\n")
	}
	return b.String(), anyErr
}

// allUnknownTargets reports whether every block failed on an unknown target — the shape
// `orchestrator.Run` returns when it refuses a plan before executing it.
func allUnknownTargets(reports []orchestrator.BlockReport) bool {
	for _, blk := range reports {
		var ue *orchestrator.UnknownTargetError
		if !errors.As(blk.Err, &ue) {
			return false
		}
	}
	return true
}

func stepText(b *strings.Builder, s proto.StepResult, indent string) {
	label := s.Category
	if s.Tag != "" {
		label += "." + s.Tag
	}
	fmt.Fprintf(b, "%s%-24s %s\n", indent, s.Label, label)
	// Show the preview/error payload (e.g. a file-copy diff in check mode).
	if s.Shell != nil && s.Shell.Stdout != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Shell.Stdout, "\n"), "\n") {
			fmt.Fprintf(b, "%s    | %s\n", indent, line)
		}
	}
	// On a failure, what went wrong. Without this the diagnostics the agent attaches —
	// an unbound variable, a refused resource, a call cycle — are written and never
	// seen, leaving `err.agent` to mean "something broke, good luck".
	if s.Category == "err" && s.Shell != nil && s.Shell.Stderr != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Shell.Stderr, "\n"), "\n") {
			fmt.Fprintf(b, "%s    ! %s\n", indent, line)
		}
	}
	// An action-shaped def's `--check` preview: what apply would do, marked so it
	// never reads as a convergence claim (ADR-0029).
	if s.Preview != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Preview, "\n"), "\n") {
			fmt.Fprintf(b, "%s    preview ▸ %s\n", indent, line)
		}
	}
	for _, sub := range s.Sub {
		stepText(b, sub, indent+"  ")
	}
}

func exitFor(isErr bool) {
	if isErr {
		os.Exit(1)
	}
}
