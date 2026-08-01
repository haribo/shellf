package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"shellf/internal/agent"
	"shellf/internal/engine"
	"shellf/internal/lang"
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

	// Clean shellf agents and files off the targets.
	if len(os.Args) > 1 && os.Args[1] == "clean" {
		cleanCmd(os.Args[2:])
		return
	}

	fmt.Fprint(os.Stderr, "usage:\n"+
		"  shellf run --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--check] [--insecure] <plan.shellf>\n"+
		"  shellf clean --inventory <hosts.shellf> [--insecure] [target...]\n")
	os.Exit(2)
}

// runCmd: shellf run <plan.shellf> --inventory <hosts.shellf> [--check] [flags].
func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	invPath := fs.String("inventory", "", "inventory file (required)")
	varsPath := fs.String("vars", "", "vars file: global `name = value` bindings")
	var sets kvFlags
	fs.Var(&sets, "set", "override a variable, k=v (repeatable); wins over --vars and plan bindings")
	check := fs.Bool("check", false, "dry-run: decide without mutating")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	agentTTL := fs.Duration("agent-ttl", 0, "resident agent inactivity TTL before it self-erases (0 = 2h)")
	fs.Parse(args)

	if fs.NArg() < 1 || *invPath == "" {
		fmt.Fprintln(os.Stderr, "usage: shellf run --inventory <hosts.shellf> [--vars <f>] [--set k=v] [--check] [--insecure] <plan.shellf>")
		os.Exit(2)
	}

	planSrc, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	invSrc, err := os.ReadFile(*invPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	baseVars, setVars, err := loadGlobals(*varsPath, sets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// ParsePlanWithVars enriches baseVars in place with the plan's top-level
	// bindings, so the same table drives per-host resolution below.
	plan, err := lang.ParsePlanWithVars(string(planSrc), baseVars, setVars, stdSignatures())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", fs.Arg(0), err)
		os.Exit(1)
	}
	inv, err := lang.ParseInventory(string(invSrc))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", *invPath, err)
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

	printReports(orchestrator.Run(plan, inv, self, mode, dial, baseVars, setVars))
}

// stdSignatures resolves an instruction's parameter names from the embedded
// stdlib (signatures live with the defs, self-hosting) plus the Go builtins —
// so adding a def needs no parser-side edit (#107).
func stdSignatures() lang.InstructionSig {
	builtins := map[string][]string{"file-copy": {"src", "dst"}}
	return func(name string) ([]string, bool) {
		if p, ok := builtins[name]; ok {
			return p, true
		}
		if def, ok := std.Lookup(name); ok {
			names := make([]string, len(def.Params))
			for i, p := range def.Params {
				names[i] = p.Name
			}
			return names, true
		}
		return nil, false
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

// cleanCmd: shellf clean --inventory <hosts.shellf> [target...]. Kills resident
// agents and removes shellf's /tmp files on each target (all hosts if no target).
func cleanCmd(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	invPath := fs.String("inventory", "", "inventory file (required)")
	insecure := fs.Bool("insecure", false, "skip host-key verification (dev only)")
	knownHosts := fs.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	fs.Parse(args)
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

func printReports(reports []orchestrator.BlockReport) {
	anyErr := false
	for _, b := range reports {
		fmt.Printf("on %s:\n", b.Target)
		for _, h := range b.Hosts {
			if h.Err != nil {
				var re *orchestrator.ResolveError
				if errors.As(h.Err, &re) {
					fmt.Printf("  %s: %v\n", h.Host, h.Err) // resolution error, not unreachable
				} else {
					fmt.Printf("  %s: unreachable (%v)\n", h.Host, h.Err)
				}
				anyErr = true
				continue
			}
			fmt.Printf("  %s:\n", h.Host)
			for _, s := range h.Response.Results {
				printStep(s, "    ")
				anyErr = anyErr || s.Category == "err"
			}
			if h.Response.Halted {
				fmt.Printf("    (halted)\n")
			}
		}
	}
	exitFor(anyErr)
}

func printStep(s proto.StepResult, indent string) {
	label := s.Category
	if s.Tag != "" {
		label += "." + s.Tag
	}
	fmt.Printf("%s%-24s %s\n", indent, s.Label, label)
	// Show the preview/error payload (e.g. a file-copy diff in check mode).
	if s.Shell != nil && s.Shell.Stdout != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Shell.Stdout, "\n"), "\n") {
			fmt.Printf("%s    | %s\n", indent, line)
		}
	}
	for _, sub := range s.Sub {
		printStep(sub, indent+"  ")
	}
}

func exitFor(isErr bool) {
	if isErr {
		os.Exit(1)
	}
}
