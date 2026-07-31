package lang

import (
	"testing"

	"shellf/internal/engine"
)

// The dogfood target: apt-install expressed as a shellf def.
const aptDef = `
def apt-install(pkg: str) {
    pre-check {
        if pkg == "" { return err.pkgMustNotBeNull }
    }
    guard {
        r = shell { dpkg -s "$pkg" }
        if r.ok { return ok.pkgAlreadyInstalled }
    }
    apply {
        r = shell { apt-get install -y "$pkg" }
        if r.exit != 0 { return err.runtime(r) }
        return ok.pkgInstalled
    }
}`

const (
	dpkgS = `dpkg -s "$pkg"`
	aptS  = `apt-get install -y "$pkg"`
)

type evalFake struct {
	resp  map[string]engine.ShellResult
	calls map[string]bool
}

func (f *evalFake) Shell(script string, _ engine.Env) engine.ShellResult {
	if f.calls == nil {
		f.calls = map[string]bool{}
	}
	f.calls[script] = true
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
	res, err := EvalDef(defs[0], map[string]string{"pkg": pkg}, f, mode)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEvalDef_PreCheckEmpty(t *testing.T) {
	f := &evalFake{}
	if got := runApt(t, f, "", engine.Apply).String(); got != "err.pkgMustNotBeNull" {
		t.Fatalf("got %s, want err.pkgMustNotBeNull", got)
	}
	if len(f.calls) != 0 {
		t.Fatal("pre-check must not touch the executor")
	}
}

func TestEvalDef_AlreadyInstalled(t *testing.T) {
	f := &evalFake{resp: map[string]engine.ShellResult{dpkgS: {Exit: 0}}}
	if got := runApt(t, f, "nginx", engine.Apply).String(); got != "ok.pkgAlreadyInstalled" {
		t.Fatalf("got %s, want ok.pkgAlreadyInstalled", got)
	}
	if f.calls[aptS] {
		t.Fatal("apply ran despite guard skip")
	}
}

func TestEvalDef_Installs(t *testing.T) {
	f := &evalFake{resp: map[string]engine.ShellResult{
		dpkgS: {Exit: 1}, // not installed
		aptS:  {Exit: 0}, // install ok
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

// ADR-0007: an apply with no trailing return yields an implicit, tag-less `ok`
// (and a tag-less `would` in check).
func TestEvalDef_ImplicitOk_NoTrailingReturn(t *testing.T) {
	src := `
def touch(path: str) {
    apply {
        r = shell { touch "$path" }
        if r.exit != 0 { return err.runtime(r) }
    }
}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	if defs[0].Return != nil {
		t.Fatalf("no trailing return → def.Return must be nil, got %+v", defs[0].Return)
	}
	f := &evalFake{resp: map[string]engine.ShellResult{`touch "$path"`: {Exit: 0}}}
	args := map[string]string{"path": "/tmp/x"}

	res, err := EvalDef(defs[0], args, f, engine.Apply)
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != engine.OK || res.Tag != "" {
		t.Fatalf("apply: want tag-less ok, got %s", res)
	}

	res, err = EvalDef(defs[0], args, f, engine.Check)
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != engine.WOULD || res.Tag != "" {
		t.Fatalf("check: want tag-less would, got %s", res)
	}
}
