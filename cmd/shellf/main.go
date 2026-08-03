package main

import (
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
		if err := agent.Serve(os.Stdin, os.Stdout, engine.ShellExecutor{}); err != nil {
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

	fmt.Fprint(os.Stderr, "usage:\n"+
		"  shellf run --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--secret-file n=path] [--check] [--insecure] <plan.shellf>\n"+
		"  shellf status --inventory <hosts.shellf> [--insecure] <plan.shellf>\n"+
		"  shellf clean --inventory <hosts.shellf> [--insecure] [target...]\n")
	os.Exit(2)
}

// runCmd: shellf run <plan.shellf> --inventory <hosts.shellf> [--check] [flags].
func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	invPath := fs.String("inventory", "", "inventory file (required)")
	varsPath := fs.String("vars", "", "vars file: global `name = value` bindings")
	var sets, secretFiles, secretEnvs kvFlags
	fs.Var(&sets, "set", "override a variable, k=v (repeatable); wins over --vars and plan bindings")
	fs.Var(&secretFiles, "secret-file", "secret from a file, name=path (repeatable); redacted in output")
	fs.Var(&secretEnvs, "secret-env", "secret from an env var, name=VAR (repeatable); redacted in output")
	check := fs.Bool("check", false, "dry-run: decide without mutating")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	agentTTL := fs.Duration("agent-ttl", 0, "resident agent inactivity TTL before it self-erases (0 = 2h)")
	_ = fs.Parse(args) // flag.ExitOnError already exits on a parse error

	if fs.NArg() < 1 || *invPath == "" {
		fmt.Fprintln(os.Stderr, "usage: shellf run --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--secret-file n=path] [--check] [--insecure] <plan.shellf>")
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
	if *check {
		mode = "check"
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure, AgentTTL: *agentTTL,
		}
	}

	printReports(orchestrator.Run(plan, inv, self, mode, dial, baseVars, setVars, defsSrc), secretValues)
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
	// Resolve `template(src, dst)` on the control host into a file-write of the
	// rendered file, before anything ships (ADR-0019).
	vars := mergedVars(baseVars, setVars)
	for bi := range plan {
		if err := resolveTemplates(plan[bi].Steps, filepath.Dir(planPath), vars); err != nil {
			return nil, nil, err
		}
	}
	return plan, defSource(defs), nil
}

// resolveTemplates rewrites each `template(src, dst)` step to `file-write(dst,
// <rendered src>)`, reading src (relative to the plan dir) and interpolating
// `${var}` with the global vars (ADR-0019). Recurses into control-flow steps.
func resolveTemplates(steps []proto.Step, dir string, vars map[string]string) error {
	for i := range steps {
		s := &steps[i]
		if s.Instruction == "template" {
			if s.Refs["src"] != "" || s.Refs["dst"] != "" {
				return fmt.Errorf("template: src and dst must be literal paths, not per-host refs")
			}
			content, err := renderTemplate(filepath.Join(dir, s.Args["src"]), vars)
			if err != nil {
				return err
			}
			s.Instruction, s.Args, s.Refs = "file-write", map[string]string{"path": s.Args["dst"], "content": content}, nil
			continue
		}
		if s.If != nil { // a template is a file-write, never a condition
			if err := resolveTemplates(s.If.Then, dir, vars); err != nil {
				return err
			}
			if err := resolveTemplates(s.If.Else, dir, vars); err != nil {
				return err
			}
		}
		if err := resolveTemplates(s.Block, dir, vars); err != nil {
			return err
		}
		if err := resolveTemplates(s.Parallel, dir, vars); err != nil {
			return err
		}
	}
	return nil
}

func renderTemplate(path string, vars map[string]string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("template: %v", err)
	}
	return lang.Interpolate(string(b), func(n string) (string, bool) { v, ok := vars[n]; return v, ok })
}

// mergedVars flattens the base and set tiers (set wins) for template rendering.
func mergedVars(base, set map[string]string) map[string]string {
	m := make(map[string]string, len(base)+len(set))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range set {
		m[k] = v
	}
	return m
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
// package), excluding the plan file itself and the inventory file.
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".shellf") {
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
	builtins := map[string][]string{"file-copy": {"src", "dst"}, "template": {"src", "dst"}}
	return func(name string) ([]string, int, bool) {
		if p, ok := builtins[name]; ok {
			return p, len(p), true // builtins have no optional params
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
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure,
		}
	}
	fmt.Print(redact(statusReport(orchestrator.Run(plan, inv, self, "status", dial, base, secrets, defsSrc)), secretValues))
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
	default: // control-flow wrappers, questions, pre-check errors — one line, no recursion
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
	for _, sub := range s.Sub {
		stepText(b, sub, indent+"  ")
	}
}

func exitFor(isErr bool) {
	if isErr {
		os.Exit(1)
	}
}
