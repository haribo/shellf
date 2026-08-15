package lang

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"shellf/internal/engine"
)

// EvalDef runs a parsed def as an instruction, reproducing the engine semantics:
// check/check/guard first (any outcome returns), then `would` in Check mode,
// then apply/post, then the default return. Shell variables are the def's
// params (and string/bool lets), injected via the environment.
//
// The returned error is an evaluation failure (unbound var, unsupported
// construct) — distinct from an `err.*` Result, which is a normal outcome.
func EvalDef(def Def, args, with map[string]string, ex engine.Executor, mode engine.Mode) (engine.Result, error) {
	return EvalDefWith(def, args, with, ex, mode, nil, nil, nil)
}

// EvalDefWith is EvalDef with instruction calls enabled (ADR-0030): `resolve` looks a
// callee up, `stack` is the chain that led here so a cycle names its path. A nil
// resolver rejects any call, which is what a def evaluated in isolation (a unit test)
// wants.
func EvalDefWith(def Def, args, with map[string]string, ex engine.Executor, mode engine.Mode, resolve DefResolver, stack []string, fetch ControlFetcher) (res engine.Result, err error) {
	return EvalDefFull(def, args, with, nil, ex, mode, resolve, stack, fetch, nil, nil)
}

// EvalDefFull is EvalDefWith plus `control`: the names of arguments the caller wrote
// `%"…"`. Without it the marker dies at the call boundary — a def receives strings, so
// `deliver(%"conf.j2", dst)` would read on the target, which is the opposite of what
// the plan asked (#332).
func EvalDefFull(def Def, args, with map[string]string, control []string, ex engine.Executor, mode engine.Mode, resolve DefResolver, stack []string, fetch ControlFetcher, sync TreeSyncer, preview TreePreviewer) (res engine.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ee, ok := r.(evalErr); ok {
				err = ee.err
				return
			}
			panic(r)
		}
	}()

	// A def's own `as <user>` escalates all its shells; it wins over an enclosing
	// block's become (applied last). `As("")` is a no-op (ADR-0011).
	ev := &evaluator{
		ex: ex.As(def.Become).Using(def.Interp), vars: map[string]value{},
		resolve: resolve, mode: mode, stack: stack, fetch: fetch, sync: sync, preview: preview,
	}
	for k, v := range args {
		ev.vars[k] = v
	}
	// Arguments the plan marked `%"…"` keep that marking inside the def, so a primitive
	// reading them goes to the control host rather than the target.
	for _, name := range control {
		if v, ok := ev.vars[name].(string); ok {
			ev.vars[name] = controlPath(v)
		}
	}
	// Param defaults fill any argument the caller omitted (ADR-0013 intent params
	// like `ensure = "present"`), so shells and the diff see the effective value.
	for _, p := range def.Params {
		if _, ok := ev.vars[p.Name]; !ok && p.Default != nil {
			ev.vars[p.Name] = ev.evalExpr(p.Default)
		}
	}
	// A `with { }` binding overrides any same-named param/default for this call
	// (ADR-0022): the most local scope wins. It reaches the def's shells as env.
	for k, v := range with {
		ev.vars[k] = v
	}
	desired := ev.desiredState(def) // the effective arguments, keyed by name

	// A delegation: this def *is* its callee with rebound arguments (ADR-0037 §2). Its
	// own `check` runs first — argument validation belongs to the wrapper, and `check`
	// runs in every mode (ADR-0035) — then the callee runs whole, in this mode. Every
	// phase of the callee therefore applies: it decides convergence in `--dry-run`, where
	// an `apply` would have decided nothing.
	if def.Delegate != nil {
		for _, ph := range def.Phases { // only `check` can be here (parser-enforced)
			if o := ev.evalPhase(ph); o != nil {
				return ev.toResult(*o), nil
			}
		}
		res := ev.evalCall(*def.Delegate)
		r, ok := res.(engine.Result)
		if !ok {
			ev.fail("%s did not yield a verdict", def.Delegate.Name)
		}
		return r, nil
	}

	// Status: report observed vs desired without acting (ADR-0013). A check
	// error still surfaces; otherwise observe drives the field report.
	if mode == engine.Status {
		// `check` runs here too (ADR-0035): a def refusing its arguments must refuse
		// them in `status` as well, or `status` reports state for a call that could
		// never run.
		for _, ph := range def.Phases {
			if ph.Name == "check" {
				if o := ev.evalPhase(ph); o != nil {
					return ev.toResult(*o), nil
				}
			}
		}
		return ev.statusResult(def, desired), nil
	}

	// Pass 1: read-only decision phases. A `check` outcome wins (err →
	// halt, or a question's ok/err). An `observe` phase reports current state;
	// convergence (state == desired) is the derived skip (ADR-0013).
	for _, ph := range def.Phases {
		switch ph.Name {
		case "check":
			if o := ev.evalPhase(ph); o != nil {
				return ev.toResult(*o), nil
			}
		case "observe":
			if converged(ev.evalObserve(ph.Stmts), desired) {
				return engine.Ok("already"), nil // in sync → skip apply (not changed)
			}
			// drift → fall through: check yields `would`, apply runs
		}
	}

	if mode == engine.Check {
		r := engine.Would(retTag(def))
		r.Changed = true // it would act
		// A `preview` phase describes what apply would do, read-only, best-effort
		// (ADR-0029). It never gates convergence; a failing preview yields none.
		for _, ph := range def.Phases {
			if ph.Name == "preview" {
				r.Preview = ev.evalPreview(ph)
			}
		}
		return r, nil
	}

	// Only the effectful pass can make this def "changed". Shells run in `check` or
	// `observe` are reads — counting them would mark every def with a decision phase as
	// having acted, firing every `if x.changed { … }` on a converged run (#328).
	ev.acted = false

	// Pass 2: effectful phases. A trailing `return` in apply is the nominal
	// outcome (evalPhase reaches it); running to the end with no return yields
	// an implicit tag-less `ok` (ADR-0007).
	for _, ph := range def.Phases {
		if ph.Name == "apply" {
			if o := ev.evalPhase(ph); o != nil {
				return ev.changedIfActed(ev.toResult(*o)), nil
			}
		}
	}
	return ev.changedIfActed(engine.Ok("")), nil
}

