package lang

import (
	"errors"

	"shellf/internal/engine"
	"strings"
	"testing"
)

// parseDef is a thin helper: parse one def and hand back the error, if any.
func parseDef(src string) error {
	_, err := ParseDefs(src)
	return err
}

// ADR-0034 §2: `%` is valid only before a primitive of the closed set. This is the rule
// that keeps shell off the operator's machine — if `%` could prefix a def, a def could
// run shell, and that shell would run where every SSH key lives. So the refusals matter
// more than the acceptances.
func TestControl_PercentOnlyBeforeAPrimitive(t *testing.T) {
	ok := []string{
		`def t(p: str) { apply { x = %file.read(p) } }`,
		`def t(c: str) { apply { x = %file.render(c) } }`,
		`def t(d: str) { apply { x = %dir.list(d) } }`,
		`def t() { apply { x = %"conf.j2" } }`,
	}
	for _, src := range ok {
		if err := parseDef(src); err != nil {
			t.Errorf("must parse: %s\n%v", src, err)
		}
	}

	refused := map[string]string{
		"a def":            `def t() { apply { x = %my-def() } }`,
		"a qualified def":  `def t() { apply { x = %docker.compose-up("/opt") } }`,
		"shell":            `def t() { apply { x = %shell { rm -rf / } } }`,
		"an unknown name":  `def t() { apply { x = %file.write("/etc/x", "y") } }`,
		"nothing":          `def t() { apply { x = % } }`,
		"a bare primitive": `def t(p: str) { apply { x = %file.read } }`,
	}
	for what, src := range refused {
		t.Run(what, func(t *testing.T) {
			err := parseDef(src)
			if err == nil {
				t.Fatalf("%% before %s must be refused: %s", what, src)
			}
		})
	}
}

// The refusal has to name what was written, or the author cannot see what to fix.
func TestControl_RefusalNamesTheOffender(t *testing.T) {
	err := parseDef(`def t() { apply { x = %docker.compose-up("/opt") } }`)
	if err == nil {
		t.Fatal("must be refused")
	}
	if !strings.Contains(err.Error(), "docker.compose-up") {
		t.Fatalf("the error must name the offending call: %v", err)
	}
	if !strings.Contains(err.Error(), "%file.read") {
		t.Fatalf("the error must list what is allowed: %v", err)
	}
}

// `%` belongs outside the quotes: a path that legitimately starts with one is ordinary
// data, and a marker inside a string would vanish when the value comes from a variable.
func TestControl_PercentInsideAStringIsNotAMarker(t *testing.T) {
	defs, err := ParseDefs(`def t() { apply { x = "%/etc/x" } }`)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected one def, got %d", len(defs))
	}
	// It parsed as a plain string: no ControlPath node anywhere.
	if strings.Contains(defs[0].Source, "ControlPath") {
		t.Fatal("a percent inside a string must stay data")
	}
}

// evalDef with a control-host fetcher, so the primitives can be exercised without the
// whole channel behind them.
func evalWithFetch(t *testing.T, src, entry string, args map[string]string, fetch ControlFetcher) (engine.Result, error) {
	t.Helper()
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	resolve := func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }
	return EvalDefWith(byName[entry], args, nil, noopExec{}, engine.Apply, resolve, []string{entry}, fetch)
}

type noopExec struct{}

func (noopExec) Shell(string, engine.Env) engine.ShellResult { return engine.ShellResult{} }
func (noopExec) As(string) engine.Executor                   { return noopExec{} }
func (noopExec) Using(string) engine.Executor                { return noopExec{} }

func TestControl_ReadAsksTheControlHost(t *testing.T) {
	var asked string
	fetch := func(r string) ([]byte, error) { asked = r; return []byte("contents"), nil }

	_, err := evalWithFetch(t, `def t(p: str) { apply { x = %file.read(p) } }`, "t",
		map[string]string{"p": "conf.j2"}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	// The primitive is part of the key, so listing a directory cannot be answered with
	// a file's contents.
	if asked != "file.read:conf.j2" {
		t.Fatalf("the resource key must carry the primitive: %q", asked)
	}
}

func TestControl_RenderSubstitutesTheDefScope(t *testing.T) {
	fetch := func(string) ([]byte, error) { return []byte("port = @{port}"), nil }
	src := `def t(p: str, port: str) { apply { x = %file.render(%file.read(p)) return ok.done } }`
	res, err := evalWithFetch(t, src, "t", map[string]string{"p": "c.j2", "port": "8080"}, fetch)
	if err != nil {
		t.Fatalf("render must resolve against the def's own scope: %v", err)
	}
	if res.String() != "ok.done" {
		t.Fatalf("got %s", res.String())
	}
}

// A refusal from the control host surfaces as an evaluation failure naming it, never as
// empty content — which would deliver a truncated file and report success.
func TestControl_FetchErrorSurfaces(t *testing.T) {
	fetch := func(string) ([]byte, error) { return nil, errors.New(`refused: "x" was not declared`) }
	_, err := evalWithFetch(t, `def t(p: str) { apply { x = %file.read(p) } }`, "t",
		map[string]string{"p": "x"}, fetch)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refusal must surface: %v", err)
	}
}

