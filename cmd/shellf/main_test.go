package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/orchestrator"
	"shellf/internal/project"
)

// The end-to-end wiring of `runCmd`/`cleanCmd` (flag parsing → transport → exit
// code) is covered by the real-SSH harness in test/e2e/. These unit tests lock
// the risk-bearing pure logic: variable precedence and signature resolution.

func TestLoadGlobals_SetOnly(t *testing.T) {
	base, set, err := loadGlobals("", kvFlags{"pkg=nginx", "env=prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 0 {
		t.Fatalf("no --vars file → empty base, got %v", base)
	}
	if set["pkg"] != "nginx" || set["env"] != "prod" {
		t.Fatalf("--set not collected: %v", set)
	}
}

func TestLoadGlobals_VarsFileThenSet(t *testing.T) {
	dir := t.TempDir()
	vf := filepath.Join(dir, "vars.shellf")
	if err := os.WriteFile(vf, []byte("pkg = \"apache\"\nregion = \"eu\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, set, err := loadGlobals(vf, kvFlags{"pkg=nginx"})
	if err != nil {
		t.Fatal(err)
	}
	// The file feeds baseVars; --set stays separate (higher precedence, layered
	// later by the orchestrator). loadGlobals must not merge them.
	if base["pkg"] != "apache" || base["region"] != "eu" {
		t.Fatalf("vars file not parsed into base: %v", base)
	}
	if set["pkg"] != "nginx" {
		t.Fatalf("--set override not kept distinct: %v", set)
	}
}

func TestLoadGlobals_MalformedSet(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue"} {
		if _, _, err := loadGlobals("", kvFlags{bad}); err == nil {
			t.Fatalf("--set %q must error", bad)
		}
	}
}

func TestLoadGlobals_MissingVarsFile(t *testing.T) {
	if _, _, err := loadGlobals("/does/not/exist.shellf", nil); err == nil {
		t.Fatal("a missing --vars file must error")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestLoadInventory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "inv.shellf", `host web = { address: "10.0.0.1", user: "deploy" }`)
	inv, err := loadInventory(filepath.Join(dir, "inv.shellf"))
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := inv.Resolve("web"); !ok || h.Address != "10.0.0.1" {
		t.Fatalf("inventory not parsed: %+v ok=%v", h, ok)
	}
	if _, err := loadInventory(filepath.Join(dir, "missing.shellf")); err == nil {
		t.Fatal("a missing inventory must error")
	}
}

// writeDef writes a def into its package directory, creating it: defs/<pkg>/<name>.
// projectDir lays out an empty shellf project (ADR-0038) under root and returns it, so a
// test states the layout once instead of repeating four MkdirAll calls.
func projectDir(t *testing.T, root string) string {
	t.Helper()
	for _, d := range []string{"plans", "defs", "assets", "inventories"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeDef(t *testing.T, root, pkg, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "defs", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, name, content)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSecrets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "s.txt", "S3cr3t!\n") // trailing newline is trimmed
	t.Setenv("MY_SECRET", "envval")

	secrets, values, err := loadSecrets(
		kvFlags{"pass=" + filepath.Join(dir, "s.txt")},
		kvFlags{"tok=MY_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if secrets["pass"] != "S3cr3t!" || secrets["tok"] != "envval" {
		t.Fatalf("secrets: %v", secrets)
	}
	if len(values) != 2 {
		t.Fatalf("values: %v", values)
	}
	// Malformed and missing-file both error.
	if _, _, err := loadSecrets(kvFlags{"noequals"}, nil); err == nil {
		t.Fatal("malformed --secret-file must error")
	}
	if _, _, err := loadSecrets(kvFlags{"x=/does/not/exist"}, nil); err == nil {
		t.Fatal("a missing secret file must error")
	}
}

func TestKVFlags(t *testing.T) {
	var k kvFlags
	if err := k.Set("a=1"); err != nil {
		t.Fatal(err)
	}
	_ = k.Set("b=2")
	if k.String() != "a=1,b=2" {
		t.Fatalf("String(): %q", k.String())
	}
}

func TestVersionLine(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "v9.9.9"
	if got := versionLine(); got != "shellf v9.9.9" {
		t.Fatalf("versionLine: %q", got)
	}
}

// TestParseDefsFor stood here: the CLI re-parsed the shipped def sources to extract
// their `%` occurrences for the allow-list. A def may no longer name a control-host file
// (#403, ADR-0043), so the plan's steps are the only source and the helper went with the
// question — see TestControl_DefMayNotDeclareAPath in internal/lang.

// ADR-0035 renames the flag. The old spelling must fail naming the new one rather than
// be accepted, or two names live forever — the same rule as the renamed instructions.
func TestDryRunFlag_OldNameIsRefused(t *testing.T) {
	bin := buildShellf(t)
	out, err := exec.Command(bin, "run", "--inventory", "x", "--check", "p.shellf").CombinedOutput()
	if err == nil {
		t.Fatal("--check must fail")
	}
	if !strings.Contains(string(out), "--dry-run") {
		t.Fatalf("the error must name the replacement: %s", out)
	}
}

// buildShellf compiles the CLI once for flag-level tests.
func buildShellf(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "shellf")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Skipf("cannot build: %v: %s", err, out)
	}
	return bin
}

func TestRemovedFlag(t *testing.T) {
	if got := removedFlag(false); got != "" {
		t.Fatalf("no removed flag passed: %q", got)
	}
	got := removedFlag(true)
	if !strings.Contains(got, "--dry-run") || !strings.Contains(got, "--check") {
		t.Fatalf("must name both the old and the new spelling: %q", got)
	}
}

// TestUsesRender stood here, guarding the case where a plan rendered without declaring a
// single `%"…"` path: the content came from the target, so nothing else opened the
// channel. A render now names a declared template (#392, ADR-0042), so a non-empty
// allow-list is the only condition left and `usesRender` went with the case.

// TestMergeVars stood here. It asserted "the host wins over globals" against a mergeVars
// helper that no longer exists: #540 took the inventory out of a bare reference's sources
// (ADR-0053), and the control-host render scope now uses orchestrator.HostEnv like every
// other call path. Its comment cited ADR-0022 for the precedence chain, which was wrong —
// that chain is ADR-0003 §3; ADR-0022 is the `with { }` per-call override.

// #345: a plan that asks the control host for nothing opens no bridge. The channel
// factory returns nil for every host, which is what keeps a detached job detached
// (ADR-0031 §2) — a bridge opened for nothing would make every plan depend on the
// control host staying reachable.
func TestControlChannel_NilWhenThePlanAsksNothing(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { dir.ensure("/x") }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	planPath := filepath.Join(dir, "plans", "plan.shellf")
	invPath := filepath.Join(dir, "inventories", "inv.shellf")

	plan, _, _, err := project.Load(planPath, invPath, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := loadInventory(invPath)
	if err != nil {
		t.Fatal(err)
	}
	channelFor := controlChannel(planPath, plan, inv, map[string]string{}, map[string]string{})
	if channelFor("web") != nil {
		t.Fatal("a plan that asks for nothing must open no bridge")
	}
}

// And the other half: a plan that marks a control-host path gets a channel.
func TestControlChannel_ServesADeclaredPath(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "assets"), "motd.tmpl", "hello @{who}\n")
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf",
		`on web { file.template(%"motd.tmpl", "/etc/motd") }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	planPath := filepath.Join(dir, "plans", "plan.shellf")
	invPath := filepath.Join(dir, "inventories", "inv.shellf")

	plan, _, _, err := project.Load(planPath, invPath, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := loadInventory(invPath)
	if err != nil {
		t.Fatal(err)
	}
	channelFor := controlChannel(planPath, plan, inv, map[string]string{}, map[string]string{})
	if channelFor("web") == nil {
		t.Fatal("a plan declaring a control-host path must get a bridge")
	}
}

// `--parallel 0` is the operator typing something that cannot mean anything — it is
// refused, never read as "unlimited" (#462). An *absent* flag is a different thing and
// takes the default, which is why the check asks the flag set what was provided rather
// than comparing to zero.
func TestCheckParallel(t *testing.T) {
	parsed := func(args ...string) (*flag.FlagSet, int) {
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		n := fs.Int("parallel", 0, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return fs, *n
	}
	// Absent: takes the default, so nothing is refused here.
	fs, n := parsed()
	checkParallel(fs, n) // must not exit
	fs, n = parsed("--parallel", "4")
	checkParallel(fs, n) // must not exit

	// Provided and impossible: the process must exit 2. Asserted in a subprocess, since
	// checkParallel exits rather than returning an error.
	if os.Getenv("SHELLF_TEST_BAD_PARALLEL") != "" {
		fs, n := parsed("--parallel", os.Getenv("SHELLF_TEST_BAD_PARALLEL"))
		checkParallel(fs, n)
		return
	}
	for _, bad := range []string{"0", "-1"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestCheckParallel")
		cmd.Env = append(os.Environ(), "SHELLF_TEST_BAD_PARALLEL="+bad)
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Fatalf("--parallel %s must exit 2, got %v", bad, err)
		}
	}
}

// anyBlockError is what makes `status` exit non-zero on an unknown target (#451). It had
// no test of its own — the behaviour was only covered end to end, where a change to it
// would surface as a puzzling exit code rather than a failing assertion.
// errFake is a minimal error for table cases. internal/report has its own since #491:
// a test helper does not cross a package boundary.
type errFake string

func (e errFake) Error() string { return string(e) }

func TestAnyBlockError(t *testing.T) {
	none := []orchestrator.BlockReport{
		{Target: "web", Hosts: []orchestrator.HostOutcome{{Host: "h1"}}},
		{Target: "db"},
	}
	if anyBlockError(none) {
		t.Fatal("no block failed as a whole")
	}
	// A per-host failure is not a block failure: the block ran, the host did not.
	perHost := []orchestrator.BlockReport{{
		Target: "web",
		Hosts:  []orchestrator.HostOutcome{{Host: "h1", Err: errFake("unreachable")}},
	}}
	if anyBlockError(perHost) {
		t.Fatal("a host error is not a block error")
	}
	blocked := []orchestrator.BlockReport{
		{Target: "web", Hosts: []orchestrator.HostOutcome{{Host: "h1"}}},
		{Target: "wbe", Err: &orchestrator.UnknownTargetError{Target: "wbe"}},
	}
	if !anyBlockError(blocked) {
		t.Fatal("a block that could not run must be reported")
	}
}

// `-v` must not undo what the report masks: the tracer is where redaction happens,
// because the CLI is what knows the run's secrets (#461).
func TestTracer_RedactsAndStaysOffStdout(t *testing.T) {
	if tracer(false, nil) != nil {
		t.Fatal("without -v there must be no tracer at all")
	}
	tr := tracer(true, []string{"sup3rs3cret"})
	if tr == nil {
		t.Fatal("with -v there must be one")
	}

	// Captured from stderr, which is also the assertion that it does not use stdout.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, stdout := os.Stderr, os.Stdout
	outR, outW, _ := os.Pipe()
	os.Stderr, os.Stdout = w, outW
	tr("pushing %s to %s", "sup3rs3cret", "/tmp/agent")
	os.Stderr, os.Stdout = stderr, stdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}

	var buf, outBuf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&outBuf, outR); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(buf.String(), "sup3rs3cret") {
		t.Fatalf("the secret leaked into the trace: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "***") {
		t.Fatalf("it must be masked, not dropped: %q", buf.String())
	}
	if outBuf.String() != "" {
		t.Fatalf("nothing may reach stdout: %q", outBuf.String())
	}
}