// changedIfActed marks a Result Changed when a run apply did something and did not
// err. "Did something" is a shell that ran, or a callee that itself reported changed
// (ADR-0030 §3) — a def whose apply only calls an already-converged instruction has
// changed nothing, and saying otherwise would fire every `if x.changed { … }`
// downstream for nothing.
func (ev *evaluator) changedIfActed(r engine.Result) engine.Result {
	if r.Category == engine.OK && ev.acted {
		r.Changed = true
	}
	return r
}

func retTag(def Def) string {
	if def.Return != nil {
		return def.Return.Tag
	}
	return ""
}

// --- evaluator ---

// Bytes is content read from the control host (ADR-0034 §4). Opaque on purpose: it can
// be handed to an instruction and nothing else. Interpolating it, comparing it, or
// printing it would treat binary as text, which is how a delivered image gets corrupted
// silently.
type Bytes []byte

type value interface{} // string | int | bool | Bytes | engine.ShellResult | engine.Result

type evalErr struct{ err error }

type evaluator struct {
	ex   engine.Executor
	vars map[string]value
	last value // last evaluated shell result, for the `when ok`/`when err` shorthand

	// Set when this def may call others (ADR-0030). resolve is supplied by the
	// caller so lang stays free of a std import; mode is carried so a callee is
	// evaluated in the caller's mode, which is what keeps `--check` inert. stack
	// is the call chain, for a readable cycle error.
	resolve DefResolver
	fetch   ControlFetcher
	sync    TreeSyncer
	preview TreePreviewer
	mode    engine.Mode
	stack   []string
	acted   bool // a shell ran, or a callee reported changed (ADR-0030 §3)
}

