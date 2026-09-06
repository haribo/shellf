package lang

import (
	"fmt"

	"shellf/internal/engine"
	"shellf/internal/proto"
)

// ADR-0056: a `check` that cannot touch anything is evaluated while the plan is read, so a
// def refusing its arguments refuses them before a host is contacted. Until then the
// refusal needed a reachable machine — measured on two unreachable hosts, a wrong *type*
// was reported in 5 ms with its position while a wrong *shape* took 10 s and said nothing
// about the argument at all.
//
// This is an addition, never a replacement: the evaluator keeps running `check` on the
// target, for the calls this pass cannot decide (§5).

// pureExpr reports whether an expression reaches nothing: no host, no file, no shell.
//
// Deliberately narrower than inertExpr, which answers "can this act in check mode" — every
// primitive passes that one, including `~file.read`, which reads a target. Here only the
// text primitives are allowed through, because they are pure by construction (ADR-0055 §1).
func pureExpr(e Expr) bool {
	switch t := e.(type) {
	case Call:
		// A def call is disqualifying rather than recursed into: resolving it needs the
		// def table, and a callee with a shell three levels down would make this answer
		// wrong. Same trade as inertApply (ADR-0041).
		if !t.Control || !purePrimitives[t.Name] {
			return false
		}
		for _, a := range t.Args {
			if !pureExpr(a) {
				return false
			}
		}
		return true
	case Binary:
		return pureExpr(t.L) && pureExpr(t.R)
	case Unary:
		return pureExpr(t.X)
	case Field:
		return pureExpr(t.Recv)
	case StrLit, BoolLit, IntLit, Ident:
		return true
	default:
		// ShellExpr, ControlPath and anything added later: not assumed harmless.
		return false
	}
}

// purePrimitives is the subset of ControlPrimitives that reaches nothing (ADR-0056 §2).
var purePrimitives = map[string]bool{
	"text.matches": true,
	"text.replace": true,
}

