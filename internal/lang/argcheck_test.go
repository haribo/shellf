package lang

import (
	"strings"
	"testing"

	"shellf/internal/proto"
)

// ADR-0056. A `check` that reaches nothing answers while the plan is read, so a def
// refusing its arguments does not need a reachable machine to do it. Measured before this
// existed: on two unreachable hosts, a wrong argument shape took 10 020 ms and reported
// only "unreachable", while a wrong type took 5 ms and named its position.
//
// The refusals matter less than the silences here: this pass must stay quiet on everything
// it cannot decide, or it refuses plans that are correct (#582).

func defsFrom(t *testing.T, src string) DefResolver {
	t.Helper()
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	return func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }
}

const guardSrc = `def guard(key: str) {
	check {
		if ~text.matches(key, "=") { return err.keyMustNotContainEquals }
	}
	apply { return ok.done }
}`

func TestArgCheck_APureErrRefusesThePlan(t *testing.T) {
	steps := []proto.Step{{Instruction: "guard", Args: map[string]string{"key": "a=b"}, Line: 7, Col: 5}}
	err := CheckArguments(steps, defsFrom(t, guardSrc))
	if err == nil {
		t.Fatal("a pure check returning err must refuse the plan")
	}
	for _, want := range []string{"7:5", "guard", "err.keyMustNotContainEquals"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must carry %q: %v", want, err)
		}
	}
}

func TestArgCheck_ACleanArgumentIsNotRefused(t *testing.T) {
	steps := []proto.Step{{Instruction: "guard", Args: map[string]string{"key": "PORT"}}}
	if err := CheckArguments(steps, defsFrom(t, guardSrc)); err != nil {
		t.Fatalf("a clean argument must pass: %v", err)
	}
}

// The silence that keeps #582 from happening again here: `${inventory.flag}` is not the
// value, it is the text standing in for it until the host is known (ADR-0052). Checking it
// would refuse a plan that is correct.
func TestArgCheck_PerHostValuesAreLeftAlone(t *testing.T) {
	resolve := defsFrom(t, guardSrc)
	cases := map[string]proto.Step{
		"a template": {Instruction: "guard", Templates: map[string]string{"key": "${inventory.k}"}},
		"a ref":      {Instruction: "guard", Refs: map[string]string{"key": "k"}},
	}
	for what, s := range cases {
		t.Run(what, func(t *testing.T) {
			if err := CheckArguments([]proto.Step{s}, resolve); err != nil {
				t.Fatalf("%s is not known yet and must not be judged: %v", what, err)
			}
		})
	}
}

// A check holding a shell is not evaluated here at all — not even the statements before
// the shell. What a reader can predict must not depend on statement order (ADR-0056 §6).
func TestArgCheck_AShellInTheCheckIsLeftToTheTarget(t *testing.T) {
	src := `def mixed(key: str) {
	check {
		if ~text.matches(key, "=") { return err.keyMustNotContainEquals }
		r = shell { true }
		if !r { return err.runtime(r) }
	}
	apply { return ok.done }
}`
	steps := []proto.Step{{Instruction: "mixed", Args: map[string]string{"key": "a=b"}}}
	if err := CheckArguments(steps, defsFrom(t, src)); err != nil {
		t.Fatalf("a check that can reach a shell must be left to the target: %v", err)
	}
}

// A primitive that reaches a host is not pure, however inert it is in check mode.
func TestArgCheck_AHostReachingPrimitiveIsNotPure(t *testing.T) {
	src := `def reads(path: str) {
	check {
		x = ~file.read(path)
		return err.always
	}
	apply { return ok.done }
}`
	steps := []proto.Step{{Instruction: "reads", Args: map[string]string{"path": "/etc/x"}}}
	if err := CheckArguments(steps, defsFrom(t, src)); err != nil {
		t.Fatalf("~file.read reaches a target, so this check is not decided here: %v", err)
	}
}

// Only an `err` decides. A question's `ok` describes state, and ADR-0051 keeps a question
// out of check mode entirely.
func TestArgCheck_AnOkDecidesNothing(t *testing.T) {
	src := `def fine(key: str) {
	check {
		if ~text.matches(key, "a") { return ok.matched }
	}
	apply { return ok.done }
}`
	steps := []proto.Step{{Instruction: "fine", Args: map[string]string{"key": "abc"}}}
	if err := CheckArguments(steps, defsFrom(t, src)); err != nil {
		t.Fatalf("an ok verdict must not refuse anything: %v", err)
	}
}

// An argument written inside a block, a parallel set or a branch is as wrong as one
// written outside it.
func TestArgCheck_NestedStepsAreWalked(t *testing.T) {
	resolve := defsFrom(t, guardSrc)
	bad := proto.Step{Instruction: "guard", Args: map[string]string{"key": "a=b"}}
	cases := map[string]proto.Step{
		"an as block":    {Block: []proto.Step{bad}},
		"a parallel set": {Parallel: []proto.Step{bad}},
		"a then branch":  {If: &proto.IfBlock{Then: []proto.Step{bad}}},
		"an else branch": {If: &proto.IfBlock{Else: []proto.Step{bad}}},
		"a condition":    {If: &proto.IfBlock{Cond: &bad}},
	}
	for what, s := range cases {
		t.Run(what, func(t *testing.T) {
			if err := CheckArguments([]proto.Step{s}, resolve); err == nil {
				t.Fatalf("a bad argument in %s must be refused too", what)
			}
		})
	}
}

func TestArgCheck_AnUnknownInstructionIsNotThisPassesError(t *testing.T) {
	steps := []proto.Step{{Instruction: "nope.at-all", Args: map[string]string{"k": "v"}}}
	if err := CheckArguments(steps, defsFrom(t, guardSrc)); err != nil {
		t.Fatalf("naming an unknown instruction is the parser's error, not this one: %v", err)
	}
}