// DefResolver resolves an instruction name to its def. The agent supplies it (user
// defs first, then the stdlib), so `lang` needs no import of `std`.
type DefResolver func(name string) (Def, bool)

// ControlFetcher answers a `%` primitive by asking the control host (ADR-0031). The
// agent supplies it; nil means no channel, and any `%` then fails naming the resource
// rather than silently returning nothing.
// vars carries the caller's scope for a primitive that substitutes; it is nil for one
// that only names a path.
type ControlFetcher func(resource string, payload []byte, vars map[string]string) ([]byte, error)

// TreeSyncer drives a tree transfer and returns how many files were written (ADR-0039).
// Separate from ControlFetcher because the answer is a sequence, not one payload: the
// agent writes files as they arrive rather than holding a tree in memory to return it.
type TreeSyncer func(resource, dst, compare string, del bool) (int, error)

// TreePreviewer answers what a transfer would do without doing it: how many files it
// would write, and which it would remove (#373).
type TreePreviewer func(resource, dst, compare string) (int, []string, error)

func (ev *evaluator) fail(format string, a ...any) {
	panic(evalErr{fmt.Errorf(format, a...)})
}

// desiredState is the effective desired value of each parameter, as a string map
// keyed by param name (defaults already applied into ev.vars). It is what an
// `observe` field is diffed against (ADR-0013).
func (ev *evaluator) desiredState(def Def) map[string]string {
	d := map[string]string{}
	for _, p := range def.Params {
		if v, ok := ev.vars[p.Name]; ok {
			d[p.Name] = stringify(v)
		}
	}
	return d
}

// evalObserve runs an `observe` phase and returns its `state(...)` record as a
// string map (field → observed value, trailing whitespace trimmed since shell
// stdout carries a newline). Read-only by convention (ADR-0013).
func (ev *evaluator) evalObserve(stmts []Stmt) map[string]string {
	for _, s := range stmts {
		switch st := s.(type) {
		case LetStmt:
			ev.vars[st.Name] = ev.evalExpr(st.Value)
		case EffectStmt:
			ev.evalExpr(st.Expr)
		case IfStmt:
			if truthy(ev.evalExpr(st.Cond)) {
				if m := ev.evalObserve(st.Body); m != nil {
					return m
				}
			}
		case StateReturnStmt:
			m := make(map[string]string, len(st.Fields))
			for _, f := range st.Fields {
				m[f.Name] = strings.TrimRight(stringify(ev.evalExpr(f.Value)), " \t\r\n")
			}
			return m
		}
	}
	return nil
}

// statusResult builds the observed-vs-desired report of a resource (its observe
// fields), or marks an action-shaped def (no observe) as having no state.
func (ev *evaluator) statusResult(def Def, desired map[string]string) engine.Result {
	for _, ph := range def.Phases {
		if ph.Name == "observe" {
			fields := diffFields(ev.evalObserve(ph.Stmts), desired)
			cat := engine.OK
			for _, f := range fields {
				if !f.Converged {
					cat = engine.WOULD // drift → it would change on apply
					break
				}
			}
			return engine.Result{Category: cat, Fields: fields}
		}
	}
	return engine.Result{Category: engine.OK, Tag: "action"} // no observable state
}

// diffFields pairs each observed field with its desired value, using the same
// three rules as converged (truthy assertion / don't-care / value match).
func diffFields(observed, desired map[string]string) []engine.FieldDiff {
	names := make([]string, 0, len(observed))
	for name := range observed {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]engine.FieldDiff, 0, len(names))
	for _, name := range names {
		cur := observed[name]
		switch want, ok := desired[name]; {
		case !ok: // no parameter → the condition must hold
			out = append(out, engine.FieldDiff{Name: name, Current: cur, Desired: "true", Converged: truthyStr(cur)})
		case want == "": // don't care
			out = append(out, engine.FieldDiff{Name: name, Current: cur, Desired: cur, Converged: true})
		default:
			out = append(out, engine.FieldDiff{Name: name, Current: cur, Desired: want, Converged: cur == want})
		}
	}
	return out
}

