package lang

import (
	"fmt"
	"sort"
	"strings"
)

// Call-cycle detection at load time (ADR-0030 §6). A cycle is a writing error, like a
// syntax error: it must surface from reading the files, before anything runs on a
// target. The evaluator keeps its own guard as a backstop — it is cheap, and it covers a
// def reached by a path this walk cannot enumerate — but by then the plan is already on
// the host and earlier steps have acted, which is a partially applied machine (#311).

// Callees lists the instructions a def calls, in the order they appear: every `Call` in
// an expression position, anywhere in any phase, plus the def's delegation (ADR-0037 §2),
// which is a call like any other and would otherwise be an edge the graph does not know.
//
// A primitive (`~file.read`) is not a callee: it is an engine call with no body to
// recurse into.
func Callees(d Def) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, ph := range d.Phases {
		callsInStmts(ph.Stmts, add)
	}
	if d.Delegate != nil && !d.Delegate.Control {
		add(d.Delegate.Name)
	}
	return out
}

func callsInStmts(stmts []Stmt, add func(string)) {
	for _, st := range stmts {
		switch t := st.(type) {
		case IfStmt:
			callsInExpr(t.Cond, add)
			callsInStmts(t.Body, add)
		case LetStmt:
			callsInExpr(t.Value, add)
		case EffectStmt:
			callsInExpr(t.Expr, add)
		case StateReturnStmt:
			for _, f := range t.Fields {
				callsInExpr(f.Value, add)
			}
		}
	}
}

func callsInExpr(e Expr, add func(string)) {
	switch x := e.(type) {
	case Call:
		if !x.Control { // `~name` is an engine primitive, not a def: no body to enter
			add(x.Name)
		}
		for _, a := range x.Args {
			callsInExpr(a, add)
		}
	case Binary:
		callsInExpr(x.L, add)
		callsInExpr(x.R, add)
	case Unary:
		callsInExpr(x.X, add)
	case Field:
		callsInExpr(x.Recv, add)
	}
}

// CheckCycles walks the call graph from every def reachable through resolve and returns
// the first cycle it finds, naming the whole chain.
//
// It takes a resolver rather than a map because the graph spans two sets no single
// package sees: user defs, parsed in this package, and the stdlib, which cannot be
// imported here (std imports lang). The caller owns the bridge — the same lookup order a
// run uses, so the check sees the graph the run will walk, `override def` included.
//
// The message is built the same way as the evaluator's guard (`a -> b -> a`) so a reader
// meeting one has met the other.
func CheckCycles(defs map[string]Def, resolve DefResolver) error {
	// Sorted, so a package with several cycles reports the same one every time. A
	// diagnostic that moves between runs is a diagnostic nobody trusts.
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)

	// done: a def whose subtree is proven acyclic. Without it a wide graph is walked
	// once per entry point, which is exponential on a diamond.
	done := map[string]bool{}
	for _, n := range names {
		onPath := map[string]bool{}
		if err := walk(n, resolve, onPath, done, nil); err != nil {
			return err
		}
	}
	return nil
}

func walk(name string, resolve DefResolver, onPath, done map[string]bool, chain []string) error {
	if done[name] {
		return nil
	}
	if onPath[name] {
		return fmt.Errorf("call cycle: %s -> %s", strings.Join(chain, " -> "), name)
	}
	d, ok := resolve(name)
	if !ok {
		// Not a def: a Go instruction, or a name that does not resolve. Neither is this
		// check's business — an unknown instruction is reported where it is called, with
		// the context to fix it.
		return nil
	}
	onPath[name] = true
	chain = append(chain, name)
	for _, callee := range Callees(d) {
		if err := walk(callee, resolve, onPath, done, chain); err != nil {
			return err
		}
	}
	onPath[name] = false
	done[name] = true
	return nil
}
