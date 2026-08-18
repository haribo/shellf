package lang

import (
	"errors"
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/proto"
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
		`def t(p: str) { apply { x = ~file.read(p) return ok.done } }`,
		`def t(p: str) { apply { x = ~file.render(p) return ok.done } }`,
		`def t(d: str) { apply { x = ~dir.list(d) return ok.done } }`,
	}
	for _, src := range ok {
		if err := parseDef(src); err != nil {
			t.Errorf("must parse: %s\n%v", src, err)
		}
	}

	refused := map[string]string{
		"a def":            `def t() { apply { x = %my-def() return ok.done } }`,
		"a qualified def":  `def t() { apply { x = %docker.compose-up("/opt") return ok.done } }`,
		"shell":            `def t() { apply { x = %shell { rm -rf / } return ok.done } }`,
		"an unknown name":  `def t() { apply { x = %file.write("/etc/x", "y") return ok.done } }`,
		"nothing":          `def t() { apply { x = % return ok.done } }`,
		"a bare primitive": `def t(p: str) { apply { x = ~file.read return ok.done } }`,
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
	// A def marked as a primitive: named, and told what is allowed.
	err := parseDef(`def t() { apply { x = ~docker.compose-up("/opt") return ok.done } }`)
	if err == nil {
		t.Fatal("must be refused")
	}
	if !strings.Contains(err.Error(), "docker.compose-up") {
		t.Fatalf("the error must name the offending call: %v", err)
	}
	if !strings.Contains(err.Error(), "~file.read") {
		t.Fatalf("the error must list what is allowed: %v", err)
	}
}

// `%name(…)` was the ADR-0034 spelling and shipped this morning, so it will be typed.
// It must point at `~`, without claiming that particular name is a primitive.
func TestControl_OldPercentCallSpellingIsRefused(t *testing.T) {
	err := parseDef(`def t() { apply { x = %file.read("conf.j2") return ok.done } }`)
	if err == nil {
		t.Fatal("the old spelling must be refused")
	}
	if !strings.Contains(err.Error(), "~file.read") {
		t.Fatalf("the error must point at the new spelling: %v", err)
	}
}

// `%` belongs outside the quotes: a path that legitimately starts with one is ordinary
// data, and a marker inside a string would vanish when the value comes from a variable.
func TestControl_PercentInsideAStringIsNotAMarker(t *testing.T) {
	defs, err := ParseDefs(`def t() { apply { x = "%/etc/x" return ok.done } }`)
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
	return evalWithFetchControl(t, src, entry, args, nil, fetch)
}

// evalWithFetchControl marks some arguments as control-host paths, as a plan writing
// `%"…"` would — without it a primitive reads the target instead (#332).
func evalWithFetchControl(t *testing.T, src, entry string, args map[string]string, control []string, fetch ControlFetcher) (engine.Result, error) {
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
	return EvalDefFull(byName[entry], args, nil, control, noopExec{}, engine.Apply, resolve, []string{entry}, fetch, nil, nil)
}

type noopExec struct{}

func (noopExec) Shell(string, engine.Env) engine.ShellResult { return engine.ShellResult{} }
func (noopExec) As(string) engine.Executor                   { return noopExec{} }
func (noopExec) Using(string) engine.Executor                { return noopExec{} }

