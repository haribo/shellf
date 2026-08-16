package lang

// ADR-0041: an `apply` whose only effects are primitives can be *evaluated* in check mode,
// because every primitive is inert there — `~file.read`, `~dir.list` and `~file.render`
// read, and `~file.write` / `~dir.sync` go through engine.Run, which guards and reports
// without applying. Evaluating it turns the primitive's own answer into the verdict
// instead of announcing a write that would not happen (#380).
//
// The alternative was to give such a def an `observe` that re-decides what the primitive
// already knows. That is precisely the duplication #378 was lost over, so the test here is
// about what an apply *can do*, never about restating what it does.

// inertApply returns the def's `apply` phase when check mode cannot make it act: the def
// declares no `observe` (with one, convergence is already decided), and the phase contains
// no `shell { }` and no call to another def.
//
// A def call is disqualifying rather than recursed into: resolving it needs the def table,
// and a callee with a shell three levels down would make this answer wrong. Refusing to
// answer costs precision in `--dry-run`; answering wrongly costs a write announced as
// nothing, or nothing announced as a write.
func inertApply(def Def) (Phase, bool) {
	var apply Phase
	found := false
	for _, ph := range def.Phases {
		switch ph.Name {
		case "observe":
			return Phase{}, false
		case "apply":
			apply, found = ph, true
		}
	}
	if !found || !inertStmts(apply.Stmts) {
		return Phase{}, false
	}
	return apply, true
}

func inertStmts(stmts []Stmt) bool {
	for _, s := range stmts {
		switch t := s.(type) {
		case LetStmt:
			if !inertExpr(t.Value) {
				return false
			}
		case EffectStmt:
			if !inertExpr(t.Expr) {
				return false
			}
		case IfStmt:
			if !inertExpr(t.Cond) || !inertStmts(t.Body) {
				return false
			}
		case ReturnStmt:
			if t.Outcome.Payload != nil && !inertExpr(t.Outcome.Payload) {
				return false
			}
		case StateReturnStmt:
			for _, f := range t.Fields {
				if !inertExpr(f.Value) {
					return false
				}
			}
		default:
			return false // an unknown statement is not assumed harmless
		}
	}
	return true
}

func inertExpr(e Expr) bool {
	switch t := e.(type) {
	case ShellExpr:
		return false // it can do anything, in any mode
	case Call:
		if !t.Control {
			return false // a def call: see inertApply's note
		}
		for _, a := range t.Args {
			if !inertExpr(a) {
				return false
			}
		}
		return true
	case Binary:
		return inertExpr(t.L) && inertExpr(t.R)
	case Unary:
		return inertExpr(t.X)
	case Field:
		return inertExpr(t.Recv)
	case StrLit, ControlPath, BoolLit, IntLit, Ident, nil:
		return true
	default:
		return false
	}
}
