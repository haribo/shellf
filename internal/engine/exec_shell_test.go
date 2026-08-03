package engine

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestShellExecutor_Escalate(t *testing.T) {
	// No become → no prefix (runs as the current user).
	if p := (ShellExecutor{}).escalate(Env{"pkg": "nginx"}); len(p) != 0 {
		t.Fatalf("no become → no prefix, got %v", p)
	}

	// Become root via sudo, preserving the injected vars (sorted, injection-safe).
	got := strings.Join(ShellExecutor{Become: "root"}.escalate(Env{"pkg": "nginx", "a": "1"}), " ")
	if got != "sudo -n --preserve-env=a,pkg" {
		t.Fatalf("sudo prefix: %q", got)
	}

	// A non-root user adds -u.
	got = strings.Join(ShellExecutor{Become: "deploy"}.escalate(Env{}), " ")
	if got != "sudo -n -u deploy" {
		t.Fatalf("sudo -u: %q", got)
	}

	// doas method.
	got = strings.Join(ShellExecutor{Become: "root", Method: "doas"}.escalate(Env{}), " ")
	if got != "doas" {
		t.Fatalf("doas: %q", got)
	}
}

func TestShellExecutor_As(t *testing.T) {
	if s := (ShellExecutor{}).As(""); s.(ShellExecutor).Become != "" {
		t.Fatal("As(\"\") must not escalate")
	}
	if s := (ShellExecutor{}).As("root"); s.(ShellExecutor).Become != "root" {
		t.Fatal("As(\"root\") must set Become")
	}
}

func TestShellExecutor_Interp(t *testing.T) {
	// prelude per interpreter (ADR-0012)
	if p := (ShellExecutor{}).prelude(); p != "set -e\n" {
		t.Fatalf("default prelude: %q", p)
	}
	if p := (ShellExecutor{Interp: "bash"}).prelude(); p != "set -e\nset -o pipefail\n" {
		t.Fatalf("bash prelude: %q", p)
	}
	for _, i := range []string{"nu", "raw"} {
		if p := (ShellExecutor{Interp: i}).prelude(); p != "" {
			t.Fatalf("%s prelude should be empty: %q", i, p)
		}
	}
	// binary
	if b, _ := (ShellExecutor{Interp: "bash"}).shellBin(); b != "/bin/bash" {
		t.Fatalf("bash bin: %s", b)
	}
	if b, _ := (ShellExecutor{}).shellBin(); b != "/bin/sh" {
		t.Fatalf("default bin: %s", b)
	}
	if b, _ := (ShellExecutor{Interp: "nu"}).shellBin(); b != "nu" {
		t.Fatalf("nu bin: %s", b)
	}
	if b, _ := (ShellExecutor{Interp: "dash"}).shellBin(); b != "/bin/dash" {
		t.Fatalf("dash bin: %s", b)
	}
	// Using sets/keeps the interpreter
	if e := (ShellExecutor{}).Using("bash"); e.(ShellExecutor).Interp != "bash" {
		t.Fatal("Using should set Interp")
	}
	if e := (ShellExecutor{Interp: "bash"}).Using(""); e.(ShellExecutor).Interp != "bash" {
		t.Fatal(`Using("") must not clear Interp`)
	}
}

// The following exercise the REAL /bin/sh executor (not a mock): the production
// path that every remote step ultimately runs through.

func TestShell_Real_ExitAndStreams(t *testing.T) {
	r := ShellExecutor{}.Shell(`printf out; printf err >&2; exit 3`, nil)
	if r.Exit != 3 {
		t.Fatalf("exit code must be captured: %d", r.Exit)
	}
	if r.Stdout != "out" || r.Stderr != "err" {
		t.Fatalf("stdout/stderr must be separated: out=%q err=%q", r.Stdout, r.Stderr)
	}
	if r.OK() {
		t.Fatal("a non-zero exit is not OK()")
	}
}

func TestShell_Real_EnvInjectionIsInert(t *testing.T) {
	// A var whose value looks like shell must be data, never executed: `echo "$x"`
	// prints the literal, and the injected `touch` never runs.
	marker := t.TempDir() + "/pwned"
	r := ShellExecutor{}.Shell(`printf '%s' "$x"`, Env{"x": "; touch " + marker})
	if r.Exit != 0 {
		t.Fatalf("unexpected failure: %+v", r)
	}
	if r.Stdout != "; touch "+marker {
		t.Fatalf("the value must be passed as data, got %q", r.Stdout)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("env injection executed — the marker file was created")
	}
}

func TestShell_Real_SetE_HaltsOnFirstFailure(t *testing.T) {
	// Default prelude injects `set -e`: a failing command aborts the rest.
	r := ShellExecutor{}.Shell("false\nprintf after", nil)
	if r.Exit == 0 || r.Stdout == "after" {
		t.Fatalf("set -e should abort before `after`: %+v", r)
	}
}

func TestShell_Real_RawSkipsSetE(t *testing.T) {
	// raw means "no net": no `set -e`, so a failure does not abort the sequence.
	r := ShellExecutor{Interp: "raw"}.Shell("false\nprintf after", nil)
	if r.Stdout != "after" {
		t.Fatalf("raw mode should continue past a failure: %+v", r)
	}
}

func TestShell_Real_MissingBinary(t *testing.T) {
	// An absent interpreter binary cannot launch → Exit -1 (distinct from a
	// non-zero script exit).
	r := ShellExecutor{Interp: "nu"}.Shell("whatever", nil)
	if _, err := exec.LookPath("nu"); err != nil && r.Exit != -1 {
		t.Fatalf("a missing interpreter must yield exit -1, got %d", r.Exit)
	}
}
