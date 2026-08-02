package agent

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/proto"
)

// These tests exercise the agent end to end, including that `apt.install` now
// routes through the embedded stdlib def (std/apt.shellf), not a Go builtin.

const (
	dpkgScript = `dpkg -s "$pkg" >/dev/null 2>&1` // apt.install's observe shell (ADR-0013)
	aptScript  = `apt-get install -y "$pkg"`
)

type fakeExec struct {
	mu        sync.Mutex
	responses map[string]engine.ShellResult
	calls     map[string]bool
	becomes   map[string]string // script-key → the user a shell escalated to (ADR-0011)
}

func newFake() *fakeExec {
	return &fakeExec{responses: map[string]engine.ShellResult{}, calls: map[string]bool{}, becomes: map[string]string{}}
}

func key(script, pkg string) string { return script + "\x00" + pkg }

func (f *fakeExec) Shell(script string, env engine.Env) engine.ShellResult {
	k := key(script, env["pkg"])
	f.mu.Lock()
	f.calls[k] = true
	f.mu.Unlock()
	if r, ok := f.responses[k]; ok {
		return r
	}
	return engine.ShellResult{Exit: 127}
}

func (f *fakeExec) As(user string) engine.Executor {
	if user == "" {
		return f
	}
	return becomeExec{f, user}
}

func (f *fakeExec) Using(string) engine.Executor { return f }

// becomeExec records the escalation user for each shell it runs, then delegates.
type becomeExec struct {
	inner  *fakeExec
	become string
}

func (b becomeExec) Shell(script string, env engine.Env) engine.ShellResult {
	b.inner.mu.Lock()
	b.inner.becomes[key(script, env["pkg"])] = b.become
	b.inner.mu.Unlock()
	return b.inner.Shell(script, env)
}

func (b becomeExec) As(user string) engine.Executor {
	if user == "" {
		return b
	}
	return becomeExec{b.inner, user}
}

func (b becomeExec) Using(string) engine.Executor { return b }

func (f *fakeExec) becameAs(script, pkg string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.becomes[key(script, pkg)]
}

func (f *fakeExec) set(script, pkg string, exit int) {
	f.responses[key(script, pkg)] = engine.ShellResult{Exit: exit}
}

func (f *fakeExec) called(script, pkg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key(script, pkg)]
}

func apt(pkg string) proto.Step {
	return proto.Step{Instruction: "apt.install", Args: map[string]string{"pkg": pkg}}
}

func serve(t *testing.T, f *fakeExec, req proto.Request) proto.Response {
	t.Helper()
	body, _ := json.Marshal(req)
	var out bytes.Buffer
	if err := Serve(bytes.NewReader(body), &out, f); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAgentBecome_BlockEscalates(t *testing.T) {
	// `as root { apt.install(nginx) }` → the def's shells run escalated to root.
	f := newFake()
	f.set(dpkgScript, "nginx", 0) // observe: installed → skip apply
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		{Become: "root", Block: []proto.Step{apt("nginx")}},
	}})
	if got := f.becameAs(dpkgScript, "nginx"); got != "root" {
		t.Fatalf("shell in `as root` block should escalate to root, got %q", got)
	}
}

func TestMissingInterpreters(t *testing.T) {
	f := newFake()
	f.set("command -v bash", "", 0) // present
	f.set("command -v nu", "", 1)   // absent
	steps := []proto.Step{
		{Instruction: "shell", Args: map[string]string{"cmd": "x"}, Interp: "bash"},
		{Instruction: "shell", Args: map[string]string{"cmd": "y"}, Interp: "nu"},
		{Instruction: "shell", Args: map[string]string{"cmd": "z"}}, // sh → never checked
	}
	if got := missingInterpreters(steps, f); len(got) != 1 || got[0] != "nu" {
		t.Fatalf("expected [nu], got %v", got)
	}
}

func TestServe_InterpreterPreflight(t *testing.T) {
	f := newFake()
	f.set("command -v nu", "", 1) // absent
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		{Instruction: "shell", Args: map[string]string{"cmd": "ls"}, Interp: "nu"},
	}})
	if !resp.Halted || len(resp.Results) != 1 || resp.Results[0].Tag != "interpreterMissing" {
		t.Fatalf("a missing interpreter must pre-flight halt: %+v", resp)
	}
	if f.called("ls", "") {
		t.Fatal("pre-flight must halt before running any step")
	}
}

func TestAgentBecome_DefIntrinsic(t *testing.T) {
	// apt.install is marked `as root` → its shells escalate with no wrapper.
	f := newFake()
	f.set(dpkgScript, "nginx", 0)
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{apt("nginx")}})
	if got := f.becameAs(dpkgScript, "nginx"); got != "root" {
		t.Fatalf("intrinsic apt.install should escalate to root, got %q", got)
	}
}

func TestAgentBecome_ShellStepEscalates(t *testing.T) {
	// `shell as root { id }` → that shell escalates.
	f := newFake()
	f.set("id", "", 0)
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		{Instruction: "shell", Args: map[string]string{"cmd": "id"}, Become: "root"},
	}})
	if got := f.becameAs("id", ""); got != "root" {
		t.Fatalf("`shell as root` should escalate, got %q", got)
	}
}

func TestServe_Sequential_HaltsOnErr(t *testing.T) {
	f := newFake()
	f.set(dpkgScript, "a", 1) // a not installed
	f.set(aptScript, "a", 100) // a install fails → err.runtime

	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{apt("a"), apt("b")}})

	if len(resp.Results) != 1 || resp.Results[0].Category != "err" {
		t.Fatalf("want single err result, got %+v", resp.Results)
	}
	if !resp.Halted {
		t.Fatal("want Halted after err")
	}
	if f.called(dpkgScript, "b") {
		t.Fatal("step b ran after step a errored")
	}
}

func TestServe_Parallel_AggregatesAndRunsBoth(t *testing.T) {
	f := newFake()
	f.set(dpkgScript, "a", 1)
	f.set(aptScript, "a", 0) // a installs ok
	f.set(dpkgScript, "b", 1)
	f.set(aptScript, "b", 100) // b fails

	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		{Parallel: []proto.Step{apt("a"), apt("b")}},
	}})

	if len(resp.Results) != 1 || resp.Results[0].Category != "err" {
		t.Fatalf("parallel aggregate should be err, got %+v", resp.Results)
	}
	if len(resp.Results[0].Sub) != 2 {
		t.Fatalf("want 2 branch results, got %d", len(resp.Results[0].Sub))
	}
	if !f.called(dpkgScript, "a") || !f.called(dpkgScript, "b") {
		t.Fatal("both branches must run to completion")
	}
}

func TestServe_Check_NoMutation(t *testing.T) {
	f := newFake()
	f.set(dpkgScript, "nginx", 1) // not installed

	resp := serve(t, f, proto.Request{Mode: "check", Steps: []proto.Step{apt("nginx")}})

	if resp.Results[0].Category != "would" || resp.Results[0].Tag != "installed" {
		t.Fatalf("want would.installed, got %+v", resp.Results[0])
	}
	if f.called(aptScript, "nginx") {
		t.Fatal("check mode ran apt-get")
	}
}
