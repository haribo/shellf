package lang

import (
	"fmt"
	"strconv"

	"shellf/internal/engine"
)

// EvalDef runs a parsed def as an instruction, reproducing the engine semantics:
// pre-check/check/guard first (any outcome returns), then `would` in Check mode,
// then apply/post, then the default return. Shell variables are the def's
// params (and string/bool lets), injected via the environment.
//
// The returned error is an evaluation failure (unbound var, unsupported
// construct) — distinct from an `err.*` Result, which is a normal outcome.
func EvalDef(def Def, args map[string]string, ex engine.Executor, mode engine.Mode) (res engine.Result, err error) {
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

	// Pass 1: read-only decision phases. First outcome wins (err → halt, or
	// guard → skip — same control flow, the category carries the meaning).
	for _, ph := range def.Phases {
		if ph.Name == "pre-check" || ph.Name == "check" || ph.Name == "guard" {
			if o := ev.evalPhase(ph); o != nil {
				return ev.toResult(*o), nil
			}
		}
	}

	if mode == engine.Check {
		r := engine.Would(retTag(def))
		r.Changed = true // it would act
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