// converged reports whether the observed state is in sync with the desired
// arguments (ADR-0013). A field with no same-named parameter is an assertion
// that must hold (truthy); a parameter present but empty is "don't care"; a
// parameter present and non-empty must equal the observed value.
func converged(observed, desired map[string]string) bool {
	for field, got := range observed {
		want, ok := desired[field]
		switch {
		case !ok:
			if !truthyStr(got) { // no parameter → the condition must hold
				return false
			}
		case want == "": // don't care
		default:
			if got != want {
				return false
			}
		}
	}
	return true
}

// truthyStr reads an observed field as a satisfied condition: non-empty, and not
// a false-ish value (a `.ok` bool renders as "true"/"false").
func truthyStr(s string) bool {
	return s != "" && s != "false" && s != "0"
}

// stringify renders a scalar value for the diff / the shell environment. Bytes are not
// scalars and never reach here: callers reject them first, so binary content cannot be
// silently mangled into a string.
func stringify(v value) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case controlPath:
		return string(t)
	case engine.ShellResult:
		return strings.TrimRight(t.Stdout, " \t\r\n") // a bare shell field → its output
	}
	return ""
}

// evalPreview runs a `preview` phase's shells (read-only, check mode) and returns
// their combined stdout as informational text. Best-effort: a shell that fails or
// is absent just contributes nothing (ADR-0029). It never returns an outcome.
func (ev *evaluator) evalPreview(ph Phase) string {
	var out []string
	for _, s := range ph.Stmts {
		ev.evalStmt(s)
		switch v := ev.last.(type) {
		case engine.ShellResult:
			if t := strings.TrimRight(v.Stdout, " \t\r\n"); t != "" {
				out = append(out, t)
			}
		case string:
			// A primitive can describe what it would do too. Collecting only shell stdout
			// made that impossible, so a destructive primitive had no way to say what it
			// would remove — which is the whole point of previewing one (#373).
			if t := strings.TrimRight(v, " \t\r\n"); t != "" {
				out = append(out, t)
			}
		}
	}
	return strings.Join(out, "\n")
}

func (ev *evaluator) evalPhase(ph Phase) *Outcome {
	for _, s := range ph.Stmts {
		if o := ev.evalStmt(s); o != nil {
			return o
		}
	}
	return nil
}

// evalStmt returns a non-nil outcome when the statement short-circuits the def
// (a `return`, or an `if` whose body returns).
func (ev *evaluator) evalStmt(s Stmt) *Outcome {
	switch st := s.(type) {
	case LetStmt:
		ev.vars[st.Name] = ev.evalExpr(st.Value)
		return nil
	case EffectStmt:
		ev.evalExpr(st.Expr) // side effect (e.g. a shell run), updates ev.last
		return nil
	case ReturnStmt:
		return &st.Outcome
	case IfStmt:
		if truthy(ev.evalExpr(st.Cond)) {
			for _, b := range st.Body {
				if o := ev.evalStmt(b); o != nil {
					return o
				}
			}
		}
		return nil
	}
	return nil
}