func TestControl_NoFetcherFails(t *testing.T) {
	_, err := evalWithFetch(t, `def t(p: str) { apply { x = %file.read(p) } }`, "t",
		map[string]string{"p": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "file.read") {
		t.Fatalf("no channel must fail naming the primitive: %v", err)
	}
}

// A control-host path literal is data until a primitive reads it.
func TestControl_PathLiteralIsInterpolated(t *testing.T) {
	var asked string
	fetch := func(r string) ([]byte, error) { asked = r; return []byte("x"), nil }
	_, err := evalWithFetch(t, `def t(name: str) { apply { x = %file.read(%"conf/${name}.j2") } }`, "t",
		map[string]string{"name": "web"}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "file.read:conf/web.j2" {
		t.Fatalf("a control path must interpolate like any string: %q", asked)
	}
}

// ADR-0034 §5: `%` occurrences are syntactic, which is what lets the control host build
// ADR-0031's allow-list before sending. If this misses one, the job is refused at
// runtime for a resource it legitimately needs.
func TestControl_ResourcesAreExtractable(t *testing.T) {
	defs, err := ParseDefs(`
def a(dst: str) { apply { x = %file.render(%file.read(%"conf.j2")) } }
def b() { observe { return state(there: %file.read(%"other.txt")) } }
def c(p: str) { apply { if %dir.list(%"tree") { shell { echo hi } } } }
`)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}

	got := ControlResources(byName, nil)
	want := []string{"dir.list:tree", "file.read:conf.j2", "file.read:other.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A path the target computes cannot be known before the run, so it is not in the set —
// and the request it makes is refused by name rather than silently served.
func TestControl_ComputedPathIsNotDeclared(t *testing.T) {
	defs, err := ParseDefs(`def a(p: str) { apply { x = %file.read(p) } }`)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{"a": defs[0]}
	if got := ControlResources(byName, nil); len(got) != 0 {
		t.Fatalf("a computed path must not enter the allow-list: %v", got)
	}
}

// The case that matters in practice: the path is written at the call site, and the def
// reading it only ever sees a parameter. Scanning defs alone would miss it, and the
// request would be refused at runtime for a file the plan legitimately declared.
func TestControl_PlanArgumentIsDeclared(t *testing.T) {
	sig := func(name string) ([]string, int, bool) {
		if name == "deliver" {
			return []string{"src", "dst"}, 2, true
		}
		return nil, 0, false
	}
	plan, err := ParsePlanWithVars(`on web { deliver(%"conf.j2", "/etc/app.conf") }`,
		map[string]string{}, nil, sig)
	if err != nil {
		t.Fatal(err)
	}
	step := plan[0].Steps[0]
	if len(step.Control) != 1 || step.Control[0] != "src" {
		t.Fatalf("the marked argument must be recorded: %+v", step.Control)
	}
	if step.Args["src"] != "conf.j2" {
		t.Fatalf("the value must travel as an ordinary string: %q", step.Args["src"])
	}

	got := ControlResources(nil, plan[0].Steps)
	if len(got) != 2 || got[0] != "dir.list:conf.j2" || got[1] != "file.read:conf.j2" {
		t.Fatalf("a marked plan argument must enter the allow-list: %v", got)
	}
}

// An unmarked argument is an ordinary string: a plan must say which paths are its own.
func TestControl_UnmarkedPlanArgumentIsNotDeclared(t *testing.T) {
	sig := func(string) ([]string, int, bool) { return []string{"src", "dst"}, 2, true }
	plan, err := ParsePlanWithVars(`on web { deliver("conf.j2", "/etc/app.conf") }`,
		map[string]string{}, nil, sig)
	if err != nil {
		t.Fatal(err)
	}
	if got := ControlResources(nil, plan[0].Steps); len(got) != 0 {
		t.Fatalf("an unmarked argument must not be served: %v", got)
	}
}

// The scan must reach every position a step can nest in, or a plan that declares a file
// inside a block or a branch is refused at runtime for a resource it did declare.
func TestControl_ScanReachesNestedSteps(t *testing.T) {
	sig := func(string) ([]string, int, bool) { return []string{"src", "dst"}, 2, true }
	plan, err := ParsePlanWithVars(`
on web {
  as root { deliver(%"in-block.j2", "/a") }
  parallel { deliver(%"in-parallel.j2", "/b") }
  if dir.exists("/opt") { deliver(%"in-then.j2", "/c") } else { deliver(%"in-else.j2", "/d") }
}`, map[string]string{}, nil, func(n string) ([]string, int, bool) {
		if n == "dir.exists" {
			return []string{"path"}, 1, true
		}
		return sig(n)
	})
	if err != nil {
		t.Fatal(err)
	}
	got := ControlResources(nil, plan[0].Steps)
	for _, want := range []string{
		"file.read:in-block.j2", "file.read:in-parallel.j2",
		"file.read:in-then.j2", "file.read:in-else.j2",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from %v", want, got)
		}
	}
}

// %file.render accepts content from a shell — the case that motivated splitting read
// from render, and that neither Go transformation could do.
func TestControl_RenderRejectsAPath(t *testing.T) {
	fetch := func(string) ([]byte, error) { return []byte("x"), nil }
	_, err := evalWithFetch(t, `def t() { apply { x = %file.render(%"conf.j2") } }`, "t", nil, fetch)
	if err == nil {
		t.Fatal("render takes content, not a control-host path: passing one must fail")
	}
}

// A primitive name that survives the parser but not the evaluator would be a silent
// hole; assert the evaluator has its own guard.
func TestControl_UnknownPrimitiveAtEval(t *testing.T) {
	defs, err := ParseDefs(`def t(p: str) { apply { x = %file.read(p) } }`)
	if err != nil {
		t.Fatal(err)
	}
	d := defs[0]
	// Rewrite the call to a name the parser would have refused.
	ph := d.Phases[0]
	let := ph.Stmts[0].(LetStmt)
	call := let.Value.(Call)
	call.Name = "file.wipe"
	let.Value = call
	ph.Stmts[0] = let
	d.Phases[0] = ph

	_, err = EvalDefWith(d, map[string]string{"p": "x"}, nil, noopExec{}, engine.Apply, nil, nil,
		func(string) ([]byte, error) { return nil, nil })
	if err == nil {
		t.Fatal("the evaluator must refuse a primitive it does not know")
	}
}
