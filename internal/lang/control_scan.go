package lang

import (
	"sort"

	"shellf/internal/proto"
)

// ControlResources lists what a set of defs will ask the control host for (ADR-0034 §5).
//
// The occurrences are syntactic, which is what makes ADR-0031's allow-list possible: the
// control host derives the set before sending, and refuses anything outside it. A `%`
// path built from a value the target produces is therefore not resolvable here — and is
// refused when the plan is read rather than mid-deploy.
func ControlResources(defs map[string]Def, steps []proto.Step) []string {
	seen := map[string]bool{}
	scanSteps(steps, seen)
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

// scanSteps collects the `%"path"` arguments a plan wrote. This is the case that
// matters in practice: `file.template(%"conf.j2", dst)` puts the path at the call site,
// while the def reading it only sees a parameter — so scanning defs alone would miss it
// and the request would be refused at runtime for a file the plan legitimately needs.
//
// Every read primitive is allowed for such a path: the plan says "this is mine to serve",
// and which one reads it is the def's business — `file.template` reads it, `dir.copy`
// syncs it, and both are the same declaration seen from the plan.
func scanSteps(steps []proto.Step, seen map[string]bool) {
	for _, s := range steps {
		for _, arg := range s.Control {
			if p, ok := s.Args[arg]; ok {
				seen[resourceKey("file.read", p)] = true
				seen[resourceKey("file.render", p)] = true
				seen[resourceKey("dir.list", p)] = true
				seen[resourceKey("dir.sync", p)] = true
			}
		}
		scanSteps(s.Block, seen)
		scanSteps(s.Parallel, seen)
		if s.If != nil {
			if s.If.Cond != nil {
				scanSteps([]proto.Step{*s.If.Cond}, seen)
			}
			scanSteps(s.If.Then, seen)
			scanSteps(s.If.Else, seen)
		}
	}
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

// UsesPrimitive reports whether any def calls the named primitive, wherever the call
// sits. It existed for `~file.render`, which used to need the channel with no `%"…"`
// path declared, since the content came from the target; that stopped being true in #392
// and the question — does this set of defs reach for this primitive — is general enough
// to keep.
func UsesPrimitive(defs map[string]Def, name string) bool {
	found := false
	var walkE func(Expr)
	var walkS func([]Stmt)
	walkE = func(e Expr) {
		switch x := e.(type) {
		case Call:
			if x.Control && x.Name == name {
				found = true
			}
			for _, a := range x.Args {
				walkE(a)
			}
		case Binary:
			walkE(x.L)
			walkE(x.R)
		case Unary:
			walkE(x.X)
		case Field:
			walkE(x.Recv)
		}
	}
	walkS = func(stmts []Stmt) {
		for _, st := range stmts {
			switch t := st.(type) {
			case LetStmt:
				walkE(t.Value)
			case EffectStmt:
				walkE(t.Expr)
			case IfStmt:
				walkE(t.Cond)
				walkS(t.Body)
			case StateReturnStmt:
				for _, f := range t.Fields {
					walkE(f.Value)
				}
			}
		}
	}
	for _, d := range defs {
		for _, ph := range d.Phases {
			walkS(ph.Stmts)
		}
	}
	return found
}