func (ev *evaluator) evalExpr(e Expr) value {
	switch x := e.(type) {
	case StrLit:
		return ev.interpolate(x.Value)

	case BoolLit:
		return x.Value
	case IntLit:
		n, err := strconv.Atoi(x.Raw)
		if err != nil {
			ev.fail("bad integer %q", x.Raw)
		}
		return n
	case Ident:
		switch x.Name {
		case "ok":
			return ev.lastOK()
		case "err":
			return !ev.lastOK()
		}
		v, ok := ev.vars[x.Name]
		if !ok {
			ev.fail("unbound variable %q", x.Name)
		}
		return v
	case Field:
		return ev.evalField(x)
	case Binary:
		eq := equal(ev.evalExpr(x.L), ev.evalExpr(x.R))
		if x.Op == "==" {
			return eq
		}
		return !eq
	case Unary: // `!x` — negate truthiness (ADR-0010)
		return !truthy(ev.evalExpr(x.X))
	case ShellExpr:
		// A per-block `shell(<interp>)` overrides the def-declared interpreter.
		ev.acted = true
		res := ev.ex.Using(x.Interp).Shell(x.Cmd, ev.shellEnv())
		ev.last = res
		return res
	case ControlPath:
		// A control-host path is data until a primitive reads it: `%"conf.j2"` names a
		// file on the operator's machine, and only `~file.read` turns it into content.
		return controlPath(ev.interpolate(x.Value))
	case Call:
		if x.Control {
			// A primitive's value is the last thing evaluated, like a shell's or a def
			// call's. Without this a `preview { ~dir.sync(…) }` collected nothing and
			// announced a deletion it could not describe (#373).
			v := ev.evalControlCall(x)
			ev.last = v
			return v
		}
		return ev.evalCall(x)
	}
	ev.fail("unevaluable expression %T", e)
	return nil
}

// evalCall runs another instruction from inside this def (ADR-0030).
//
//   - scope: the callee sees its own parameters only, never the caller's lets, so a
//     def means the same thing wherever it is called from (§1);
//   - escalation: it inherits this def's executor, and its own intrinsic `as` wins
//     inside EvalDefWith (§2);
//   - mode: it is evaluated in the caller's mode, so nothing effectful runs in
//     `--check` (§5);
//   - errors: an `err` halts the caller, like any failing step (§4);
//   - cycles: the chain is carried so a repeat names its path (§6).
func (ev *evaluator) evalCall(c Call) value {
	if ev.resolve == nil {
		ev.fail("instruction calls are not available here: %q", c.Name)
	}
	for _, seen := range ev.stack {
		if seen == c.Name {
			ev.fail("call cycle: %s -> %s", strings.Join(ev.stack, " -> "), c.Name)
		}
	}
	def, ok := ev.resolve(c.Name)
	if !ok {
		ev.fail("unknown instruction %q", c.Name)
	}
	if len(c.Args) > len(def.Params) {
		ev.fail("%s takes %d argument(s), got %d", c.Name, len(def.Params), len(c.Args))
	}
	// Positional arguments, evaluated in THIS def's scope, then handed over as the
	// callee's own params. Nothing else of this def crosses over.
	args := map[string]string{}
	var control []string
	for i, a := range c.Args {
		name := def.Params[i].Name
		v := ev.evalExpr(a)
		args[name] = stringify(v)
		// A control-host path stays one across the call, or the callee reads the target
		// while the plan asked for the operator's machine (#332).
		if _, isControl := v.(controlPath); isControl {
			control = append(control, name)
		}
	}
	res, err := EvalDefFull(def, args, nil, control, ev.ex, ev.mode, ev.resolve, append(ev.stack, c.Name), ev.fetch, ev.sync, ev.preview)
	if err != nil {
		ev.fail("%s: %v", c.Name, err)
	}
	if res.Changed {
		ev.acted = true
	}
	if res.Category == engine.ERR {
		ev.fail("%s returned %s", c.Name, res.String())
	}
	ev.last = res
	return res
}

