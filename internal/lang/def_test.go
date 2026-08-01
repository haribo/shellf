package lang

import "testing"

func TestParseDef_Install(t *testing.T) {
	src := `
def install(pkg: str) {
    pre-check {
        if pkg == "" { return err.pkgMustNotBeNull }
    }
    guard {
        r = shell { dpkg -s "$pkg" }
        if r { return ok.pkgAlreadyInstalled }
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
	if len(d.Params) != 1 || d.Params[0] != (Param{"pkg", "str"}) {
		t.Fatalf("params: %+v", d.Params)
	}
	if len(d.Phases) != 3 {
		t.Fatalf("want 3 phases, got %d", len(d.Phases))
	}
	if d.Return == nil || d.Return.Category != "ok" || d.Return.Tag != "pkgInstalled" {
		t.Fatalf("return: %+v", d.Return)
	}

	// pre-check: `if pkg == "" { return err.pkgMustNotBeNull }`
	iff, ok := d.Phases[0].Stmts[0].(IfStmt)
	if !ok {
		t.Fatalf("pre-check stmt: %T", d.Phases[0].Stmts[0])
	}
	if b, ok := iff.Cond.(Binary); !ok || b.Op != "==" {
		t.Fatalf("pre-check cond: %+v", iff.Cond)
	}
	if r, ok := iff.Body[0].(ReturnStmt); !ok || r.Outcome.Category != "err" || r.Outcome.Tag != "pkgMustNotBeNull" {
		t.Fatalf("pre-check return: %+v", iff.Body)
	}

	// guard: `r = shell {…}` then `if r.ok { return ok.pkgAlreadyInstalled }`
	if _, ok := d.Phases[1].Stmts[0].(LetStmt); !ok {
		t.Fatalf("guard stmt0: %T", d.Phases[1].Stmts[0])
	}
	gif, ok := d.Phases[1].Stmts[1].(IfStmt)
	if !ok {
		t.Fatalf("guard stmt1: %T", d.Phases[1].Stmts[1])
	}
	if r := gif.Body[0].(ReturnStmt); r.Outcome.Tag != "pkgAlreadyInstalled" {
		t.Fatalf("guard return: %+v", r.Outcome)
	}
}

func TestParseDef_TruthyUnaryPayload(t *testing.T) {
	// ADR-0010: shell success is `if r` (truthy), failure is `if !r` (unary).
	src := `
def svc(name: str, want: bool) {
    guard {
        r = shell { systemctl is-active --quiet "$name" }
        if r { return ok.already }
    }
    apply {
        r = shell { systemctl start "$name" }
        if !r { return err.runtime(r) }
        return ok.changed
    }
}
`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	d := defs[0]
	if d.Params[1] != (Param{"want", "bool"}) {
		t.Fatalf("bool param: %+v", d.Params)
	}

	// guard: `if r { … }` — truthy on a ShellResult (bare Ident cond).
	gif, ok := d.Phases[0].Stmts[1].(IfStmt)
	if !ok {
		t.Fatalf("guard stmt1: %T", d.Phases[0].Stmts[1])
	}
	if id, ok := gif.Cond.(Ident); !ok || id.Name != "r" {
		t.Fatalf("guard cond not `r`: %+v", gif.Cond)
	}

	// apply: `if !r { return err.runtime(r) }` — unary `!` + err payload.
	aif := d.Phases[1].Stmts[1].(IfStmt)
	un, ok := aif.Cond.(Unary)
	if !ok || un.Op != "!" {
		t.Fatalf("apply cond not `!r`: %+v", aif.Cond)
	}
	if id, ok := un.X.(Ident); !ok || id.Name != "r" {
		t.Fatalf("unary operand not `r`: %+v", un.X)
	}
	ret := aif.Body[0].(ReturnStmt)
	if ret.Outcome.Tag != "runtime" || ret.Outcome.Payload == nil {
		t.Fatalf("err.runtime payload missing: %+v", ret.Outcome)
	}
	if id := ret.Outcome.Payload.(Ident); id.Name != "r" {
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

func TestParseDef_Errors(t *testing.T) {
	cases := []string{
		`def x(pkg str) { return ok.a }`,                 // missing colon in param
		`def x() { guard { if a { return nope.tag } } }`, // unknown outcome category
		`def x() { apply { shell { echo hi } }`,          // unterminated def (missing })
		`def x() { apply { 5 == } }`,                     // dangling operator
		`def x() { apply {} return ok.a }`,               // return outside a phase (ADR-0007)
	}
	for _, src := range cases {
		if _, err := ParseDefs(src); err == nil {
			t.Fatalf("expected error for: %s", src)
		}
	}
}
