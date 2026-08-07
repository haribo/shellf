package lang

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"shellf/internal/engine"
)

// EvalDef runs a parsed def as an instruction, reproducing the engine semantics:
// pre-check/check/guard first (any outcome returns), then `would` in Check mode,
// then apply/post, then the default return. Shell variables are the def's
// params (and string/bool lets), injected via the environment.
//
// The returned error is an evaluation failure (unbound var, unsupported
// construct) — distinct from an `err.*` Result, which is a normal outcome.
func EvalDef(def Def, args, with map[string]string, ex engine.Executor, mode engine.Mode) (res engine.Result, err error) {
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
	ev := &evaluator{ex: ex.As(def.Become).Using(def.Interp), vars: map[string]value{}}
	for k, v := range args {
		ev.vars[k] = v
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

	// Status: report observed vs desired without acting (ADR-0013). A pre-check
	// error still surfaces; otherwise observe drives the field report.
	if mode == engine.Status {
		for _, ph := range def.Phases {
			if ph.Name == "pre-check" {
				if o := ev.evalPhase(ph); o != nil {
					return ev.toResult(*o), nil
				}
			}
		}
		return ev.statusResult(def, desired), nil
	}

	// Pass 1: read-only decision phases. A `pre-check`/`check` outcome wins (err →
	// halt, or a question's ok/err). An `observe` phase reports current state;
	// convergence (state == desired) is the derived skip (ADR-0013).
	for _, ph := range def.Phases {
		switch ph.Name {
		case "pre-check", "check":
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

	// Pass 2: effectful phases. A trailing `return` in apply is the nominal
	// outcome (evalPhase reaches it); running to the end with no return yields
	// an implicit tag-less `ok` (ADR-0007).
	for _, ph := range def.Phases {
		if ph.Name == "apply" || ph.Name == "post" {
			if o := ev.evalPhase(ph); o != nil {
				return changedIfOK(ev.toResult(*o)), nil
			}
		}
	}
	return changedIfOK(engine.Ok("")), nil
}

// changedIfOK marks a Result Changed when it comes from a run apply (not a
// guard skip) and did not err.
func changedIfOK(r engine.Result) engine.Result {
	if r.Category == engine.OK {
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

type value interface{} // string | int | bool | engine.ShellResult

type evalErr struct{ err error }

type evaluator struct {
	ex   engine.Executor
	vars map[string]value
	last value // last evaluated shell result, for the `when ok`/`when err` shorthand
}

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

// stringify renders a scalar value for the diff / the shell environment.
func stringify(v value) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
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
		if sr, ok := ev.last.(engine.ShellResult); ok {
			if t := strings.TrimRight(sr.Stdout, " \t\r\n"); t != "" {
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
		return x.Value
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
		res := ev.ex.Using(x.Interp).Shell(x.Cmd, ev.shellEnv())
		ev.last = res
		return res
	case Call:
		ev.fail("instruction calls are not supported yet: %q", x.Name)
	}
	ev.fail("unevaluable expression %T", e)
	return nil
}

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

// shellEnv exposes string/bool vars to the shell via the environment (injection-safe).
func (ev *evaluator) shellEnv() engine.Env {
	env := engine.Env{}
	for k, v := range ev.vars {
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
	}
	return false
}

func equal(a, b value) bool { return a == b }
