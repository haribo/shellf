package main

import (
	"io"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
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

// runCmd: shellf run <plan.shellf> --inventory <hosts.shellf> [--check] [flags].
func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	invPath := fs.String("inventory", "", "inventory file (required)")
	varsPath := fs.String("vars", "", "vars file: global `name = value` bindings")
	var sets, secretFiles, secretEnvs kvFlags
	fs.Var(&sets, "set", "override a variable, k=v (repeatable); wins over --vars and plan bindings")
	fs.Var(&secretFiles, "secret-file", "secret from a file, name=path (repeatable); redacted in output")
	fs.Var(&secretEnvs, "secret-env", "secret from an env var, name=VAR (repeatable); redacted in output")
	dryRun := fs.Bool("dry-run", false, "decide and preview without mutating")
	// `--check` was the old name (ADR-0035). Accepting it silently would keep two
	// spellings alive; this only exists to say what to type instead.
	oldCheck := fs.Bool("check", false, "")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	agentTTL := fs.Duration("agent-ttl", 0, "resident agent inactivity TTL before it self-erases (0 = 2h)")
	_ = fs.Parse(args) // flag.ExitOnError already exits on a parse error

	// Before anything is read: a wrong flag must be the error the operator sees, not a
	// missing file that happens to be reported first.
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

	// What the plan may ask the control host for (ADR-0034 §5 → ADR-0031 §3). Derived
	// from the defs before anything is sent: the channel serves this set and refuses
	// the rest by name, which is what keeps an imported def from reading ~/.ssh.
	planDir := filepath.Dir(fs.Arg(0))
	var allSteps []proto.Step
	for _, b := range plan {
		allSteps = append(allSteps, b.Steps...)
	}
	declared := lang.ControlResources(parseDefsFor(defsSrc), allSteps)
	var channel func(io.Reader, io.WriteCloser) error
	if len(declared) > 0 {
		allow := orchestrator.NewAllowed(planDir, declared)
		channel = func(r io.Reader, w io.WriteCloser) error {
			c := proto.NewConnRW(r, w)
			if err := c.Handshake(); err != nil {
				return err
			}
			return orchestrator.Serve(c, allow)
		}
	}

	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		if h.Local { // reached on the control host, no SSH (ADR-0027)
			return transport.Local{Channel: channel}
		}
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure, AgentTTL: *agentTTL,
			Channel: channel, // nil when the plan asks nothing: no bridge is opened
		}
	}

	render := templateRenderer(planDir)
	printReports(orchestrator.Run(plan, inv, self, mode, dial, baseVars, setVars, defsSrc, render), secretValues)
}

