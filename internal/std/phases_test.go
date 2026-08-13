package std

import (
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// ADR-0035 §1 folds `pre-check` into `check`. The only behaviour that changes is under
// `status`, which ran `pre-check` and not `check` — so a def refusing its arguments must
// keep refusing them there, or `status` reports state for a call that could never run.
func TestCheckPhase_RefusesUnderStatus(t *testing.T) {
	// apt.install is the one instruction that used pre-check: it rejects an empty pkg.
	for _, mode := range []engine.Mode{engine.Apply, engine.Check, engine.Status} {
		res := eval(t, "apt.install", map[string]string{"pkg": ""}, &fakeExec{}, mode)
		if res.Category != engine.ERR {
			t.Fatalf("an empty pkg must be refused in every mode, %v gave %s", mode, res.String())
		}
	}
}

// And a valid argument is not refused, in any mode — the guard must not fire wrongly.
func TestCheckPhase_PassesAValidArgument(t *testing.T) {
	res := eval(t, "apt.install", map[string]string{"pkg": "nginx"},
		&fakeExec{observe: converged}, engine.Status)
	if res.Category == engine.ERR {
		t.Fatalf("a valid pkg must not be refused under status: %s", res.String())
	}
}

// The removed names must fail at parse, naming what to do — not "unknown phase".
func TestRemovedPhasesAreRefused(t *testing.T) {
	for name, want := range map[string]string{
		"pre-check": "check",
		"post":      "removed",
	} {
		_, err := parseOne(`def t() { ` + name + ` { return ok.x } }`)
		if err == nil {
			t.Fatalf("%s must be refused", name)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: the error must say what to do, got %v", name, err)
		}
	}
}

func parseOne(src string) (any, error) {
	return lang.ParseDefs(src)
}

// `preview` runs only in the dry-run mode, and `apply` only outside it — the two rows
// of the mode/phase table that are easy to get backwards.
func TestPhases_PreviewOnlyInCheckMode(t *testing.T) {
	// docker.compose-restart is action-shaped with a preview.
	args := map[string]string{"dir": "/opt/app", "service": "web"}

	f := &fakeExec{observe: engine.ShellResult{Stdout: "Container app-web-1 Restarting"}, apply: converged}
	res := eval(t, "docker.compose-restart", args, f, engine.Check)
	if res.Preview == "" {
		t.Fatal("preview must run in dry-run mode")
	}
	for _, s := range f.calls {
		if !strings.Contains(s, "--dry-run") {
			t.Fatalf("dry-run must not run the real action: %s", s)
		}
	}

	f2 := &fakeExec{apply: converged, applyMatch: "compose restart"}
	if r := eval(t, "docker.compose-restart", args, f2, engine.Apply); r.Preview != "" {
		t.Fatal("preview must not run outside dry-run mode")
	}
}
