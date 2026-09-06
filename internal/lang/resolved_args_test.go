package lang

import (
	"strings"
	"testing"

	"shellf/internal/proto"
)

// #582. `${inventory.<field>}` is resolved per host, after the plan is read (ADR-0052),
// so while the plan is read the argument is the *text* `${inventory.flag}` and not a
// value. The type check read that text and refused the plan — so no `bool` parameter
// could take a value from the inventory at all.
//
// The rule the fix follows is the title of ADR-0045 §3: checked where the value is known.
// For a per-host value that place is after expansion, on the control host, before the
// request is sent.

const boolDef = `def wants(running: bool) {
	apply { return ok.done }
}`

// The plan that was refused and should not have been — at the parser, which is where the
// defect lived: the type check read the raw `${inventory.flag}` three lines before the
// test that knows such a value is expanded later.
func TestResolvedArgs_APerHostBooleanParses(t *testing.T) {
	src := `on web { wants("${inventory.flag}") }`
	libs := map[string]string{"lib.shellf": boolDef}
	if _, _, err := ParsePackage(src, libs, nil, map[string]string{}, map[string]string{}, testStdSig); err != nil {
		t.Fatalf("a boolean held in the inventory is a legitimate plan: %v", err)
	}
}

// The literal case does not regress: written out, `"yes"` is knowable now and is refused
// now, with its position (ADR-0045 §3).
func TestResolvedArgs_ALiteralNonBooleanIsStillRefusedAtParse(t *testing.T) {
	src := `on web { wants("yes") }`
	libs := map[string]string{"lib.shellf": boolDef}
	_, _, err := ParsePackage(src, libs, nil, map[string]string{}, map[string]string{}, testStdSig)
	if err == nil {
		t.Fatal(`"yes" is not a boolean and must still be refused while the plan is read`)
	}
	if !strings.Contains(err.Error(), "expects a boolean") {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// The plan that was refused and should not have been.
func TestResolvedArgs_APerHostBooleanIsNotJudgedEarly(t *testing.T) {
	steps := []proto.Step{{Instruction: "wants", Templates: map[string]string{"running": "${inventory.flag}"}}}
	if err := CheckArguments(steps, defsFrom(t, boolDef)); err != nil {
		t.Fatalf("the text standing in for a per-host value must not be judged: %v", err)
	}
}

// …and is judged once expanded, so nothing is merely skipped: a host holding `"yes"` is
// still refused, before its request goes out.
func TestResolvedArgs_ARealBooleanIsCheckedAfterExpansion(t *testing.T) {
	resolve := defsFrom(t, boolDef)
	for _, c := range []struct{ value, want string }{
		{"true", ""},
		{"false", ""},
		{"yes", "expects a boolean"},
	} {
		t.Run(c.value, func(t *testing.T) {
			steps := []proto.Step{{Instruction: "wants", Args: map[string]string{"running": c.value}, Line: 4, Col: 5}}
			err := CheckResolvedArguments(steps, resolve)
			if c.want == "" {
				if err != nil {
					t.Fatalf("%q is a boolean: %v", c.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q is not a boolean and must be refused", c.value)
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "running") {
				t.Fatalf("the refusal must name the parameter and what it wanted: %v", err)
			}
		})
	}
}

// The shape guards of ADR-0056 reach per-host values by the same route: once expanded,
// the value is as knowable as one written in the plan.
func TestResolvedArgs_ShapeGuardsReachPerHostValuesToo(t *testing.T) {
	steps := []proto.Step{{Instruction: "guard", Args: map[string]string{"key": "a=b"}}}
	err := CheckResolvedArguments(steps, defsFrom(t, guardSrc))
	if err == nil || !strings.Contains(err.Error(), "keyMustNotContainEquals") {
		t.Fatalf("an expanded value must meet the same guard: %v", err)
	}
}
