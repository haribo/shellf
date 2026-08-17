package lang

import (
	"strings"
	"testing"
)

func TestParseDef_Install(t *testing.T) {
	src := `
def install(pkg: str) {
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
}
`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("want 1 def, got %d", len(defs))
	}
	d := defs[0]
	if d.Name != "install" {
		t.Fatalf("name: %q", d.Name)
	}
	if len(d.Params) != 1 || d.Params[0].Name != "pkg" || d.Params[0].Type != "str" {
		t.Fatalf("params: %+v", d.Params)
	}
	if len(d.Phases) != 3 {
		t.Fatalf("want 3 phases, got %d", len(d.Phases))
	}
	if d.Return == nil || d.Return.Category != "ok" || d.Return.Tag != "pkgInstalled" {
		t.Fatalf("return: %+v", d.Return)
	}

	// check: `if pkg == "" { return err.pkgMustNotBeNull }`
	iff, ok := d.Phases[0].Stmts[0].(IfStmt)
	if !ok {
		t.Fatalf("check stmt: %T", d.Phases[0].Stmts[0])
	}
	if b, ok := iff.Cond.(Binary); !ok || b.Op != "==" {
		t.Fatalf("check cond: %+v", iff.Cond)
	}
	if r, ok := iff.Body[0].(ReturnStmt); !ok || r.Outcome.Category != "err" || r.Outcome.Tag != "pkgMustNotBeNull" {
		t.Fatalf("check return: %+v", iff.Body)
	}

	// observe: `return state(installed: shell {…}.ok)` — a StateReturnStmt whose
	// single field is `installed`.
	if d.Phases[1].Name != "observe" {
		t.Fatalf("phase 1 should be observe: %q", d.Phases[1].Name)
	}
	sr, ok := d.Phases[1].Stmts[0].(StateReturnStmt)
	if !ok {
		t.Fatalf("observe stmt0: %T", d.Phases[1].Stmts[0])
	}
	if len(sr.Fields) != 1 || sr.Fields[0].Name != "installed" {
		t.Fatalf("observe field: %+v", sr.Fields)
	}
}

func TestParseDef_TruthyUnaryPayload(t *testing.T) {
	// ADR-0010: shell success is `if r` (truthy), failure is `if !r` (unary).
	src := `
def svc(name: str, want: bool) {
    apply {
        r = shell { systemctl is-active --quiet "$name" }
        if r { return ok.already }
        s = shell { systemctl start "$name" }
        if !s { return err.runtime(s) }
        return ok.changed
    }
}
`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	d := defs[0]
	if d.Params[1].Name != "want" || d.Params[1].Type != "bool" {
		t.Fatalf("bool param: %+v", d.Params)
	}

	// `if r { … }` — truthy on a ShellResult (bare Ident cond).
	gif, ok := d.Phases[0].Stmts[1].(IfStmt)
	if !ok {
		t.Fatalf("apply stmt1: %T", d.Phases[0].Stmts[1])
	}
	if id, ok := gif.Cond.(Ident); !ok || id.Name != "r" {
		t.Fatalf("truthy cond not `r`: %+v", gif.Cond)
	}

	// apply: `if !r { return err.runtime(r) }` — unary `!` + err payload.
	aif := d.Phases[0].Stmts[3].(IfStmt)
	un, ok := aif.Cond.(Unary)
	if !ok || un.Op != "!" {
		t.Fatalf("apply cond not `!s`: %+v", aif.Cond)
	}
	if id, ok := un.X.(Ident); !ok || id.Name != "s" {
		t.Fatalf("unary operand not `s`: %+v", un.X)
	}
	ret := aif.Body[0].(ReturnStmt)
	if ret.Outcome.Tag != "runtime" || ret.Outcome.Payload == nil {
		t.Fatalf("err.runtime payload missing: %+v", ret.Outcome)
	}
	if id := ret.Outcome.Payload.(Ident); id.Name != "s" {
		t.Fatalf("payload: %+v", ret.Outcome.Payload)
	}
}

func TestParseDef_Become(t *testing.T) {
	defs, err := ParseDefs(`def install(pkg: str) as root { apply { return ok.installed } }`)
	if err != nil {
		t.Fatal(err)
	}
	if defs[0].Become != "root" {
		t.Fatalf("def become: %q", defs[0].Become)
	}
	// no `as` → empty become
	plain, _ := ParseDefs(`def q(p: str) { check { return ok.yes } }`)
	if plain[0].Become != "" {
		t.Fatalf("unmarked def become: %q", plain[0].Become)
	}
}

func TestParseDef_Interp(t *testing.T) {
	defs, err := ParseDefs(`def build() using bash { apply { r = shell(bash) { false | true }  if !r { return err.runtime(r) }  return ok.built } }`)
	if err != nil {
		t.Fatal(err)
	}
	d := defs[0]
	if d.Interp != "bash" {
		t.Fatalf("def-declared interp: %q", d.Interp)
	}
	let := d.Phases[0].Stmts[0].(LetStmt)
	if se, ok := let.Value.(ShellExpr); !ok || se.Interp != "bash" {
		t.Fatalf("shell(bash) block annotation: %+v", let.Value)
	}
	// `as` and `using` compose, either order
	d2, _ := ParseDefs(`def x() as root using bash { apply { return ok.done } }`)
	if d2[0].Become != "root" || d2[0].Interp != "bash" {
		t.Fatalf("as+using: %+v", d2[0])
	}
	d3, _ := ParseDefs(`def x() using bash as root { apply { return ok.done } }`)
	if d3[0].Become != "root" || d3[0].Interp != "bash" {
		t.Fatalf("using+as: %+v", d3[0])
	}
}

func TestParseDef_Interp_Unknown(t *testing.T) {
	if _, err := ParseDefs(`def x() using fish { apply { return ok.a } }`); err == nil {
		t.Fatal("an unknown def interpreter must error")
	}
}

func TestParseDef_Errors(t *testing.T) {
	cases := []string{
		`def x(pkg str) { return ok.a }`,                                // missing colon in param
		`def x() { apply { if a { return nope.tag } return ok.done } }`, // unknown outcome category
		`def x() { apply { shell { echo hi } return ok.done }`,          // unterminated def (missing })
		`def x() { apply { 5 == return ok.done } }`,                     // dangling operator
		`def x() { apply { return ok.done } return ok.a }`,              // return outside a phase (ADR-0007)
	}
	for _, src := range cases {
		if _, err := ParseDefs(src); err == nil {
			t.Fatalf("expected error for: %s", src)
		}
	}
}

// #418, ADR-0045 §1: the type vocabulary is closed. `def t(p: banana)` parsed and ran —
// the parser read an identifier and nothing read it back, so the annotation documented an
// intent no one enforced.
func TestParams_TypeVocabularyIsClosed(t *testing.T) {
	for _, ok := range []string{
		`def t(p: str) { apply { shell { echo "$p" } return ok.done } }`,
		`def t(p: bool) { apply { shell { echo "$p" } return ok.done } }`,
	} {
		if _, err := ParseDefs(ok); err != nil {
			t.Fatalf("must parse: %s\n%v", ok, err)
		}
	}
	_, err := ParseDefs(`def t(p: banana) { apply { shell { echo "$p" } return ok.done } }`)
	if err == nil {
		t.Fatal("an invented type must be refused, not carried as decoration")
	}
	if !strings.Contains(err.Error(), "banana") {
		t.Fatalf("the refusal must name what was written: %v", err)
	}
	if !strings.Contains(err.Error(), "str") || !strings.Contains(err.Error(), "bool") {
		t.Fatalf("the refusal must name what is accepted: %v", err)
	}
}
