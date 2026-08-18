package lang

import (
	"strings"
	"testing"

	"shellf/internal/engine"
)

// ADR-0037 §2: a def may hold one call outside every phase. It *is* that def with
// rebound arguments, so the callee's own phases run — in every mode. That is the point:
// `apply` is skipped in `--dry-run`, so a call placed there loses the callee's `check`
// and `observe` and the preview lies (#340 is the same defect seen from the stdlib).
func TestDelegation_ParsesAndRecordsTheCall(t *testing.T) {
	defs, err := ParseDefs(`def template(src: str, dst: str) { file.write(dst, src) }`)
	if err != nil {
		t.Fatal(err)
	}
	if defs[0].Delegate == nil {
		t.Fatal("a body-level call must be recorded as a delegation")
	}
	if defs[0].Delegate.Name != "file.write" {
		t.Fatalf("got %q", defs[0].Delegate.Name)
	}
}

// The refusals, each with the message that says what to do instead.
func TestDelegation_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"two calls": {
			`def d(a: str) { one(a) two(a) }`,
			"exactly one",
		},
		"observe beside a delegation": {
			`def d(a: str) { observe { return state(x: "1") } one(a) }`,
			"may only declare `check`",
		},
		"apply beside a delegation": {
			`def d(a: str) { one(a) apply { return ok.x } }`,
			"may only declare `check`",
		},
		"preview beside a delegation": {
			`def d(a: str) { preview { shell { echo hi } } one(a) }`,
			"may only declare `check`",
		},
		"a shell in an argument": {
			`def d(a: str) { one(shell { cat /etc/hostname }) }`,
			"may not run a shell",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDefs(tc.src)
			if err == nil {
				t.Fatal("must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("message must name the rule (%q), got %v", tc.want, err)
			}
		})
	}
}

// `check` is the one phase that composes with a delegation: argument validation belongs
// to the wrapper, and it runs in every mode (ADR-0035).
func TestDelegation_CheckStillRuns(t *testing.T) {
	src := `
def leaf(p: str) { apply { r = shell { touch "$p" }  if !r { return err.runtime(r) }  return ok.written } }
def wrap(p: str) {
    check { if p == "" { return err.empty } }
    leaf(p)
}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	resolve := func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }

	res, err := EvalDefFull(byName["wrap"], map[string]string{"p": ""}, nil, nil, &delegFake{}, engine.Apply, resolve, []string{"wrap"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.String() != "err.empty" {
		t.Fatalf("the wrapper's own check must still refuse, got %s", res)
	}
}

// The verdict is the callee's, in every mode. In check mode this is the whole gain: the
// callee decides, where an `apply` would have decided nothing.
func TestDelegation_VerdictComesFromTheCallee(t *testing.T) {
	src := `
def leaf(p: str) {
    observe { return state(synced: shell { test -f "$p" }.exit == 0) }
    apply { r = shell { touch "$p" }  if !r { return err.runtime(r) }  return ok.written }
}
def wrap(p: str) { leaf(p) }`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	resolve := func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }

	for name, tc := range map[string]struct {
		observe engine.ShellResult
		mode    engine.Mode
		want    string
	}{
		"converged, dry-run": {engine.ShellResult{Exit: 0}, engine.Check, "ok.already"},
		"drifted, dry-run":   {engine.ShellResult{Exit: 1}, engine.Check, "would.written"},
		"converged, apply":   {engine.ShellResult{Exit: 0}, engine.Apply, "ok.already"},
		"drifted, apply":     {engine.ShellResult{Exit: 1}, engine.Apply, "ok.written"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &delegFake{observe: tc.observe}
			res, err := EvalDefFull(byName["wrap"], map[string]string{"p": "/tmp/x"}, nil, nil, f, tc.mode, resolve, []string{"wrap"}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.String() != tc.want {
				t.Fatalf("got %s, want %s", res, tc.want)
			}
		})
	}
}

// delegFake answers the observe read and lets everything else succeed. Its own type
// rather than a field on evalFake: this file's needs are different, and widening a
// shared helper for one test is how helpers stop being readable.
type delegFake struct{ observe engine.ShellResult }

func (f *delegFake) As(string) engine.Executor    { return f }
func (f *delegFake) Using(string) engine.Executor { return f }

func (f *delegFake) Shell(script string, _ engine.Env) engine.ShellResult {
	if strings.Contains(script, "test -f") {
		return f.observe
	}
	return engine.ShellResult{Exit: 0}
}

// #441: a def that calls another loses the category of the error it propagates. Observed
// on v0.5.0, a one-line bridge def over `sshd.config` with an invalid directive:
//
//	hosting.sshd-drop-in(…) err.agent
//	    ! sshd.config returned err.validation
//
// The note is right and the top line is not. `err.agent` means the agent could not be
// reached or could not run — the operator cannot tell a broken config from a broken
// connection — and `if x == err.validation`, which language.md documents, can never match
// one call deep.
func TestDelegation_KeepsTheCalleeErrorCategory(t *testing.T) {
	src := `
def inner(p: str) { apply { return err.validation } }
def outer(p: str) { apply { inner(p) return ok.done } }
`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	resolve := func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }

	res, err := EvalDefFull(byName["outer"], map[string]string{"p": "x"}, nil, nil,
		noopExec{}, engine.Apply, resolve, []string{"outer"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("a callee's err is a verdict, not an evaluation failure: %v", err)
	}
	if got := res.String(); got != "err.validation" {
		t.Fatalf("the caller must report what the callee returned, got %s", got)
	}
}

// The other half of #441, and the reason the fix is narrow: `err.agent` must keep meaning
// "the agent could not run this". An evaluation failure inside a callee — an unbound
// variable, a missing channel — is still that, and must not borrow the callee's category
// machinery just because it happened one call deep.
func TestDelegation_EvaluationFailureIsStillAnAgentError(t *testing.T) {
	src := `
def inner(p: str) { apply { x = ~file.read(p) return ok.done } }
def outer(p: str) { apply { inner(p) return ok.done } }
`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	resolve := func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }

	// No fetcher: the primitive cannot reach the control host, which is a failure of the
	// run rather than a verdict the def chose.
	_, err = EvalDefFull(byName["outer"], map[string]string{"p": "c.j2"}, nil, []string{"p"},
		noopExec{}, engine.Apply, resolve, []string{"outer"}, nil, nil, nil)
	if err == nil {
		t.Fatal("an evaluation failure must stay an error, not become a verdict")
	}
	if !strings.Contains(err.Error(), "control host") {
		t.Fatalf("the failure must keep saying what broke: %v", err)
	}
}