// interpolate resolves `${name}` against this def's scope — its params, its lets. A
// plan interpolates at parse time against globals; inside a def the values are only
// known at evaluation, so it happens here. Same sigil, same meaning, so an author does
// not have to know which side of the fence they are on (#296).
func (ev *evaluator) interpolate(raw string) string {
	out, err := Interpolate(raw, func(n string) (string, bool) {
		v, ok := ev.vars[n]
		if !ok {
			return "", false
		}
		if b, isBytes := v.(Bytes); isBytes {
			_ = b
			ev.fail("%s holds control-host bytes, which cannot be interpolated into a string (ADR-0034)", n)
		}
		return stringify(v), true
	})
	if err != nil {
		ev.fail("%v", err)
	}
	return out
}

// controlPath is the value a `%"…"` yields: a path on the control host, distinct from
// an ordinary string so a primitive can require one and an instruction cannot be handed
// one by mistake.
type controlPath string

// evalControlCall runs a `%` primitive by asking the control host (ADR-0034 §3, using
// the channel of ADR-0031). It reads; it never acts.
func (ev *evaluator) evalControlCall(c Call) value {
	// Arity first: it is a property of the call, not of the environment. Checking the
	// channel before it answers "no control host" to a def written with the wrong number
	// of arguments, which sends the author looking at their transport.
	want := 1
	switch c.Name {
	case "file.write":
		want = 2
	case "dir.sync":
		want = 4
	}
	if len(c.Args) != want {
		ev.fail("~%s takes exactly %d argument(s), got %d", c.Name, want, len(c.Args))
	}
	if ev.fetch == nil {
		ev.fail("~%s: no control host channel available for this run", c.Name)
	}
	arg := ev.evalExpr(c.Args[0])

	// A transfer is not a request/response: it drives a sequence and writes as it goes.
	if c.Name == "dir.sync" {
		return ev.evalSync(c, arg)
	}

	switch c.Name {
	case "file.write":
		// Writes on the target, always (ADR-0036 §4): the allow-list bounds what a plan
		// may read from the operator's machine, and there is no equivalent for writes.
		path, ok := arg.(string)
		if !ok {
			if _, isControl := arg.(controlPath); isControl {
				ev.fail("~file.write cannot write on the control host (ADR-0036)")
			}
			ev.fail("~file.write expects a target path")
		}
		var content []byte
		switch t := ev.evalExpr(c.Args[1]).(type) {
		case Bytes:
			content = t
		case string:
			content = []byte(t)
		default:
			ev.fail("~file.write expects content")
		}
		res := engine.Run(engine.FilePut{
			Path: path, Content: base64.StdEncoding.EncodeToString(content),
		}, ev.ex, ev.mode)
		if res.Changed {
			ev.acted = true
		}
		if res.Category == engine.ERR {
			ev.fail("~file.write: %s", res.String())
		}
		return res

	case "file.read", "dir.list":
		// These name something on the control host, so a plain string is a mistake
		// worth catching: `~file.read("conf.j2")` reads a target path by accident,
		// `~file.read(%"conf.j2")` says what it means.
		switch t := arg.(type) {
		case controlPath:
			// Marked `%"…"`: the operator's machine, so it goes through the channel and
			// the allow-list.
			if ev.fetch == nil {
				ev.fail("~%s: no control host channel available for this run", c.Name)
			}
			b, err := ev.fetch(resourceKey(c.Name, string(t)), nil, nil)
			if err != nil {
				ev.fail("%v", err)
			}
			return Bytes(b)
		case string:
			// An ordinary path: the target, read through the shell. base64 so a binary
			// survives the trip — a raw read would stop at the first null byte.
			return Bytes(ev.readTarget(c.Name, t))
		default:
			ev.fail("~%s expects a path", c.Name)
		}
		return nil

	case "file.render":
		// Rendering happens on the control host: the host's variables live there and
		// never travel (ADR-0024, ADR-0018). The agent sends content and receives the
		// substituted result — which is what lets a template whose source is on the
		// *target* be rendered too: `~file.render(shell { cat … })`.
		var content string
		switch t := arg.(type) {
		case Bytes:
			content = string(t)
		case string:
			content = t
		case engine.ShellResult:
			content = t.Stdout
		default:
			ev.fail("~file.render expects content")
		}
		if ev.fetch == nil {
			ev.fail("~file.render: no control host channel available for this run")
		}
		// The scope travels with the content: a template names variables that live on
		// the control host (host vars, secrets) *and* variables that live here (the
		// def's params, a `with` override at the call site). Rendering with only one of
		// the two would fail on the other's names.
		out, err := ev.fetch("file.render:", []byte(content), ev.renderScope())
		if err != nil {
			ev.fail("%v", err)
		}
		return string(out)

	}
	ev.fail("~%s is not a primitive", c.Name)
	return nil
}

