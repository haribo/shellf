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