func TestControl_ReadAsksTheControlHost(t *testing.T) {
	var asked string
	fetch := func(r string, _ []byte, _ map[string]string) ([]byte, error) {
		asked = r
		return []byte("contents"), nil
	}

	_, err := evalWithFetchControl(t, `def t(p: str) { apply { x = ~file.read(p) return ok.done } }`, "t",
		map[string]string{"p": "conf.j2"}, []string{"p"}, fetch)
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
	// The control host is what substitutes (ADR-0036 §5), over the scope the ask
	// carries; this stands in for it, so the assertion is on the substitution itself
	// rather than on the def merely finishing. Since #392 it also reads the template,
	// so the ask names it and carries no content (ADR-0042 §1).
	var rendered string
	fetch := func(resource string, _ []byte, vars map[string]string) ([]byte, error) {
		if resource != "file.render:c.j2" {
			return nil, errors.New("unexpected resource " + resource)
		}
		rendered = strings.ReplaceAll("port = @{port}", "@{port}", vars["port"])
		return []byte(rendered), nil
	}
	// `p` is marked as a control-host path, as a plan writing `%"c.j2"` would: unmarked,
	// the render is refused rather than handed a path read on the target.
	src := `def t(p: str, port: str) { apply { x = ~file.render(p) return ok.done } }`
	res, err := evalWithFetchControl(t, src, "t", map[string]string{"p": "c.j2", "port": "8080"},
		[]string{"p"}, fetch)
	if err != nil {
		t.Fatalf("render must resolve against the def's own scope: %v", err)
	}
	if rendered != "port = 8080" {
		t.Fatalf("the def's own scope must reach the render: got %q", rendered)
	}
	if res.String() != "ok.done" {
		t.Fatalf("got %s", res.String())
	}
}

// #334: the caller's scope travels with the ask. A template names variables from both
// sides — the host's, which stay on the control host, and the call site's params and
// `with` override, which exist only here — so an ask carrying the resource alone renders
// a `with { }` binding as an undefined variable. The scope is what an ask still brings
// once the content stops travelling (#392).
func TestControl_RenderSendsTheCallerScope(t *testing.T) {
	var got map[string]string
	fetch := func(_ string, _ []byte, vars map[string]string) ([]byte, error) {
		got = vars
		return []byte("rendered"), nil
	}
	defs, err := ParseDefs(`def t(p: str, port: str) { apply { x = ~file.render(p) return ok.done } }`)
	if err != nil {
		t.Fatal(err)
	}
	// `p` is the template's path, marked at the call site; `port` is an ordinary
	// parameter, and it is what a template names.
	if _, err := EvalDefFull(defs[0], map[string]string{"p": "c.j2", "port": "8080"},
		map[string]string{"greeting": "hello-with"}, []string{"p"}, noopExec{}, engine.Apply, nil, nil, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got["greeting"] != "hello-with" {
		t.Fatalf("a `with` override must reach the renderer, got %q", got["greeting"])
	}
	if got["port"] != "8080" {
		t.Fatalf("a def parameter must reach the renderer, got %q", got["port"])
	}
}

// A refusal from the control host surfaces as an evaluation failure naming it, never as
// empty content — which would deliver a truncated file and report success.
func TestControl_FetchErrorSurfaces(t *testing.T) {
	fetch := func(string, []byte, map[string]string) ([]byte, error) {
		return nil, errors.New(`refused: "x" was not declared`)
	}
	_, err := evalWithFetchControl(t, `def t(p: str) { apply { x = ~file.read(p) return ok.done } }`, "t",
		map[string]string{"p": "x"}, []string{"p"}, fetch)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refusal must surface: %v", err)
	}
}

func TestControl_NoFetcherFails(t *testing.T) {
	_, err := evalWithFetchControl(t, `def t(p: str) { apply { x = ~file.read(p) return ok.done } }`, "t",
		map[string]string{"p": "x"}, []string{"p"}, nil)
	if err == nil || !strings.Contains(err.Error(), "file.read") {
		t.Fatalf("no channel must fail naming the primitive: %v", err)
	}
}

// A control-host path literal interpolates like any string, so a plan can name a file per
// host. Written in a plan, which is the only place a `%"…"` may now sit (ADR-0043).
func TestControl_PathLiteralIsInterpolated(t *testing.T) {
	sig := func(string) ([]Param, int, bool) { return strParams("src", "dst"), 2, true }
	plan, err := parsePlan(`on web { deliver(%"conf/${name}.j2", "/etc/app.conf") }`,
		map[string]string{"name": "web"}, nil, sig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := ControlResources(plan[0].Steps)
	found := false
	for _, r := range got {
		if r == "file.read:conf/web.j2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a control path must interpolate like any string: %v", got)
	}
}

