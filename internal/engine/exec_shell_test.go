package engine

import (
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
	// Using sets/keeps the interpreter
	if e := (ShellExecutor{}).Using("bash"); e.(ShellExecutor).Interp != "bash" {
		t.Fatal("Using should set Interp")
	}
	if e := (ShellExecutor{Interp: "bash"}).Using(""); e.(ShellExecutor).Interp != "bash" {
		t.Fatal(`Using("") must not clear Interp`)
	}
}
