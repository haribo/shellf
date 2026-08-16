package lang

import (
	"strings"
	"testing"
)

func TestParseShell(t *testing.T) {
	src := `
on web {
  shell docker compose up -d
  shell {
    install -d /opt/app
    echo "done ${HOME}"
  }
}
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Instruction != "shell" || steps[0].Args["cmd"] != "docker compose up -d" {
		t.Fatalf("one-line: %+v", steps[0])
	}
	// Block body captured verbatim, including the ${HOME} braces.
	if !strings.Contains(steps[1].Args["cmd"], "install -d /opt/app") ||
		!strings.Contains(steps[1].Args["cmd"], "${HOME}") {
		t.Fatalf("block: %q", steps[1].Args["cmd"])
	}
}

func TestUnlessRemovedFromPlan(t *testing.T) {
	_, err := ParsePlan("on web { shell { echo hi } unless { true } }")
	if err == nil {
		t.Fatal("expected an error: unless is removed from plans")
	}
	if !strings.Contains(err.Error(), "unless") {
		t.Fatalf("error should mention unless: %v", err)
	}
}

func TestParseShell_UnterminatedBlock(t *testing.T) {
	if _, err := ParsePlan("on web {\n shell {\n echo hi\n"); err == nil {
		t.Fatal("expected error for unterminated shell block")
	}
}