// evalSync drives a tree transfer (ADR-0039). Unlike the other primitives it is not a
// single request/response, so it goes through its own channel entry point rather than
// through fetch — the answer is a sequence, and the agent writes files as it arrives.
//
// `src` must be marked `%"…"`: a tree is transferred *from* the control host, and an
// unmarked path would name a directory on the target, which is a different operation
// nobody asked for.
func (ev *evaluator) evalSync(c Call, arg value) value {
	src, ok := arg.(controlPath)
	if !ok {
		ev.fail("~dir.sync reads its source on the control host: mark it %%\"…\"")
	}
	dst := stringify(ev.evalExpr(c.Args[1]))
	del := stringify(ev.evalExpr(c.Args[2]))
	compare := stringify(ev.evalExpr(c.Args[3]))
	switch compare {
	case "", "meta", "sha256":
	default:
		ev.fail("~dir.sync: compare must be \"meta\" or \"sha256\", got %q", compare)
	}
	if ev.sync == nil {
		ev.fail("~dir.sync: no control host channel available for this run")
	}
	// Inert in check mode, by construction rather than by where it is called: it asks for
	// the delta, receives the terminator alone and writes nothing. A destructive
	// primitive that acted in `--dry-run` because someone put it outside a `preview`
	// would be the worst kind of surprise (#373).
	if ev.mode == engine.Check {
		if ev.preview == nil {
			ev.fail("~dir.sync: no control host channel available for this run")
		}
		n, extras, err := ev.preview(resourceKey("dir.sync", string(src)), dst, compare)
		if err != nil {
			ev.fail("%v", err)
		}
		return syncSummary(n, extras, del == "true")
	}
	n, err := ev.sync(resourceKey("dir.sync", string(src)), dst, compare, del == "true")
	if err != nil {
		ev.fail("%v", err)
	}
	// A transfer that wrote nothing changed nothing: this is what makes a converged tree
	// report `already` instead of claiming an action (ADR-0039 §1).
	if n > 0 {
		ev.acted = true
	}
	return strconv.Itoa(n)
}

// syncSummary is what a transfer says it would do. Deletions are named one per line and
// not merely counted: "3 files would be removed" tells an operator nothing they can act
// on, and this is the text a `preview` phase puts in front of them before they say yes.
func syncSummary(written int, extras []string, del bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) would be transferred", written)
	if !del || len(extras) == 0 {
		if len(extras) > 0 {
			fmt.Fprintf(&b, "; %d extra file(s) on the target would be left alone", len(extras))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "; %d file(s) would be REMOVED from the target:", len(extras))
	for _, e := range extras {
		fmt.Fprintf(&b, "\n  - %s", e)
	}
	return b.String()
}

// readTarget reads a path on the target. Content comes back base64-encoded so a binary
// file survives: a shell variable stops at the first null byte, which would truncate an
// image without a word.
func (ev *evaluator) readTarget(primitive, path string) []byte {
	var cmd string
	switch primitive {
	case "file.read":
		cmd = `base64 < "$__p"`
	case "dir.list":
		cmd = `ls -A "$__p" | base64`
	}
	r := ev.ex.Shell(cmd, engine.Env{"__p": path})
	if !r.OK() {
		ev.fail("~%s(%s) on the target: %s", primitive, path, strings.TrimSpace(r.Stderr))
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(r.Stdout, "\n", ""))
	if err != nil {
		ev.fail("~%s(%s): unreadable content from the target", primitive, path)
	}
	return b
}

