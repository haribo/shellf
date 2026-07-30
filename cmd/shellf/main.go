package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"shellf/internal/agent"
	"shellf/internal/engine"
	"shellf/internal/inventory"
	"shellf/internal/lang"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
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

	// Run a plan file against an inventory file.
	if len(os.Args) > 1 && os.Args[1] == "run" {
		runCmd(os.Args[2:])
		return
	}

	targets := flag.String("targets", "", "comma-separated user@host list (empty = run locally)")
	port := flag.String("port", "", "ssh port (shared by all targets)")
	key := flag.String("key", "", "ssh identity file")
	knownHosts := flag.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	insecure := flag.Bool("insecure", false, "skip host-key verification (dev only)")
	par := flag.String("par", "", "extra packages to install in a parallel block")
	cp := flag.String("copy", "", "append a file-copy step, as src:dst")
	check := flag.Bool("check", false, "dry-run: decide without mutating")
	flag.Parse()

	// Build the step sequence: positional pkgs sequential, --par as one parallel block.
	steps := buildSteps(flag.Args(), *par, *cp)
	if len(steps) == 0 {
		fmt.Fprintln(os.Stderr, "usage: shellf [--targets u@h1,u@h2] [--par p1,p2] [--check] <pkg>...")
		os.Exit(2)
	}

	modeStr := "apply"
	if *check {
		modeStr = "check"
	}

	// Local path: run the sequence here via the same agent code.
	if *targets == "" {
		req, _ := json.Marshal(proto.Request{Mode: modeStr, Steps: steps})
		var out bytes.Buffer
		if err := agent.Serve(bytes.NewReader(req), &out, engine.ShellExecutor{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var resp proto.Response
		json.Unmarshal(out.Bytes(), &resp)
		anyErr := false
		for _, s := range resp.Results {
			printStep(s, "  ")
			anyErr = anyErr || s.Category == "err"
		}
		exitFor(anyErr)
		return
	}

	// Fleet path: build a one-group inventory from --targets, run one `on` block.
	inv := inventoryFrom(splitList(*targets), *port, *key)
	dial := func(alias string) transport.Transport {
		h, _ := inv.Resolve(alias)
		return transport.SSH{
			User: h.User, Host: h.Address, Port: h.Port, Key: h.Key,
			KnownHosts: *knownHosts, Insecure: *insecure,
		}
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	plan := orchestrator.Plan{{Target: "targets", Steps: steps}}
	reports := orchestrator.Run(plan, inv, self, modeStr, dial, nil, nil)
	printReports(reports)
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
	plan, err := lang.ParsePlanWithVars(string(planSrc), baseVars, setVars)
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
			KnownHosts: *knownHosts, Insecure: *insecure,
		}
	}

	printReports(orchestrator.Run(plan, inv, self, mode, dial, baseVars, setVars))
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

func buildSteps(pkgs []string, par, copy string) []proto.Step {
	var steps []proto.Step
	for _, p := range pkgs {
		steps = append(steps, aptStep(p))
	}
	if branches := splitList(par); len(branches) > 0 {
		var block []proto.Step
		for _, p := range branches {
			block = append(block, aptStep(p))
		}
		steps = append(steps, proto.Step{Parallel: block})
	}
	if copy != "" {
		src, dst, _ := strings.Cut(copy, ":")
		steps = append(steps, proto.Step{
			Instruction: "file-copy",
			Args:        map[string]string{"src": src, "dst": dst},
		})
	}
	return steps
}

func aptStep(pkg string) proto.Step {
	return proto.Step{Instruction: "apt-install", Args: map[string]string{"pkg": pkg}}
}

func inventoryFrom(list []string, port, key string) inventory.Inventory {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{},
		Groups: map[string][]string{},
	}
	for _, t := range list {
		user, addr, _ := strings.Cut(t, "@")
		inv.Hosts[t] = inventory.Host{Address: addr, User: user, Port: port, Key: key}
		inv.Groups["targets"] = append(inv.Groups["targets"], t)
	}
	return inv
}

func printReports(reports []orchestrator.BlockReport) {
	anyErr := false
	for _, b := range reports {
		fmt.Printf("on %s:\n", b.Target)
		for _, h := range b.Hosts {
			if h.Err != nil {
				fmt.Printf("  %s: unreachable (%v)\n", h.Host, h.Err)
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

func splitList(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func exitFor(isErr bool) {
	if isErr {
		os.Exit(1)
	}
}
