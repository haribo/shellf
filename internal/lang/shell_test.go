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
	plan, err := parsePlan(src, map[string]string{}, nil, defaultSig, nil, nil)
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
	_, err := parsePlan("on web { shell { echo hi } unless { true } }", map[string]string{}, nil, defaultSig, nil, nil)
	if err == nil {
		t.Fatal("expected an error: unless is removed from plans")
	}
	if !strings.Contains(err.Error(), "unless") {
		t.Fatalf("error should mention unless: %v", err)
	}
}

func TestParseShell_UnterminatedBlock(t *testing.T) {
	if _, err := parsePlan("on web {\n shell {\n echo hi\n", map[string]string{}, nil, defaultSig, nil, nil); err == nil {
		t.Fatal("expected error for unterminated shell block")
	}
}

// #415: `unless { … }` inside a def parsed and was **silently ignored**. Measured: a def
// doing `shell { touch "$dst" } unless { true }` created the file — the guard held, the
// command ran anyway. The clause was stored in `ShellExpr.Unless` and nothing ever read
// it: `engine.Shell.Unless` is only ever filled from a plan step's argument, and plans
// refuse the keyword outright.
//
// So it lived in exactly one place, where it did nothing. Refused now, with the message
// plans already give.
func TestShell_UnlessInADefIsRefused(t *testing.T) {
	srcs := map[string]string{
		"in an apply":    `def t(p: str) { apply { shell { touch "$p" } unless { true } return ok.done } }`,
		"in an observe":  `def t(p: str) { observe { return state(v: shell { test -f "$p" } unless { true }) } }`,
		"with interp":    `def t(p: str) { apply { shell(bash) { touch "$p" } unless { true } return ok.done } }`,
		"as a condition": `def t(p: str) { apply { if shell { test -f "$p" } unless { true } { shell { echo hi } } return ok.done } }`,
	}
	for what, src := range srcs {
		t.Run(what, func(t *testing.T) {
			_, err := ParseDefs(src)
			if err == nil {
				t.Fatal("`unless` in a def must be refused, not parsed and ignored")
			}
			// The same message plans give, so one construct has one answer.
			if !strings.Contains(err.Error(), "if !shell") {
				t.Fatalf("the refusal must name the replacement: %v", err)
			}
		})
	}

	// The shape it replaces keeps working.
	if _, err := ParseDefs(`def t(p: str) { apply { if !shell { test -f "$p" } { shell { touch "$p" } } return ok.done } }`); err != nil {
		t.Fatalf("the replacement form must parse: %v", err)
	}
}