func pureStmts(stmts []Stmt) bool {
	for _, s := range stmts {
		switch t := s.(type) {
		case LetStmt:
			if !pureExpr(t.Value) {
				return false
			}
		case EffectStmt:
			if !pureExpr(t.Expr) {
				return false
			}
		case IfStmt:
			if !pureExpr(t.Cond) || !pureStmts(t.Body) {
				return false
			}
		case ReturnStmt:
			if t.Outcome.Payload != nil && !pureExpr(t.Outcome.Payload) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// pureCheck returns the def's `check` phase when every statement in it is pure.
func pureCheck(def Def) (Phase, bool) {
	for _, ph := range def.Phases {
		if ph.Name == "check" {
			if !pureStmts(ph.Stmts) {
				return Phase{}, false
			}
			return ph, true
		}
	}
	return Phase{}, false
}

// CheckResolvedArguments is CheckArguments once every per-host value exists: the caller
// runs it after `proto.ResolveRefs`, on the control host, before the request goes out.
//
// The two passes differ in what they may look at, not in what they decide (#582). While
// the plan is read, an argument written `${inventory.flag}` is the *text* standing in for
// a value the host has not supplied yet; judging it there refused plans that were correct,
// which is what made a `bool` parameter unable to take a value from the inventory. Here
// the value is the value, so the declared type is held to it as well — the rule ADR-0045
// §3 is named after: checked where the value is known.
func CheckResolvedArguments(steps []proto.Step, resolve DefResolver) error {
	for _, s := range steps {
		if err := checkResolvedStep(s, resolve); err != nil {
			return err
		}
	}
	return nil
}

func checkResolvedStep(s proto.Step, resolve DefResolver) error {
	for _, group := range [][]proto.Step{s.Block, s.Parallel} {
		if err := CheckResolvedArguments(group, resolve); err != nil {
			return err
		}
	}
	if s.If != nil {
		for _, group := range [][]proto.Step{s.If.Then, s.If.Else} {
			if err := CheckResolvedArguments(group, resolve); err != nil {
				return err
			}
		}
		if s.If.Cond != nil {
			if err := checkResolvedStep(*s.If.Cond, resolve); err != nil {
				return err
			}
		}
	}
	if s.Instruction == "" || s.Instruction == "shell" || resolve == nil {
		return nil
	}
	def, ok := resolve(s.Instruction)
	if !ok {
		return nil
	}
	// The declared type, on the value that finally exists. A parameter the step does not
	// carry was defaulted by the def and is not the caller's to answer for.
	for _, param := range def.Params {
		v, given := s.Args[param.Name]
		if !given || param.Type != "bool" || isBoolValue(v) {
			continue
		}
		return fmt.Errorf("%s%s: %s expects a boolean, got %q — write true or false",
			position(s), s.Instruction, param.Name, v)
	}
	return checkStepArguments(s, resolve)
}

// CheckArguments evaluates every pure `check` it can decide, against the arguments the
// plan wrote, and returns the first refusal. `resolve` supplies the defs — user defs and
// the stdlib both, which is why this is driven from the caller the way CheckCycles is.
func CheckArguments(steps []proto.Step, resolve DefResolver) error {
	for _, s := range steps {
		if err := checkStepArguments(s, resolve); err != nil {
			return err
		}
	}
	return nil
}

func checkStepArguments(s proto.Step, resolve DefResolver) error {
	// A block, a parallel set and an if all hold steps, and an argument written inside
	// one is as wrong as an argument written outside it.
	for _, group := range [][]proto.Step{s.Block, s.Parallel} {
		if err := CheckArguments(group, resolve); err != nil {
			return err
		}
	}
	if s.If != nil {
		for _, group := range [][]proto.Step{s.If.Then, s.If.Else} {
			if err := CheckArguments(group, resolve); err != nil {
				return err
			}
		}
		if s.If.Cond != nil {
			if err := checkStepArguments(*s.If.Cond, resolve); err != nil {
				return err
			}
		}
	}
	if s.Instruction == "" || s.Instruction == "shell" || resolve == nil {
		return nil
	}
	def, ok := resolve(s.Instruction)
	if !ok {
		return nil // an unknown instruction is not this pass's error to report
	}
	// Only what the plan already holds (ADR-0056 §4). A Ref or a Template is resolved per
	// host, and checking the text `${inventory.flag}` against a pattern would refuse a
	// plan that is correct — which is #582, from the type check doing exactly that.
	if len(s.Refs) > 0 || len(s.Templates) > 0 {
		return nil
	}
	ph, ok := pureCheck(def)
	if !ok {
		return nil
	}
	res, decided := evalPureCheck(def, ph, s.Args, s.With, s.Control)
	// Only an `err` decides: an `ok` from a question describes state, and ADR-0051 keeps a
	// question out of check mode entirely.
	if !decided || res.Category != engine.ERR {
		return nil
	}
	return fmt.Errorf("%s%s: %s", position(s), s.Instruction, res.String())
}

// newPureEvaluator builds the smallest evaluator a pure phase needs: the def's arguments,
// its defaults, and a `with` override. No executor, no control-host channel, no def
// resolver — a statement needing any of them never got past pureStmts.
func newPureEvaluator(def Def, args, with map[string]string, control []string) *evaluator {
	ev := &evaluator{vars: map[string]value{}, def: def.Name, mode: engine.Check}
	for k, v := range args {
		ev.vars[k] = v
	}
	for _, name := range control {
		if v, ok := ev.vars[name].(string); ok {
			ev.vars[name] = controlPath(v)
		}
	}
	for _, p := range def.Params {
		if _, ok := ev.vars[p.Name]; !ok && p.Default != nil {
			ev.vars[p.Name] = ev.evalExpr(p.Default)
		}
	}
	// The most local binding wins, as at a call site (ADR-0022).
	for k, v := range with {
		ev.vars[k] = v
	}
	return ev
}

// evalPureCheck runs one pure phase with no executor behind it. Nothing in it can reach a
// shell — pureStmts refused every path that could — so the nil executor is the assertion,
// not a risk: a statement that got past the walk would panic here rather than run.
func evalPureCheck(def Def, ph Phase, args, with map[string]string, control []string) (res engine.Result, decided bool) {
	defer func() {
		// An evaluation failure here is not the plan's verdict: an unbound variable or a
		// pattern that does not compile is reported when the def runs, with the machinery
		// that reports it today. This pass answers or stays silent; it never invents.
		if r := recover(); r != nil {
			res, decided = engine.Result{}, false
		}
	}()
	ev := newPureEvaluator(def, args, with, control)
	o := ev.evalPhase(ph)
	if o == nil {
		return engine.Result{}, false
	}
	return ev.toResult(*o), true
}

// position renders where the instruction was written, empty when the step carries no
// line — a step built by a test, or one this parser did not produce.
func position(s proto.Step) string {
	if s.Line <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d: ", s.Line, s.Col)
}
