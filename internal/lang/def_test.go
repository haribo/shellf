package lang

import "testing"

func TestParseDef_AptInstall(t *testing.T) {
	src := `
def apt-install(pkg: str) {
    pre-check: when pkg == "" -> err.pkgMustNotBeNull
    guard: shell { dpkg -s "$pkg" } -> ok.pkgAlreadyInstalled when ok
    apply: shell {
        apt-get install -y "$pkg"
    }
    ok.pkgInstalled
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
	if d.Name != "apt-install" {
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

	// pre-check: a guard on `pkg == ""`
	g, ok := d.Phases[0].Stmts[0].(GuardStmt)
	if !ok {
		t.Fatalf("pre-check stmt: %T", d.Phases[0].Stmts[0])
	}
	if b, ok := g.Cond.(Binary); !ok || b.Op != "==" {
		t.Fatalf("pre-check cond: %+v", g.Cond)
	}
	if g.Outcome.Category != "err" || g.Outcome.Tag != "pkgMustNotBeNull" {
		t.Fatalf("pre-check outcome: %+v", g.Outcome)
	}

	// guard: shell {...} -> ok.pkgAlreadyInstalled when ok
	e, ok := d.Phases[1].Stmts[0].(EffectStmt)
	if !ok {
		t.Fatalf("guard stmt: %T", d.Phases[1].Stmts[0])
	}
	if _, ok := e.Expr.(ShellExpr); !ok {
		t.Fatalf("guard expr: %T", e.Expr)
	}
	if e.Outcome == nil || e.Outcome.Tag != "pkgAlreadyInstalled" {
		t.Fatalf("guard outcome: %+v", e.Outcome)
	}
	if id, ok := e.When.(Ident); !ok || id.Name != "ok" {
		t.Fatalf("guard when: %+v", e.When)
	}
}

func TestParseDef_BoolFieldBinaryPayload(t *testing.T) {
	src := `
def svc(name: str, want: bool) {
    guard: shell { systemctl is-active --quiet "$name" }.ok == want -> ok.already
    apply {
        let r = shell { systemctl start "$name" }
        when r.exit != 0 -> err.runtime(r)
    }
    ok.changed
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

	// guard expr: (shell{}.ok) == want
	e := d.Phases[0].Stmts[0].(EffectStmt)
	bin, ok := e.Expr.(Binary)
	if !ok || bin.Op != "==" {
		t.Fatalf("guard expr not a binary: %+v", e.Expr)
	}
	f, ok := bin.L.(Field)
	if !ok || f.Name != "ok" {
		t.Fatalf("lhs not field .ok: %+v", bin.L)
	}
	if _, ok := f.Recv.(ShellExpr); !ok {
		t.Fatalf("field recv not shell: %T", f.Recv)
	}

	// apply block: let + when r.exit != 0 -> err.runtime(r)
	apply := d.Phases[1]
	if _, ok := apply.Stmts[0].(LetStmt); !ok {
		t.Fatalf("first apply stmt not let: %T", apply.Stmts[0])
	}
	when := apply.Stmts[1].(GuardStmt)
	if b := when.Cond.(Binary); b.Op != "!=" {
		t.Fatalf("when cond op: %s", b.Op)
	}
	if when.Outcome.Tag != "runtime" || when.Outcome.Payload == nil {
		t.Fatalf("err.runtime payload missing: %+v", when.Outcome)
	}
	if id := when.Outcome.Payload.(Ident); id.Name != "r" {
		t.Fatalf("payload: %+v", when.Outcome.Payload)
	}
}

func TestParseDef_Errors(t *testing.T) {
	cases := []string{
		`def x(pkg str) { ok.a }`,              // missing colon in param
		`def x() { guard: when a -> nope.tag }`, // unknown outcome category
		`def x() { apply: shell { echo hi }`,    // unterminated def (missing })
		`def x() { apply: 5 == }`,               // dangling operator
	}
	for _, src := range cases {
		if _, err := ParseDefs(src); err == nil {
			t.Fatalf("expected error for: %s", src)
		}
	}
}
