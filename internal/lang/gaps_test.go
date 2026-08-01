package lang

import (
	"strings"
	"testing"

	"shellf/internal/engine"
)

func TestUnescape_InStringArg(t *testing.T) {
	// A double-quoted arg processes \n \t \" \\ ; anything else stays literal.
	plan, err := ParsePlan("on s { file-write(\"/p\", \"a\\nb\\tc\\\"d\\\\e\\q\") }")
	if err != nil {
		t.Fatal(err)
	}
	got := plan[0].Steps[0].Args["content"]
	want := "a\nb\tc\"d\\eq" // \q is not an escape → backslash dropped, q kept
	if got != want {
		t.Fatalf("unescape: got %q want %q", got, want)
	}
}

func TestArg_BindingKinds(t *testing.T) {
	// Top-level bindings exercise arg(): raw string, bool, and an ident that
	// resolves against an earlier binding. Global bindings reach a call arg via
	// interpolation (a bare ident there would be a per-host ref instead).
	src := `
raw = """verbatim ${keep}"""
flag = true
base = "b"
ref = base
on s { file-write("/p", "${raw}|${ref}") }
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	if c := plan[0].Steps[0].Args["content"]; c != "verbatim ${keep}|b" {
		t.Fatalf("bindings not resolved via interpolation: %q", c)
	}
}

func TestArg_UndefinedBinding(t *testing.T) {
	_, err := ParsePlan("x = nope\non s { file-write(\"/p\", x) }")
	if err == nil || !strings.Contains(err.Error(), "undefined variable") {
		t.Fatalf("a binding to an unknown name must error: %v", err)
	}
}

func TestShellExpr_InterpAndUnless(t *testing.T) {
	// shellExpr in a def: an interpreter annotation + an `unless` guard block
	// (rawBracesRequired). Parsing only — no eval.
	defs, err := ParseDefs(`
def d() {
    apply {
        r = shell(bash) { echo hi } unless { test -f /x }
        if !r { return err.x }
        return ok
    }
}`)
	if err != nil {
		t.Fatal(err)
	}
	// Find the shell expr and check the captured interp/unless.
	le := defs[0].Phases[0].Stmts[0].(LetStmt)
	se := le.Value.(ShellExpr)
	if se.Interp != "bash" {
		t.Fatalf("shell(bash) interp not captured: %q", se.Interp)
	}
	if !strings.Contains(se.Unless, "test -f /x") {
		t.Fatalf("unless guard not captured: %q", se.Unless)
	}
}

func TestShellExpr_UnlessRequiresBraces(t *testing.T) {
	_, err := ParseDefs(`
def d() {
    apply {
        r = shell { echo hi } unless test -f /x
        return ok
    }
}`)
	if err == nil || !strings.Contains(err.Error(), "expected '{' after unless") {
		t.Fatalf("unless without a brace block must error: %v", err)
	}
}

func TestCallArgs_ParsedInDef(t *testing.T) {
	// A call expression `foo(a, b)` inside a def exercises callArgs (parse only;
	// eval rejects instruction calls, which is a separate concern).
	defs, err := ParseDefs(`
def d() {
    apply {
        x = foo("a", "b")
        return ok
    }
}`)
	if err != nil {
		t.Fatal(err)
	}
	call := defs[0].Phases[0].Stmts[0].(LetStmt).Value.(Call)
	if call.Name != "foo" || len(call.Args) != 2 {
		t.Fatalf("call args not parsed: %+v", call)
	}
}

func TestEvalDef_OkErrShorthand(t *testing.T) {
	// Bare `ok`/`err` after a shell test the last result (lastOK).
	src := `
def probe() {
    apply {
        shell { run-it }
        if ok { return ok.up }
        return err.down
    }
}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	// Shell succeeds → `ok` is true → ok.up.
	up := &evalFake{resp: map[string]engine.ShellResult{"run-it": {Exit: 0}}}
	if got, _ := EvalDef(defs[0], nil, up, engine.Apply); got.String() != "ok.up" {
		t.Fatalf("ok shorthand on success: %s", got.String())
	}
	// Shell fails → `ok` is false → falls through to err.down.
	down := &evalFake{resp: map[string]engine.ShellResult{"run-it": {Exit: 1}}}
	if got, _ := EvalDef(defs[0], nil, down, engine.Apply); got.String() != "err.down" {
		t.Fatalf("ok shorthand on failure: %s", got.String())
	}
}