// ADR-0034 §5: `%` occurrences are syntactic, which is what lets the control host build
// ADR-0031's allow-list before sending. If this misses one, the job is refused at
// runtime for a resource it legitimately needs.
//
// The occurrences are the plan's: a def may not write one (#403, ADR-0043), so the def
// half of this test moved to TestControl_DefMayNotDeclareAPath, where those same three
// bodies are now refusals.
func TestControl_ResourcesAreExtractable(t *testing.T) {
	steps := []proto.Step{
		{Instruction: "deliver", Args: map[string]string{"src": "conf.j2"}, Control: []string{"src"}},
		{Instruction: "mirror", Args: map[string]string{"tree": "tree"}, Control: []string{"tree"}},
	}
	got := ControlResources(steps)
	want := []string{
		"dir.list:conf.j2", "dir.list:tree",
		"dir.sync:conf.j2", "dir.sync:tree",
		"file.read:conf.j2", "file.read:tree",
		"file.render:conf.j2", "file.render:tree",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// #403, ADR-0043 — the allow-list is the operator's declaration, so only the plan writes
// it. A def naming a control-host file in its own body was adding itself to the list that
// bounds it; it is now a parse error, before the run rather than during it.
func TestControl_DefMayNotDeclareAPath(t *testing.T) {
	refused := map[string]string{
		"in an apply":     `def t() { apply { x = ~file.read(%"conf.j2") return ok.done } }`,
		"in an observe":   `def t() { observe { return state(v: ~file.read(%"conf.j2")) } }`,
		"in a condition":  `def t() { apply { if ~dir.list(%"tree") { shell { echo hi } } return ok.done } }`,
		"calling a def":   `def t() { apply { file.copy(%"conf.j2", "/etc/x") return ok.done } }`,
		"a bare literal":  `def t() { apply { x = %"conf.j2" return ok.done } }`,
		"in a delegation": `def t(dst: str) { file.copy(%"conf.j2", dst) }`,
	}
	for name, src := range refused {
		t.Run(name, func(t *testing.T) {
			err := parseDef(src)
			if err == nil {
				t.Fatal("a def may not name a control-host file: the plan declares, the def receives")
			}
			if !strings.Contains(err.Error(), "parameter") {
				t.Fatalf("the refusal must say what to write instead: %v", err)
			}
		})
	}

	// The same paths, written where they belong: unchanged.
	if err := parseDef(`def t(src: str) { apply { x = ~file.read(src) return ok.done } }`); err != nil {
		t.Fatalf("a def taking its path as a parameter must parse: %v", err)
	}
}

// A path the target computes cannot be known before the run, so it is not in the set —
// and the request it makes is refused by name rather than silently served.
func TestControl_ComputedPathIsNotDeclared(t *testing.T) {
	// No `Control` marking: the plan passed an ordinary string, so nothing is declared.
	steps := []proto.Step{{Instruction: "deliver", Args: map[string]string{"src": "/tmp/x"}}}
	if got := ControlResources(steps); len(got) != 0 {
		t.Fatalf("a computed path must not enter the allow-list: %v", got)
	}
}

// The case that matters in practice: the path is written at the call site, and the def
// reading it only ever sees a parameter. Scanning defs alone would miss it, and the
// request would be refused at runtime for a file the plan legitimately declared.
func TestControl_PlanArgumentIsDeclared(t *testing.T) {
	sig := func(name string) ([]Param, int, bool) {
		if name == "deliver" {
			return strParams("src", "dst"), 2, true
		}
		return nil, 0, false
	}
	plan, err := parsePlan(`on web { deliver(%"conf.j2", "/etc/app.conf") }`,
		map[string]string{}, nil, sig, nil, nil)
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

	// Every read primitive is declared for a marked path: the plan says the file is its
	// to serve, and which primitive reads it is the def's business — `file.template`
	// reads it, `dir.copy` syncs it (#335). Sorted, so the set is comparable.
	got := ControlResources(plan[0].Steps)
	want := []string{"dir.list:conf.j2", "dir.sync:conf.j2", "file.read:conf.j2", "file.render:conf.j2"}
	if len(got) != len(want) {
		t.Fatalf("a marked plan argument must enter the allow-list: %v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("allow-list entry %d: got %q, want %q (full set %v)", i, got[i], w, got)
		}
	}
}

// An unmarked argument is an ordinary string: a plan must say which paths are its own.
func TestControl_UnmarkedPlanArgumentIsNotDeclared(t *testing.T) {
	sig := func(string) ([]Param, int, bool) { return strParams("src", "dst"), 2, true }
	plan, err := parsePlan(`on web { deliver("conf.j2", "/etc/app.conf") }`,
		map[string]string{}, nil, sig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ControlResources(plan[0].Steps); len(got) != 0 {
		t.Fatalf("an unmarked argument must not be served: %v", got)
	}
}

// The scan must reach every position a step can nest in, or a plan that declares a file
// inside a block or a branch is refused at runtime for a resource it did declare.
func TestControl_ScanReachesNestedSteps(t *testing.T) {
	sig := func(string) ([]Param, int, bool) { return strParams("src", "dst"), 2, true }
	plan, err := parsePlan(`
on web {
  as root { deliver(%"in-block.j2", "/a") }
  parallel { deliver(%"in-parallel.j2", "/b") }
  if dir.exists("/opt") { deliver(%"in-then.j2", "/c") } else { deliver(%"in-else.j2", "/d") }
}`, map[string]string{}, nil, func(n string) ([]Param, int, bool) {
		if n == "dir.exists" {
			return strParams("path"), 1, true
		}
		return sig(n)
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := ControlResources(plan[0].Steps)
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

// #392, ADR-0042 — the reverse of what this test asserted until then. `~file.render` took
// content, which let an imported def submit `"@{db_password}"` and be answered by the
// machine holding it; it now names a declared template, and content is refused.
func TestControl_RenderRequiresAControlPath(t *testing.T) {
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return []byte("x"), nil }

	if _, err := evalWithFetchControl(t, `def t(src: str) { apply { x = ~file.render(src) return ok.done } }`, "t",
		map[string]string{"src": "conf.j2"}, []string{"src"}, fetch); err != nil {
		t.Fatalf("a marked template is what render takes: %v", err)
	}

	_, err := evalWithFetch(t, `def t() { apply { x = ~file.render("host = @{db_password}") return ok.done } }`, "t", nil, fetch)
	if err == nil {
		t.Fatal("content submitted by the target must be refused, not substituted")
	}
	if !strings.Contains(err.Error(), `%`) {
		t.Fatalf("the refusal must name the fix: %v", err)
	}
}

// A primitive name that survives the parser but not the evaluator would be a silent
// hole; assert the evaluator has its own guard.
func TestControl_UnknownPrimitiveAtEval(t *testing.T) {
	defs, err := ParseDefs(`def t(p: str) { apply { x = ~file.read(p) return ok.done } }`)
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

	_, err = EvalDefFull(d, map[string]string{"p": "x"}, nil, nil, noopExec{}, engine.Apply, nil, nil, func(string, []byte, map[string]string) ([]byte, error) { return nil, nil }, nil, nil)
	if err == nil {
		t.Fatal("the evaluator must refuse a primitive it does not know")
	}
}

// The phase table is the contract; a def declaring a removed phase must fail at parse
// with what to do, not with "unknown".
func TestPhases_RemovedNamesRefused(t *testing.T) {
	for name, want := range map[string]string{
		"pre-check": "check",
		"post":      "removed",
	} {
		_, err := ParseDefs(`def t() { ` + name + ` { return ok.x } }`)
		if err == nil {
			t.Fatalf("%s must be refused", name)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// And the surviving four still parse.
	for _, name := range []string{"check", "observe", "preview", "apply"} {
		if _, err := ParseDefs(`def t() { ` + name + ` { return ok.x } }`); err != nil {
			t.Fatalf("%s must still parse: %v", name, err)
		}
	}
}

// Composition (ADR-0030) tested in its own package: a def calling another, the callee's
// scope, and the cycle guard.
func TestCompose_InLang(t *testing.T) {
	src := `
def leaf(p: str) { apply { shell { touch "$p" } return ok.done } }
def caller(x: str) { apply { leaf(x) return ok.done } }
def a() { apply { b() return ok.done } }
def b() { apply { a() return ok.done } }
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

	res, err := EvalDefFull(byName["caller"], map[string]string{"x": "/tmp/x"}, nil, nil, noopExec{}, engine.Apply, resolve, []string{"caller"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("a def must be able to call another: %v", err)
	}
	if res.Category != engine.OK {
		t.Fatalf("got %s", res.String())
	}

	// A cycle names its chain rather than blowing the stack.
	_, err = EvalDefFull(byName["a"], nil, nil, nil, noopExec{}, engine.Apply, resolve, []string{"a"}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a cycle must be refused with its chain: %v", err)
	}

	// With no resolver, a call is refused rather than silently skipped.
	_, err = EvalDefFull(byName["caller"], map[string]string{"x": "/tmp/x"}, nil, nil, noopExec{}, engine.Apply, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("no resolver must refuse the call")
	}
}

// ADR-0036 §1: the same primitive reads either side, and the argument decides. Getting
// this backwards delivers the wrong file with no error at all, so both directions are
// asserted.
func TestControl_ReadSideDependsOnTheArgument(t *testing.T) {
	asked := ""
	fetch := func(r string, _ []byte, _ map[string]string) ([]byte, error) {
		asked = r
		return []byte("from control"), nil
	}

	// Marked: goes through the channel.
	_, err := evalWithFetchControl(t, `def t(p: str) { apply { x = ~file.read(p) return ok.done } }`, "t",
		map[string]string{"p": "conf.j2"}, []string{"p"}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "file.read:conf.j2" {
		t.Fatalf("a marked path must go to the control host: %q", asked)
	}

	// Unmarked: must not touch the channel at all.
	asked = ""
	_, _ = evalWithFetchControl(t, `def t(p: str) { apply { x = ~file.read(p) return ok.done } }`, "t",
		map[string]string{"p": "/etc/motd"}, nil, fetch)
	if asked != "" {
		t.Fatalf("an unmarked path must be read on the target, not asked for: %q", asked)
	}
}

// The marker must survive the call boundary. Without it a def receives plain strings and
// reads the target — the opposite of what the plan asked.
func TestControl_MarkerCrossesTheCallBoundary(t *testing.T) {
	asked := ""
	fetch := func(r string, _ []byte, _ map[string]string) ([]byte, error) { asked = r; return []byte("x"), nil }

	src := `
def inner(p: str) { apply { x = ~file.read(p) return ok.read } }
def outer(p: str) { apply { inner(p) return ok.done } }
`
	_, err := evalWithFetchControl(t, src, "outer", map[string]string{"p": "conf.j2"},
		[]string{"p"}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "file.read:conf.j2" {
		t.Fatalf("the mark must reach a def called by a def: %q", asked)
	}
}

// ~file.write refuses a control-host path: there is no allow-list for writes, so a plan
// must not be able to write on the operator's machine (ADR-0036 §4).
func TestControl_WriteRefusesTheControlHost(t *testing.T) {
	_, err := evalWithFetchControl(t, `def t(p: str) { apply { ~file.write(p, "data") return ok.done } }`, "t",
		map[string]string{"p": "/tmp/x"}, []string{"p"},
		func(string, []byte, map[string]string) ([]byte, error) { return nil, nil })
	if err == nil {
		t.Fatal("writing on the control host must be refused")
	}
	if !strings.Contains(err.Error(), "control host") {
		t.Fatalf("the refusal must say why: %v", err)
	}
}

// #335: `~dir.sync` transfers *from* the control host, so its source must be marked.
// An unmarked path would name a directory on the target — a different operation nobody
// asked for — so it is refused rather than quietly done.
func TestControl_SyncRequiresAMarkedSource(t *testing.T) {
	src := `def d(s: str, dst: str) { apply { x = ~dir.sync(s, dst, "false", "meta") return ok.x } }`
	_, err := evalWithFetch(t, src, "d", map[string]string{"s": "tree", "dst": "/opt/x"}, nil)
	if err == nil {
		t.Fatal("an unmarked source must be refused")
	}
	if !strings.Contains(err.Error(), "control host") {
		t.Fatalf("the message must say where the source is read, got %v", err)
	}
}

// #390, ADR-0044 — the escalation must reach the transfer. It could not: a transfer wrote
// from the agent's own process, so a tree delivered inside `as root` landed owned by the
// connecting user while the run reported `ok.copied`. It now places files through a child
// launched by the executor, so what this asserts is that the executor handed to the
// transfer is the escalated one.
//
// This test replaces the interim refusal of ADR-0044 §6, which asserted the opposite.
func TestControl_SyncReceivesTheEscalatedExecutor(t *testing.T) {
	src := `def d(s: str, dst: str) as root { apply { x = ~dir.sync(s, dst, "false", "meta") return ok.x } }`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return nil, nil }

	var gotSync, gotPreview engine.Executor
	sync := func(ex engine.Executor, _, _, _ string, _ bool) (int, int, error) {
		gotSync = ex
		return 1, 0, nil
	}
	preview := func(ex engine.Executor, _, _, _ string) (int, []string, error) {
		gotPreview = ex
		return 1, nil, nil
	}

	for _, c := range []struct {
		mode engine.Mode
		got  *engine.Executor
	}{{engine.Apply, &gotSync}, {engine.Check, &gotPreview}} {
		if _, err := EvalDefFull(defs[0], map[string]string{"s": "tree", "dst": "/opt/x"}, nil,
			[]string{"s"}, engine.ShellExecutor{}, c.mode, nil, []string{"d"}, fetch, sync, preview); err != nil {
			t.Fatalf("%v: %v", c.mode, err)
		}
		se, ok := (*c.got).(engine.ShellExecutor)
		if !ok {
			t.Fatalf("%v: the transfer must receive the run's executor, got %T", c.mode, *c.got)
		}
		if se.Become != "root" {
			t.Fatalf("%v: the transfer must be told who it runs as, got %q", c.mode, se.Become)
		}
	}
}

// `compare` is a closed set: a typo must fail at the call, not silently fall back to a
// comparison the author did not choose.
func TestControl_SyncRejectsAnUnknownCompare(t *testing.T) {
	src := `def d(s: str, dst: str) { apply { x = ~dir.sync(s, dst, "false", "md5") return ok.x } }`
	noop := func(string, []byte, map[string]string) ([]byte, error) { return nil, nil }
	_, err := evalWithFetchControl(t, src, "d",
		map[string]string{"s": "tree", "dst": "/opt/x"}, []string{"s"}, noop)
	if err == nil {
		t.Fatal("an unknown compare must be refused")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("the message must name the accepted values, got %v", err)
	}
}

// Arity is checked before anything else: four arguments, or a message saying so.
func TestControl_SyncArity(t *testing.T) {
	src := `def d(s: str, dst: str) { apply { x = ~dir.sync(s, dst) return ok.x } }`
	_, err := evalWithFetchControl(t, src, "d",
		map[string]string{"s": "tree", "dst": "/opt/x"}, []string{"s"}, nil)
	if err == nil || !strings.Contains(err.Error(), "exactly 4") {
		t.Fatalf("wrong arity must be refused by count: %v", err)
	}
}

// #373: `~dir.sync` must be inert in check mode wherever it appears — not only inside a
// `preview` phase. A destructive primitive that acted during `--dry-run` because someone
// wrote it in an `apply` would be the worst kind of surprise, and no phase placement
// should be load-bearing for that.
func TestControl_SyncIsInertInCheckMode(t *testing.T) {
	// Both phases hold the primitive, and both reach it in check mode: `preview` runs
	// there by definition, and since ADR-0041 so does an `apply` that cannot act — which
	// this one is. That is the point. Inertness is a property of the primitive, not of
	// where it was written, so a change to which phases run in which mode must not be able
	// to turn a `--dry-run` into a deletion.
	src := `def d(s: str, dst: str) {
		preview { ~dir.sync(s, dst, "true", "meta") }
		apply { x = ~dir.sync(s, dst, "true", "meta") return ok.x }
	}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(string) (Def, bool) { return Def{}, false }

	var synced, previewed bool
	sync := func(engine.Executor, string, string, string, bool) (int, int, error) { synced = true; return 1, 0, nil }
	preview := func(engine.Executor, string, string, string) (int, []string, error) {
		previewed = true
		return 1, []string{"gone"}, nil
	}

	args := map[string]string{"s": "tree", "dst": "/opt/x"}
	// A non-nil fetcher: the agent supplies all three together, and the generic
	// "no control host channel" guard tests that one.
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return nil, nil }
	if _, err := EvalDefFull(defs[0], args, nil, []string{"s"}, noopExec{}, engine.Check,
		resolve, []string{"d"}, fetch, sync, preview); err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("check mode must never reach the acting transfer")
	}
	if !previewed {
		t.Fatal("check mode must ask what the transfer would do")
	}

	// And apply does act, or the inertness would be indistinguishable from doing nothing.
	synced, previewed = false, false
	if _, err := EvalDefFull(defs[0], args, nil, []string{"s"}, noopExec{}, engine.Apply,
		resolve, []string{"d"}, fetch, sync, preview); err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("apply must perform the transfer")
	}
}

// #411: bytes handed to an instruction that declares `str` used to be flattened to "" by
// stringify, so `file.write(dst, ~file.read(src))` wrote an **empty file** and reported
// `ok.done`. Measured, not deduced: the destination was 0 bytes with a "hello-bytes"
// source.
//
// The refusal already existed one call away — interpolating bytes into a string is
// refused by name (ADR-0034 §4) — and simply was not applied at the argument boundary.
func TestControl_BytesRefusedAsAStringArgument(t *testing.T) {
	src := `
def sink(content: str) { apply { shell { printf '%s' "$content" } return ok.done } }
def caller(p: str) { apply { sink(~file.read(p)) return ok.done } }
`
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return []byte("hello-bytes"), nil }
	_, err := evalWithFetchControl(t, src, "caller", map[string]string{"p": "f.txt"}, []string{"p"}, fetch)
	if err == nil {
		t.Fatal("bytes passed where a string is expected must fail, not become an empty string")
	}
	// The message has to say what to write instead: an author reaching for this wanted to
	// deliver a file, which is `file.copy`.
	if !strings.Contains(err.Error(), "file.copy") {
		t.Fatalf("the refusal must name what to use instead: %v", err)
	}
	if !strings.Contains(err.Error(), "content") {
		t.Fatalf("the refusal must name the parameter it refused: %v", err)
	}
}

// The same flattening reaches `~dir.sync`'s own arguments, where an empty destination is
// nonsense rather than a silent truncation — refused for the same reason (#411).
func TestControl_SyncRefusesBytesArguments(t *testing.T) {
	defs, err := ParseDefs(`def d(s: str, p: str) { apply { x = ~dir.sync(s, ~file.read(p), "false", "meta") return ok.x } }`)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return []byte("/tmp/x"), nil }
	// A working channel, so the refusal cannot be "no control host channel" — the failure
	// this asserts must come from the argument itself.
	sync := func(engine.Executor, string, string, string, bool) (int, int, error) {
		t.Fatal("a transfer must not start with bytes for a destination")
		return 0, 0, nil
	}
	preview := func(engine.Executor, string, string, string) (int, []string, error) { return 0, nil, nil }

	_, err = EvalDefFull(defs[0], map[string]string{"s": "tree", "p": "f.txt"}, nil,
		[]string{"s", "p"}, noopExec{}, engine.Apply, nil, []string{"d"}, fetch, sync, preview)
	if err == nil {
		t.Fatal("bytes as a destination must fail rather than resolve to an empty path")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("the refusal must name what was wrong: %v", err)
	}
}