// resourceKey is what the control host is asked for. The primitive is part of the key
// so listing a directory cannot be answered with a file's contents.
func resourceKey(primitive, path string) string { return primitive + ":" + path }

func (ev *evaluator) evalField(f Field) value {
	recv := ev.evalExpr(f.Recv)
	sr, ok := recv.(engine.ShellResult)
	if !ok {
		ev.fail("field .%s on a non-shell value", f.Name)
	}
	switch f.Name {
	case "exit":
		return sr.Exit
	case "stdout":
		return sr.Stdout
	case "stderr":
		return sr.Stderr
	}
	// ADR-0010: a ShellResult is a product; success is `if r` / `r.exit == 0`, not `.ok`.
	ev.fail("no field .%s on a shell result; use `if r` / `if !r`, or .exit/.stdout/.stderr", f.Name)
	return nil
}

// renderScope is the caller's half of a template's namespace: the scalar variables in
// scope here, sent with a `~file.render` ask so the control host can layer them over
// the host environment.
//
// A `Bytes` var is skipped rather than refused, where shellEnv refuses: bytes are what a
// def holds while delivering a file (`~file.read` → `~file.write`), so failing would
// break the ordinary case. A template naming one gets "undefined variable", which says
// what to fix.
func (ev *evaluator) renderScope() map[string]string {
	scope := map[string]string{}
	for k, v := range ev.vars {
		switch t := v.(type) {
		case string:
			scope[k] = t
		case bool:
			scope[k] = strconv.FormatBool(t)
		case int:
			scope[k] = strconv.Itoa(t)
		}
	}
	return scope
}

// shellEnv exposes string/bool vars to the shell via the environment (injection-safe).
func (ev *evaluator) shellEnv() engine.Env {
	env := engine.Env{}
	for k, v := range ev.vars {
		if _, isBytes := v.(Bytes); isBytes {
			// Refuse rather than skip: silently omitting it makes `$k` an empty string
			// in the shell, so a plan delivering a binary would "succeed" and write
			// nothing. Bytes go to an instruction that takes them (ADR-0034 §4).
			ev.fail("%s holds control-host bytes; pass it to an instruction, it cannot be a shell variable", k)
		}
		switch t := v.(type) {
		case string:
			env[k] = t
		case bool:
			env[k] = strconv.FormatBool(t)
		case int:
			env[k] = strconv.Itoa(t)
		}
	}
	return env
}

func (ev *evaluator) lastOK() bool {
	sr, ok := ev.last.(engine.ShellResult)
	return ok && sr.OK()
}

func (ev *evaluator) toResult(o Outcome) engine.Result {
	var shell *engine.ShellResult
	if o.Payload != nil {
		if sr, ok := ev.evalExpr(o.Payload).(engine.ShellResult); ok {
			shell = &sr
		}
	}
	switch o.Category {
	case "ok":
		return engine.Result{Category: engine.OK, Tag: o.Tag, Shell: shell}
	case "err":
		return engine.Result{Category: engine.ERR, Tag: o.Tag, Shell: shell}
	case "would":
		return engine.Result{Category: engine.WOULD, Tag: o.Tag, Shell: shell}
	}
	ev.fail("unknown outcome category %q", o.Category)
	return engine.Result{}
}

// truthy backs `if x` / `if !x`: a bool is itself; a ShellResult is its success
// (exit 0). A Result is never truthy-tested here — plans match it (ADR-0010).
func truthy(v value) bool {
	switch t := v.(type) {
	case bool:
		return t
	case engine.ShellResult:
		return t.OK()
	case engine.Result:
		return t.Category == engine.OK
	}
	return false
}

func equal(a, b value) bool { return a == b }
