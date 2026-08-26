package lang

import (
	"strings"
	"testing"

	"shellf/internal/engine"
)

// The dogfood target: apt-install expressed as a shellf def.
const aptDef = `
def apt-install(pkg: str) {
    check {
        if pkg == "" { return err.pkgMustNotBeNull }
    }
    observe {
        return state(installed: shell { dpkg -s "$pkg" }.exit == 0)
    }
    apply {
        r = shell { apt-get install -y "$pkg" }
        if !r { return err.runtime(r) }
        return ok.pkgInstalled
    }
}`

const (
	dpkgS = `dpkg -s "$pkg"`
	aptS  = `apt-get install -y "$pkg"`
)

type evalFake struct {
	resp       map[string]engine.ShellResult
	calls      map[string]bool
	lastInterp string     // interpreter of the last shell (ADR-0012 threading)
	lastEnv    engine.Env // env of the last shell (ADR-0022 `with` threading)

	// ADR-0050 re-reads `observe` after an apply that acted, so a fake now has to say what
	// the apply produced. Without it every drift case reports `err.unconfirmed`, because
	// the fake answers the second observe exactly as it answered the first — insisting the
	// state never moved.
	//
	// `applyScript` names the script that acts; `after` overrides the probes' answers once
	// it has run. Both are opt-in: a test that never reaches an apply needs neither.
	applyScript string
	after       map[string]engine.ShellResult
	applied     bool
}

func (f *evalFake) As(string) engine.Executor { return f }

func (f *evalFake) Using(interp string) engine.Executor {
	if interp == "" {
		return f
	}
	return interpFake{f, interp}
}

// interpFake records the interpreter each shell runs under, then delegates.
type interpFake struct {
	inner  *evalFake
	interp string
}

func (i interpFake) Shell(script string, env engine.Env) engine.ShellResult {
	i.inner.lastInterp = i.interp
	return i.inner.Shell(script, env)
}
func (i interpFake) As(string) engine.Executor { return i }
func (i interpFake) Using(interp string) engine.Executor {
	if interp == "" {
		return i
	}
	return interpFake{i.inner, interp}
}

func (f *evalFake) Shell(script string, env engine.Env) engine.ShellResult {
	if f.calls == nil {
		f.calls = map[string]bool{}
	}
	f.calls[script] = true
	f.lastEnv = env
	if script == f.applyScript {
		f.applied = true
	} else if f.applied {
		if r, ok := f.after[script]; ok {
			return r
		}
	}
	if r, ok := f.resp[script]; ok {
		return r
	}
	return engine.ShellResult{Exit: 127}
}

