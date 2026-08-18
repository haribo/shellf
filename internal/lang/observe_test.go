package lang

import (
	"testing"

	"shellf/internal/engine"
)

// A resource def in the ADR-0013 shape: an `observe` phase whose fields are
// diffed against the arguments (with `ensure` defaulting to "present"), driving
// whether `apply` runs.
const observeDef = `
def apt-install(pkg: str, ensure: str = "present", version: str = "") {
    observe {
        return state(
            ensure:  shell { probe-present },
            version: shell { probe-version },
        )
    }
    apply {
        r = shell { do-install }
        if !r { return err.runtime(r) }
        return ok.installed
    }
}`

func evalObserveDef(t *testing.T, f *evalFake, args map[string]string, mode engine.Mode) engine.Result {
	t.Helper()
	defs, err := ParseDefs(observeDef)
	if err != nil {
		t.Fatal(err)
	}
	res, err := EvalDefFull(defs[0], args, nil, nil, f, mode, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestObserve_Converged_SkipsApply(t *testing.T) {
	// Installed, present; version is "don't care" (no version arg) → in sync.
	f := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "present\n"},
		"probe-version": {Stdout: "1.2.0\n"},
	}}
	got := evalObserveDef(t, f, map[string]string{"pkg": "nginx"}, engine.Apply)
	if got.String() != "ok.already" {
		t.Fatalf("converged must skip with ok.already, got %s", got.String())
	}
	if f.calls["do-install"] {
		t.Fatal("apply must not run when converged")
	}
	if got.Changed {
		t.Fatal("a skip is not a change")
	}
}

func TestObserve_DriftOnPresence_RunsApply(t *testing.T) {
	// Not installed → ensure "absent" ≠ desired "present" → apply runs.
	f := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "absent\n"},
		"probe-version": {Stdout: "\n"},
		"do-install":    {Exit: 0},
	}}
	got := evalObserveDef(t, f, map[string]string{"pkg": "nginx"}, engine.Apply)
	if got.String() != "ok.installed" {
		t.Fatalf("drift must run apply → ok.installed, got %s", got.String())
	}
	if !f.calls["do-install"] {
		t.Fatal("apply must run on drift")
	}
	if !got.Changed {
		t.Fatal("a run apply is a change")
	}
}

func TestObserve_DriftOnVersion_RunsApply(t *testing.T) {
	// Present but 1.2.0 while 1.3.0 wanted → version field drifts → apply.
	f := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "present\n"},
		"probe-version": {Stdout: "1.2.0\n"},
		"do-install":    {Exit: 0},
	}}
	got := evalObserveDef(t, f, map[string]string{"pkg": "nginx", "version": "1.3.0"}, engine.Apply)
	if got.String() != "ok.installed" || !f.calls["do-install"] {
		t.Fatalf("version drift must run apply, got %s (ran=%v)", got.String(), f.calls["do-install"])
	}
}

func TestObserve_StatusMode(t *testing.T) {
	// Status reports observed vs desired and never runs apply.
	f := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "present\n"},
		"probe-version": {Stdout: "1.2.0\n"},
	}}
	// In sync (version is don't-care, unset) → OK, both fields converged.
	res := evalObserveDef(t, f, map[string]string{"pkg": "nginx"}, engine.Status)
	if res.Category != engine.OK || len(res.Fields) != 2 {
		t.Fatalf("status converged: %s fields=%+v", res, res.Fields)
	}
	if f.calls["do-install"] {
		t.Fatal("status must never run apply")
	}
	// Drift on version → WOULD, with the field carrying current → desired.
	d := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "present\n"}, "probe-version": {Stdout: "1.2.0\n"},
	}}
	res = evalObserveDef(t, d, map[string]string{"pkg": "nginx", "version": "1.3.0"}, engine.Status)
	if res.Category != engine.WOULD {
		t.Fatalf("status drift should be would: %s", res)
	}
	var ver engine.FieldDiff
	for _, fd := range res.Fields {
		if fd.Name == "version" {
			ver = fd
		}
	}
	if ver.Current != "1.2.0" || ver.Desired != "1.3.0" || ver.Converged {
		t.Fatalf("version field: %+v", ver)
	}
}

func TestObserve_Check_ConvergedVsDrift(t *testing.T) {
	// Converged in check → ok.already, and apply is never touched.
	conv := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "present\n"}, "probe-version": {Stdout: "1.2.0\n"},
	}}
	if got := evalObserveDef(t, conv, map[string]string{"pkg": "nginx"}, engine.Check); got.String() != "ok.already" {
		t.Fatalf("check+converged → ok.already, got %s", got.String())
	}
	if conv.calls["do-install"] {
		t.Fatal("check must never run apply")
	}
	// Drift in check → would.installed (the apply's nominal tag), no mutation.
	drift := &evalFake{resp: map[string]engine.ShellResult{
		"probe-present": {Stdout: "absent\n"}, "probe-version": {Stdout: "\n"},
	}}
	got := evalObserveDef(t, drift, map[string]string{"pkg": "nginx"}, engine.Check)
	if got.String() != "would.installed" {
		t.Fatalf("check+drift → would.installed, got %s", got.String())
	}
	if drift.calls["do-install"] {
		t.Fatal("check must never run apply")
	}
}
