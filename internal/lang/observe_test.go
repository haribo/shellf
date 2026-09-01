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
	}, applyScript: "do-install", after: map[string]engine.ShellResult{
		// What the install leaves behind — ADR-0050 re-reads the probes after it.
		"probe-present": {Stdout: "present\n"},
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
	}, applyScript: "do-install", after: map[string]engine.ShellResult{
		// The install moved the version to the one asked for.
		"probe-version": {Stdout: "1.3.0\n"},
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

// ADR-0051: a question that answers *no* in check mode answers `would`, and only there.
//
// A question is a def whose whole body is a `check` — no observe, no apply. Its answer in
// `--dry-run` is about the world as it stands, and the plan has changed nothing yet: a `no`
// is often exactly the state the plan is about to create, so reporting it as an error halts
// a preview for a reason that will not exist at apply time (#508).
//
// The asymmetry is the decision, and each half is pinned here: a `yes` still resolves — that
// is ADR-0004's case, which this amends rather than reverses.
func TestQuestion_FailingAnswerIsWouldInCheckOnly(t *testing.T) {
	defs, err := ParseDefs(`
def present(path: str) {
    check {
        r = shell { probe }
        if r { return ok.present }
        return err.absent
    }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	def := defs[0]

	run := func(exit int, mode engine.Mode) string {
		f := &evalFake{resp: map[string]engine.ShellResult{"probe": {Exit: exit}}}
		res, err := EvalDefFull(def, map[string]string{"path": "/x"}, nil, nil, f, mode, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.String()
	}

	for name, tc := range map[string]struct {
		exit int
		mode engine.Mode
		want string
	}{
		"no, in check → not knowable yet": {1, engine.Check, "would"},
		"yes, in check → resolved":        {0, engine.Check, "ok.present"},
		"no, in apply → a fact":           {1, engine.Apply, "err.absent"},
		"yes, in apply → a fact":          {0, engine.Apply, "ok.present"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(tc.exit, tc.mode); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// The gates: a def that is not a question keeps its error in check mode, because its `check`
// validates arguments rather than interrogating state — that is what ADR-0035 protects, and
// softening it would hide a plan's own errors from `--dry-run`.
func TestQuestion_OnlyAQuestionIsSoftened(t *testing.T) {
	defs, err := ParseDefs(`
def resource(path: str) {
    check {
        r = shell { validate }
        if !r { return err.badArgument }
    }
    observe { return state(present: shell { probe }.exit == 0) }
    apply {
        r = shell { create }
        if !r { return err.runtime(r) }
        return ok.created
    }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	f := &evalFake{resp: map[string]engine.ShellResult{"validate": {Exit: 1}, "probe": {Exit: 1}}}
	res, err := EvalDefFull(defs[0], map[string]string{"path": "/x"}, nil, nil, f, engine.Check, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.String() != "err.badArgument" {
		t.Fatalf("a def with observe/apply keeps its check error in check mode, got %s", res)
	}
}
