package agent

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"shellf/internal/engine"
)

const (
	guardScript = `dpkg -s "$pkg"`
	applyScript = `apt-get install -y "$pkg"`
)

// fakeExec is a concurrency-safe lookup table keyed by (script, pkg).
type fakeExec struct {
	mu        sync.Mutex
	responses map[string]engine.ShellResult
	calls     map[string]bool
}

func newFake() *fakeExec {
	return &fakeExec{responses: map[string]engine.ShellResult{}, calls: map[string]bool{}}
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

func (f *fakeExec) set(script, pkg string, exit int) {
	f.responses[key(script, pkg)] = engine.ShellResult{Exit: exit}
}

func (f *fakeExec) called(script, pkg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key(script, pkg)]
}

func serve(t *testing.T, f *fakeExec, req Request) Response {
	t.Helper()
	body, _ := json.Marshal(req)
	var out bytes.Buffer
	if err := Serve(bytes.NewReader(body), &out, f); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestServe_Sequential_HaltsOnErr(t *testing.T) {
	f := newFake()
	f.set(guardScript, "a", 1)   // a not installed
	f.set(applyScript, "a", 100) // a install fails

	resp := serve(t, f, Request{Mode: "apply", Steps: []Step{
		{Instruction: "apt-install", Pkg: "a"},
		{Instruction: "apt-install", Pkg: "b"},
	}})

	if len(resp.Results) != 1 || resp.Results[0].Category != "err" {
		t.Fatalf("want single err result, got %+v", resp.Results)
	}
	if !resp.Halted {
		t.Fatal("want Halted after err")
	}
	if f.called(guardScript, "b") {
		t.Fatal("step b ran after step a errored")
	}
}

func TestServe_Parallel_AggregatesAndRunsBoth(t *testing.T) {
	f := newFake()
	f.set(guardScript, "a", 1)
	f.set(applyScript, "a", 0) // a installs ok
	f.set(guardScript, "b", 1)
	f.set(applyScript, "b", 100) // b fails

	resp := serve(t, f, Request{Mode: "apply", Steps: []Step{
		{Parallel: []Step{
			{Instruction: "apt-install", Pkg: "a"},
			{Instruction: "apt-install", Pkg: "b"},
		}},
	}})

	if len(resp.Results) != 1 || resp.Results[0].Category != "err" {
		t.Fatalf("parallel aggregate should be err, got %+v", resp.Results)
	}
	if len(resp.Results[0].Sub) != 2 {
		t.Fatalf("want 2 branch results, got %d", len(resp.Results[0].Sub))
	}
	if !f.called(guardScript, "a") || !f.called(guardScript, "b") {
		t.Fatal("both branches must run to completion (no short-circuit)")
	}
}

func TestServe_Check_NoMutation(t *testing.T) {
	f := newFake()
	f.set(guardScript, "nginx", 1) // not installed

	resp := serve(t, f, Request{Mode: "check", Steps: []Step{
		{Instruction: "apt-install", Pkg: "nginx"},
	}})

	if resp.Results[0].Category != "would" || resp.Results[0].Tag != "installed" {
		t.Fatalf("want would.installed, got %+v", resp.Results[0])
	}
	if f.called(applyScript, "nginx") {
		t.Fatal("check mode mutated")
	}
}
