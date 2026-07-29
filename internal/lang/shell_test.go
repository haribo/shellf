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
    mkdir -p /opt/app
    echo "done ${HOME}"
  }
  shell {
    docker network create web
  } unless {
    docker network inspect web
  }
}
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(steps))
	}
	if steps[0].Instruction != "shell" || steps[0].Args["cmd"] != "docker compose up -d" {
		t.Fatalf("one-line: %+v", steps[0])
	}
	// Block body captured verbatim, including the ${HOME} braces.
	if !strings.Contains(steps[1].Args["cmd"], "mkdir -p /opt/app") ||
		!strings.Contains(steps[1].Args["cmd"], "${HOME}") {
		t.Fatalf("block: %q", steps[1].Args["cmd"])
	}
	if steps[2].Args["cmd"] != "docker network create web" ||
		steps[2].Args["unless"] != "docker network inspect web" {
		t.Fatalf("unless: %+v", steps[2])
	}
}

func TestParseShell_UnterminatedBlock(t *testing.T) {
	if _, err := ParsePlan("on web {\n shell {\n echo hi\n"); err == nil {
		t.Fatal("expected error for unterminated shell block")
	}
}