// parseDefsFor re-parses the shipped def sources so their `%` occurrences can be
// extracted. The sources are what travels to the agent (ADR-0014), so parsing them here
// scans exactly what will run there.
func parseDefsFor(srcByName map[string]string) map[string]lang.Def {
	out := map[string]lang.Def{}
	for name, src := range srcByName {
		defs, err := lang.ParseDefs(src)
		if err != nil || len(defs) != 1 {
			continue // it already parsed once; a failure here cannot be actioned
		}
		out[name] = defs[0]
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
	libs, err := packageLibs(planPath, invPath)
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
	// `file.template` steps are NOT resolved here: they render per host, in the
	// orchestrator, over each host's env (ADR-0024). See templateRenderer.
	//
	// `dir.copy` IS resolved here: its bytes are control-side and identical for
	// every host, so it expands once into dir-ensure + file-put steps (ADR-0028).
	planDir := filepath.Dir(planPath)
	for bi := range plan {
		expanded, err := resolveDirCopy(plan[bi].Steps, planDir)
		if err != nil {
			return nil, nil, err
		}
		plan[bi].Steps = expanded
	}
	return plan, defSource(defs), nil
}

// dirCopyCeiling bounds the base64-encoded payload a single dir-copy may carry, so
// a large tree is refused with a clear error instead of OOMing the agent (ADR-0028).
// A var, not a const, so a test can lower it without a 32 MB fixture.
var dirCopyCeiling int64 = 32 << 20

// resolveDirCopy expands every `dir.copy(src, dst)` step into a `dir.ensure` per
// directory and a `file.put(dst, base64)` per file, reading src relative to the
// plan dir. Recurses into if/block/parallel; other steps pass through.
func resolveDirCopy(steps []proto.Step, dir string) ([]proto.Step, error) {
	var out []proto.Step
	for _, s := range steps {
		switch {
		case s.Instruction == "dir.copy":
			if s.Refs["src"] != "" || s.Refs["dst"] != "" {
				return nil, fmt.Errorf("dir-copy: src and dst must be literal paths, not per-host refs")
			}
			expanded, err := expandTree(srcPath(dir, s.Args["src"]), s.Args["dst"])
			if err != nil {
				return nil, err
			}
			out = append(out, expanded...)
		case s.If != nil:
			// A condition is one step yielding one Result, but dir-copy expands to
			// one step per file — there is nothing sound to put there. Refuse it
			// here, with the reason, rather than ship `dir.copy` to the agent and
			// surface the opaque `err.agent` it dies on (#293).
			if s.If.Cond != nil && s.If.Cond.Instruction == "dir.copy" {
				return nil, fmt.Errorf("dir-copy: cannot be used as a condition (it expands to one step per file)")
			}
			then, err := resolveDirCopy(s.If.Then, dir)
			if err != nil {
				return nil, err
			}
			els, err := resolveDirCopy(s.If.Else, dir)
			if err != nil {
				return nil, err
			}
			ib := *s.If
			ib.Then, ib.Else = then, els
			ns := s
			ns.If = &ib
			out = append(out, ns)
		case len(s.Block) > 0:
			sub, err := resolveDirCopy(s.Block, dir)
			if err != nil {
				return nil, err
			}
			ns := s
			ns.Block = sub
			out = append(out, ns)
		case len(s.Parallel) > 0:
			sub, err := resolveDirCopy(s.Parallel, dir)
			if err != nil {
				return nil, err
			}
			ns := s
			ns.Parallel = sub
			out = append(out, ns)
		default:
			out = append(out, s)
		}
	}
	return out, nil
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

// srcPath resolves a control-side `src`: absolute paths are used as-is, relative
// ones are joined to the plan dir (#281). Shared by `dir.copy` and `file.template` so
// the two cannot drift.
func srcPath(planDir, src string) string {
	if filepath.IsAbs(src) {
		return src
	}
	return filepath.Join(planDir, src)
}

// expandTree walks srcRoot (control host) and returns the dir-ensure + file-put
// steps that deliver it verbatim under dstRoot (target). Refuses a tree whose
// base64 payload exceeds the ceiling (ADR-0028).
func expandTree(srcRoot, dstRoot string) ([]proto.Step, error) {
	info, err := os.Stat(srcRoot)
	if err != nil {
		return nil, fmt.Errorf("dir-copy: %v", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("dir-copy: %s is not a directory", srcRoot)
	}
	steps := []proto.Step{{Instruction: "dir.ensure", Args: map[string]string{"path": dstRoot}}}
	var total int64
	walkErr := filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			steps = append(steps, proto.Step{Instruction: "dir.ensure", Args: map[string]string{"path": dst}})
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(b)
		total += int64(len(enc))
		if total > dirCopyCeiling {
			return fmt.Errorf("dir-copy: %s exceeds the %d MB payload ceiling (ADR-0028)", srcRoot, dirCopyCeiling>>20)
		}
		steps = append(steps, proto.Step{Instruction: "file.put", Args: map[string]string{"path": dst, "content": enc}})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return steps, nil
}

// templateRenderer builds the per-host renderer the orchestrator injects
// (ADR-0024): it reads a template `src` relative to the plan dir and interpolates
// `@{var}` over the host's vars. `src` stays a control-host path.
func templateRenderer(planDir string) orchestrator.TemplateRenderer {
	return func(src string, vars map[string]string) (string, error) {
		return renderTemplate(srcPath(planDir, src), vars)
	}
}

func renderTemplate(path string, vars map[string]string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("file.template: %v", err)
	}
	return lang.Template(string(b), func(n string) (string, bool) { v, ok := vars[n]; return v, ok })
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
	dir := filepath.Dir(planPath)
	lock, err := module.LoadLock(dir)
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
		if err := module.SaveLock(dir, lock); err != nil {
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

// packageLibs reads every sibling `*.shellf` file in the plan's directory (the
// package), excluding the plan file itself and the inventory file. It also reads one
// level of subdirectories: each is a sub-package, and its files are keyed `<dir>/<file>`
// so the parser qualifies their defs as `<dir>.<def>` (ADR-0033). Two levels down is an
// error, not a silent skip — a skipped directory is how an override fails to apply
// while the plan reports success.
func packageLibs(planPath, invPath string) (map[string]string, error) {
	dir := filepath.Dir(planPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	planAbs, _ := filepath.Abs(planPath)
	invAbs, _ := filepath.Abs(invPath)
	libs := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			if err := readSubPackage(dir, e.Name(), libs); err != nil {
				return nil, err
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".shellf") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		abs, _ := filepath.Abs(path)
		if abs == planAbs || abs == invAbs {
			continue // the plan carries the `on` blocks; the inventory is parsed apart
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		libs[e.Name()] = string(src)
	}
	return libs, nil
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
	return inv, nil
}

// stdSignatures resolves an instruction's parameter names from the embedded
// stdlib (signatures live with the defs, self-hosting) plus the Go builtins —
// so adding a def needs no parser-side edit (#107).
func stdSignatures() lang.InstructionSig {
	// params, and how many are required.
	builtins := map[string]struct {
		params   []string
		required int
	}{
		"file.copy":     {[]string{"src", "dst"}, 2},
		"file.template": {[]string{"src", "dst"}, 2},
		"dir.copy":      {[]string{"src", "dst"}, 2},
	}
	return func(name string) ([]string, int, bool) {
		if b, ok := builtins[name]; ok {
			return b.params, b.required, true
		}
		if def, ok := std.Lookup(name); ok {
			names := make([]string, len(def.Params))
			required := 0
			for i, p := range def.Params {
				names[i] = p.Name
				if p.Default == nil {
					required++
				}
			}
			return names, required, true
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
	invSrc, err := os.ReadFile(*invPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inv, err := lang.ParseInventory(string(invSrc))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", *invPath, err)
		os.Exit(1)
	}

	// Targets: positional args (hosts or groups), or every host if none given.
	targets := fs.Args()
	if len(targets) == 0 {
		for name := range inv.Hosts {
			targets = append(targets, name)
		}
	}
	var aliases []string
	seen := map[string]bool{}
	for _, t := range targets {
		for _, a := range inv.Members(t) {
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
	var secretFiles, secretEnvs kvFlags
	fs.Var(&secretFiles, "secret-file", "secret from a file, name=path (repeatable); redacted in output")
	fs.Var(&secretEnvs, "secret-env", "secret from an env var, name=VAR (repeatable); redacted in output")
	_ = fs.Parse(args)

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
	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		if h.Local { // reached on the control host, no SSH (ADR-0027)
			return transport.Local{}
		}
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure,
		}
	}
	render := templateRenderer(filepath.Dir(fs.Arg(0)))
	fmt.Print(redact(statusReport(orchestrator.Run(plan, inv, self, "status", dial, base, secrets, defsSrc, render)), secretValues))
}

// statusReport renders the per-host state report: one line per resource, with a
// `current → desired` diff on each field that has drifted. Pure (returns the
// text) so it is unit-testable without capturing stdout.
func statusReport(reports []orchestrator.BlockReport) string {
	var b strings.Builder
	for _, blk := range reports {
		fmt.Fprintf(&b, "on %s:\n", blk.Target)
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

func printReports(reports []orchestrator.BlockReport, secrets []string) {
	text, anyErr := reportText(reports)
	fmt.Print(redact(text, secrets))
	exitFor(anyErr)
}

// reportText renders the run/check report and reports whether any host errored.
// Pure (returns the text) so it is unit-testable without capturing stdout.
func reportText(reports []orchestrator.BlockReport) (string, bool) {
	var b strings.Builder
	anyErr := false
	for _, blk := range reports {
		fmt.Fprintf(&b, "on %s:\n", blk.Target)
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
				anyErr = anyErr || s.Category == "err"
			}
			if h.Response.Halted {
				fmt.Fprintf(&b, "    (halted)\n")
			}
		}
	}
	return b.String(), anyErr
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
