package agent

import (
	"os"
	"path/filepath"
	"shellf/internal/engine"
	"strings"
	"testing"
)

// ADR-0044 §4. The control host checks the agent binary before launching it
// (internal/transport/ssh.go, #391) — checks written when the agent ran unprivileged.
// About to hand that path to sudo, the same weakness stops being a foothold and becomes
// the machine, so it is checked again against the state on disk now.
func TestOwnedAndUnwritable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "shellf")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ownedAndUnwritable(bin); err != nil {
		t.Fatalf("a binary we own, that nobody else can write, must pass: %v", err)
	}

	// Group- or world-writable: someone else chooses what root executes.
	for _, mode := range []os.FileMode{0o775, 0o777, 0o757} {
		if err := os.Chmod(bin, mode); err != nil {
			t.Fatal(err)
		}
		err := ownedAndUnwritable(bin)
		if err == nil {
			t.Fatalf("mode %v must be refused before escalating", mode)
		}
		if !strings.Contains(err.Error(), "writable by another user") {
			t.Fatalf("mode %v: the refusal must say why: %v", mode, err)
		}
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	// A writable directory on the way to it: replacing the file is not the only way to
	// change what a path resolves to.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	err := ownedAndUnwritable(bin)
	if err == nil {
		t.Fatal("a directory another user can write must be refused: the binary can be swapped")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("the refusal must name the offending path: %v", err)
	}

	// The same directory with the sticky bit is /tmp, which is how every target's temp
	// directory looks — refusing it would refuse every ordinary agent.
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := ownedAndUnwritable(bin); err != nil {
		t.Fatalf("a sticky world-writable directory is /tmp, not a hazard: %v", err)
	}
}

// A refusal must stop the transfer, and it must stop it *before* running anything: falling
// back to an unescalated write is the wrong-owner success #390 was opened on.
func TestChildVerb_RefusesBeforeRunningAnything(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil { // no sticky bit: anyone can swap what is here
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "shellf")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ran := false
	ex := &spyExec{onShell: func() { ran = true }}
	_, err := childVerbAt(bin, ex, "__sync-scan", "/tmp/x", "meta")
	if err == nil {
		t.Fatal("an agent binary another user can swap must not be escalated")
	}
	if !strings.Contains(err.Error(), "refusing to escalate") {
		t.Fatalf("the refusal must say what it refused to do: %v", err)
	}
	if ran {
		t.Fatal("the refusal must come before anything is executed")
	}
}

type spyExec struct{ onShell func() }

func (s *spyExec) Shell(string, engine.Env) engine.ShellResult {
	s.onShell()
	return engine.ShellResult{}
}
func (s *spyExec) As(string) engine.Executor    { return s }
func (s *spyExec) Using(string) engine.Executor { return s }