func runApt(t *testing.T, f *evalFake, pkg string, mode engine.Mode) engine.Result {
	t.Helper()
	defs, err := ParseDefs(aptDef)
	if err != nil {
		t.Fatal(err)
	}
	res, err := EvalDefFull(defs[0], map[string]string{"pkg": pkg}, nil, nil, f, mode, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEvalDef_CheckEmpty(t *testing.T) {
	f := &evalFake{}
	if got := runApt(t, f, "", engine.Apply).String(); got != "err.pkgMustNotBeNull" {
		t.Fatalf("got %s, want err.pkgMustNotBeNull", got)
	}
	if len(f.calls) != 0 {
		t.Fatal("check must not touch the executor")
	}
}

func TestEvalDef_AlreadyInstalled(t *testing.T) {
	f := &evalFake{resp: map[string]engine.ShellResult{dpkgS: {Exit: 0}}}
	// Converged (installed) → the uniform derived skip `ok.already` (ADR-0013).
	if got := runApt(t, f, "nginx", engine.Apply).String(); got != "ok.already" {
		t.Fatalf("got %s, want ok.already", got)
	}
	if f.calls[aptS] {
		t.Fatal("apply ran despite the converged skip")
	}
}

func TestEvalDef_Installs(t *testing.T) {
	f := &evalFake{resp: map[string]engine.ShellResult{
		dpkgS: {Exit: 1}, // not installed
		aptS:  {Exit: 0}, // install ok
	}, applyScript: aptS, after: map[string]engine.ShellResult{
		dpkgS: {Exit: 0}, // and installed afterwards, which ADR-0050 re-reads
	}}
	if got := runApt(t, f, "nginx", engine.Apply).String(); got != "ok.pkgInstalled" {
		t.Fatalf("got %s, want ok.pkgInstalled", got)
	}
}

func TestEvalDef_ApplyFails_ErrRuntimeWithPayload(t *testing.T) {
	f := &evalFake{resp: map[string]engine.ShellResult{
		dpkgS: {Exit: 1},
		aptS:  {Exit: 100, Stderr: "E: locked"},
	}}
	res := runApt(t, f, "nginx", engine.Apply)
	if res.String() != "err.runtime" {
		t.Fatalf("got %s, want err.runtime", res)
	}
	if res.Shell == nil || res.Shell.Exit != 100 {
		t.Fatalf("err.runtime should carry the shell payload, got %+v", res.Shell)
	}
}

func TestEvalDef_Check_WouldNotMutate(t *testing.T) {
	f := &evalFake{resp: map[string]engine.ShellResult{dpkgS: {Exit: 1}}}
	if got := runApt(t, f, "nginx", engine.Check).String(); got != "would.pkgInstalled" {
		t.Fatalf("got %s, want would.pkgInstalled", got)
	}
	if f.calls[aptS] {
		t.Fatal("check mode ran apt-get")
	}
}

// ADR-0012: a def `using <interp>` runs its shells under that interpreter; a
// per-block `shell(<interp>)` overrides it.
func TestEvalDef_Interp(t *testing.T) {
	defs, err := ParseDefs(`def q(p: str) using bash { apply { r = shell { echo hi }  if !r { return err.x(r) }  return ok.done } }`)
	if err != nil {
		t.Fatal(err)
	}
	f := &evalFake{resp: map[string]engine.ShellResult{"echo hi": {Exit: 0}}}
	if _, err := EvalDefFull(defs[0], map[string]string{"p": "x"}, nil, nil, f, engine.Apply, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.lastInterp != "bash" {
		t.Fatalf("def `using bash` should run its shells under bash, got %q", f.lastInterp)
	}

	over, _ := ParseDefs(`def q(p: str) using bash { apply { r = shell(sh) { echo hi }  if !r { return err.x(r) }  return ok.done } }`)
	f2 := &evalFake{resp: map[string]engine.ShellResult{"echo hi": {Exit: 0}}}
	_, _ = EvalDefFull(over[0], map[string]string{"p": "x"}, nil, nil, f2, engine.Apply, nil, nil, nil, nil, nil)
	if f2.lastInterp != "sh" {
		t.Fatalf("shell(sh) must override the def interpreter, got %q", f2.lastInterp)
	}
}

// ADR-0022: a `with { }` binding reaches the def's shells as an env var and
// overrides a same-named param for that call only.
func TestEvalDef_WithOverridesEnv(t *testing.T) {
	defs, err := ParseDefs(`def q(p: str) { apply { r = shell { echo hi }  if !r { return err.x(r) }  return ok.done } }`)
	if err != nil {
		t.Fatal(err)
	}
	f := &evalFake{resp: map[string]engine.ShellResult{"echo hi": {Exit: 0}}}
	with := map[string]string{"p": "override", "extra": "v"}
	if _, err := EvalDefFull(defs[0], map[string]string{"p": "orig"}, with, nil, f, engine.Apply, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.lastEnv["p"] != "override" { // `with` wins over the passed arg
		t.Fatalf("with should override the param, env p=%q", f.lastEnv["p"])
	}
	if f.lastEnv["extra"] != "v" { // a new var injected for this call
		t.Fatalf("with should inject new env vars, got %+v", f.lastEnv)
	}
}

// ADR-0010: `.ok` on a shell result is gone — success is `if r` / `.exit == 0`.
func TestEvalDef_ShellDotOkRejected(t *testing.T) {
	src := `def q(p: str) { apply { r = shell { test -d "$p" } if r.ok { return ok.yes } return ok.done } }`
	defs, err := ParseDefs(src) // `.ok` still parses as a Field; it fails at eval
	if err != nil {
		t.Fatal(err)
	}
	f := &evalFake{resp: map[string]engine.ShellResult{`test -d "$p"`: {Exit: 0}}}
	if _, err := EvalDefFull(defs[0], map[string]string{"p": "/x"}, nil, nil, f, engine.Apply, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("`.ok` on a shell result must be an eval error (ADR-0010)")
	}
}

// ADR-0037 §1 reverses ADR-0007 §4: an `apply` names its verdict. The implicit tag-less
// `ok` is gone, because it made a forgotten `return` and a deliberate "nothing to
// declare" report identically — an omission read as a success. Measured before removing
// it: no stdlib def relied on it, all 31 `apply` blocks already returned.
func TestParseDef_ApplyMustEndWithAReturn(t *testing.T) {
	for name, src := range map[string]string{
		"no trailing return": `def touch(path: str) {
    apply {
        r = shell { touch "$path" }
        if r.exit != 0 { return err.runtime(r) }
    }
}`,
		"empty apply": `def touch(path: str) { apply { } }`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDefs(src)
			if err == nil {
				t.Fatal("an apply without a trailing return must be refused")
			}
			// The message has to say what to write: a refusal the author cannot act on
			// is worse than the implicit ok it replaces.
			if !strings.Contains(err.Error(), "return ok.") {
				t.Fatalf("the error must name the fix, got %v", err)
			}
		})
	}
}

// A trailing return is still what `would.<tag>` is derived from in check mode, where
// apply never runs (ADR-0007 §3, kept by ADR-0037).
func TestEvalDef_TrailingReturnDrivesWould(t *testing.T) {
	src := `
def touch(path: str) {
    apply {
        r = shell { touch "$path" }
        if r.exit != 0 { return err.runtime(r) }
        return ok.touched
    }
}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	f := &evalFake{resp: map[string]engine.ShellResult{`touch "$path"`: {Exit: 0}}}
	args := map[string]string{"path": "/tmp/x"}

	res, err := EvalDefFull(defs[0], args, nil, nil, f, engine.Apply, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != engine.OK || res.Tag != "touched" {
		t.Fatalf("apply: want ok.touched, got %s", res)
	}

	res, err = EvalDefFull(defs[0], args, nil, nil, f, engine.Check, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != engine.WOULD || res.Tag != "touched" {
		t.Fatalf("check: want would.touched, got %s", res)
	}
}

// ADR-0029: a `preview` phase attaches informational output in check mode, never
// runs in apply, and is best-effort (a failing shell yields no preview).
func TestEvalDef_Preview(t *testing.T) {
	src := `def act(x: str) {
		preview { shell { echo hi } }
		apply { r = shell { echo done }  if !r { return err.x(r) }  return ok.up }
	}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	// check mode: preview shell runs, its stdout rides on the result
	f := &evalFake{resp: map[string]engine.ShellResult{"echo hi": {Exit: 0, Stdout: "Recreate web\nRecreate worker\n"}}}
	res, err := EvalDefFull(defs[0], map[string]string{"x": "y"}, nil, nil, f, engine.Check, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != engine.WOULD || res.Preview != "Recreate web\nRecreate worker" {
		t.Fatalf("check: want would + preview text, got %s preview=%q", res, res.Preview)
	}
	if f.calls["echo done"] { // apply shell must NOT run in check
		t.Fatal("apply ran during check")
	}

	// apply mode: the preview phase is not run
	f2 := &evalFake{resp: map[string]engine.ShellResult{"echo done": {Exit: 0}, "echo hi": {Exit: 0, Stdout: "x"}}}
	if _, err := EvalDefFull(defs[0], map[string]string{"x": "y"}, nil, nil, f2, engine.Apply, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f2.calls["echo hi"] {
		t.Fatal("preview must not run in apply mode")
	}

	// best-effort: a preview shell that produces no stdout yields no preview,
	// but the step still previews would.up
	f3 := &evalFake{resp: map[string]engine.ShellResult{"echo hi": {Exit: 127}}}
	res3, _ := EvalDefFull(defs[0], map[string]string{"x": "y"}, nil, nil, f3, engine.Check, nil, nil, nil, nil, nil)
	if res3.Category != engine.WOULD || res3.Preview != "" {
		t.Fatalf("failing preview must degrade to plain would: %s preview=%q", res3, res3.Preview)
	}
}

// A shell's report shows the variables that shell can read — and only those. The naive
// substring match is wrong: `$p` is a prefix of `$path`, so a def with both would show a
// value the command never reads (#470).
func TestCitedVars_MatchesWholeNamesOnly(t *testing.T) {
	env := engine.Env{"p": "short", "path": "/opt/app", "mode": "755", "unused": "x"}

	got := citedVars(`chmod "$mode" "$path"`, env) // bare `$path`, where `$p` is a prefix

	if len(got) != 2 || got["mode"] != "755" || got["path"] != "/opt/app" {
		t.Fatalf("want mode and path only, got %v", got)
	}
	if _, ok := got["p"]; ok {
		t.Fatal("`$p` must not match inside `$path`")
	}
	if _, ok := got["unused"]; ok {
		t.Fatal("a variable the command never names must not be reported")
	}
}

// No variable cited: nothing is carried, rather than an empty map that serialises as noise.
func TestCitedVars_NoneCitedIsNil(t *testing.T) {
	if got := citedVars(`systemctl daemon-reload`, engine.Env{"a": "1"}); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

// Under `-v` a run must show every command, not only the one a failing def hands back.
// Without this the verbose report equals the silent one (#470).
func TestEvalDef_CollectsEveryShellItRan(t *testing.T) {
	src := `def d(path: str) {
    apply {
        a = shell { test -d "$path" }
        b = shell { systemctl is-enabled nginx }
        return ok.created
    }
}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	ex := &ranFake{}
	res, err := EvalDefFull(defs[0], map[string]string{"path": "/opt/app"}, nil, nil, ex,
		engine.Apply, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Ran) != 2 {
		t.Fatalf("want the 2 commands that ran, got %d: %+v", len(res.Ran), res.Ran)
	}
	if res.Ran[0].Cmd != `test -d "$path"` || res.Ran[1].Cmd != `systemctl is-enabled nginx` {
		t.Fatalf("commands lost or reordered: %q, %q", res.Ran[0].Cmd, res.Ran[1].Cmd)
	}
	// Source text, not a substituted command line: the value travels in the environment,
	// so no expanded string ever existed.
	if res.Ran[0].Vars["path"] != "/opt/app" {
		t.Fatalf("the value must be carried separately: %v", res.Ran[0].Vars)
	}
	if res.Ran[0].Def != "d" || res.Ran[0].Line != 3 {
		t.Fatalf("provenance lost: def=%q line=%d", res.Ran[0].Def, res.Ran[0].Line)
	}
}

// ranFake succeeds at everything: this test is about what gets recorded, not about
// outcomes.
type ranFake struct{}

func (f *ranFake) As(string) engine.Executor    { return f }
func (f *ranFake) Using(string) engine.Executor { return f }
func (f *ranFake) Shell(string, engine.Env) engine.ShellResult {
	return engine.ShellResult{Exit: 0}
}
