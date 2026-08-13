package lang

import "sort"

// ControlResources lists what a set of defs will ask the control host for (ADR-0034 §5).
//
// The occurrences are syntactic, which is what makes ADR-0031's allow-list possible: the
// control host derives the set before sending, and refuses anything outside it. A `%`
// path built from a value the target produces is therefore not resolvable here — and is
// refused when the plan is read rather than mid-deploy.
func ControlResources(defs map[string]Def) []string {
	seen := map[string]bool{}
	for _, d := range defs {
		for _, ph := range d.Phases {
			scanStmts(ph.Stmts, seen)
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func scanStmts(stmts []Stmt, seen map[string]bool) {
	for _, st := range stmts {
		switch t := st.(type) {
		case IfStmt:
			scanExpr(t.Cond, seen)
			scanStmts(t.Body, seen)
		case LetStmt:
			scanExpr(t.Value, seen)
		case EffectStmt:
			scanExpr(t.Expr, seen)
		case StateReturnStmt:
			for _, f := range t.Fields {
				scanExpr(f.Value, seen)
			}
		}
	}
}

func scanExpr(e Expr, seen map[string]bool) {
	switch x := e.(type) {
	case Call:
		if x.Control && len(x.Args) == 1 {
			// Only a literal path is knowable before the run; anything computed is
			// left out, and the request it makes will be refused with its name.
			if p, ok := x.Args[0].(ControlPath); ok {
				seen[resourceKey(x.Name, p.Value)] = true
			}
			if s, ok := x.Args[0].(StrLit); ok {
				seen[resourceKey(x.Name, s.Value)] = true
			}
		}
		for _, a := range x.Args {
			scanExpr(a, seen)
		}
	case Binary:
		scanExpr(x.L, seen)
		scanExpr(x.R, seen)
	case Unary:
		scanExpr(x.X, seen)
	case Field:
		scanExpr(x.Recv, seen)
	}
}
