package lang

import (
	"strings"
	"testing"
)

func defsOf(t *testing.T, src string) (map[string]Def, DefResolver) {
	t.Helper()
	parsed, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range parsed {
		byName[d.Name] = d
	}
	return byName, func(n string) (Def, bool) { d, ok := byName[n]; return d, ok }
}

// ADR-0030 §6: a cycle is a writing error and must be refused from reading the files.
// #310 caught it at evaluation instead — correct message, wrong moment: the plan was
// already on the target and earlier steps had acted, leaving a partially applied host.
func TestCycles_Refused(t *testing.T) {
	for name, tc := range map[string]struct{ src, chain string }{
		"self call": {
			`def a() { apply { a() return ok.x } }`,
			"a -> a",
		},
		"mutual": {
			`def a() { apply { b() return ok.x } }
			 def b() { apply { a() return ok.x } }`,
			"a -> b -> a",
		},
		"indirect": {
			`def a() { apply { b() return ok.x } }
			 def b() { apply { c() return ok.x } }
			 def c() { apply { a() return ok.x } }`,
			"a -> b -> c -> a",
		},
		"through a delegation": {
			// The edge ADR-0037 added: a delegation is a call, and a graph that only
			// walked phases would not see it (#339 landed after #311 was written).
			`def a(p: str) { b(p) }
			 def b(p: str) { apply { a(p) return ok.x } }`,
			"a -> b -> a",
		},
		"through an if branch": {
			`def a() { apply { if "1" == "1" { b() } return ok.x } }
			 def b() { apply { a() return ok.x } }`,
			"a -> b -> a",
		},
		"through an observe": {
			`def a() { observe { return state(x: b()) } apply { return ok.x } }
			 def b() { apply { a() return ok.x } }`,
			"a -> b -> a",
		},
	} {
		t.Run(name, func(t *testing.T) {
			defs, resolve := defsOf(t, tc.src)
			err := CheckCycles(defs, resolve)
			if err == nil {
				t.Fatal("a cycle must be refused when the defs are loaded")
			}
			// The chain is the diagnostic: "there is a cycle" sends the author reading
			// every def, the path sends them to the edge to cut.
			if !strings.Contains(err.Error(), tc.chain) {
				t.Fatalf("want chain %q, got %v", tc.chain, err)
			}
			if !strings.HasPrefix(err.Error(), "call cycle: ") {
				t.Fatalf("the wording must match the evaluator's guard, got %v", err)
			}
		})
	}
}

// The other half. A checker that refuses everything would pass every test above and be
// worse than none.
func TestCycles_Accepted(t *testing.T) {
	for name, src := range map[string]string{
		"a plain chain": `def a() { apply { b() return ok.x } }
		                  def b() { apply { c() return ok.x } }
		                  def c() { apply { return ok.x } }`,
		"the same callee twice on different branches": `
			def a() { apply { if "1" == "1" { c() } if "2" == "2" { c() } return ok.x } }
			def c() { apply { return ok.x } }`,
		"a diamond": `def a() { apply { b() c() return ok.x } }
		              def b() { apply { d() return ok.x } }
		              def c() { apply { d() return ok.x } }
		              def d() { apply { return ok.x } }`,
		"a call to something that is not a def": `def a() { apply { shell { echo hi } return ok.x } }`,
		"a primitive, which has no body":        `def a(p: str) { apply { x = ~file.read(p) return ok.x } }`,
	} {
		t.Run(name, func(t *testing.T) {
			defs, resolve := defsOf(t, src)
			if err := CheckCycles(defs, resolve); err != nil {
				t.Fatalf("must be accepted: %v", err)
			}
		})
	}
}

// A cycle crossing the user/stdlib boundary: the case a single-package check misses, and
// the reason CheckCycles takes a resolver instead of a map. `wrap` calls `leaf`; the
// stdlib's `leaf` is overridden by a user def that calls `wrap` back.
func TestCycles_AcrossTheResolverBoundary(t *testing.T) {
	user, _ := defsOf(t, `def wrap(p: str) { apply { leaf(p) return ok.x } }`)
	stdlib, _ := defsOf(t, `def leaf(p: str) { apply { wrap(p) return ok.x } }`)

	resolve := func(n string) (Def, bool) { // user first, then "stdlib" (ADR-0014)
		if d, ok := user[n]; ok {
			return d, true
		}
		d, ok := stdlib[n]
		return d, ok
	}
	err := CheckCycles(user, resolve)
	if err == nil {
		t.Fatal("a cycle through a def the user package does not hold must still be refused")
	}
	if !strings.Contains(err.Error(), "wrap -> leaf -> wrap") {
		t.Fatalf("got %v", err)
	}
}

// The report must not depend on map iteration order: a diagnostic that changes between
// runs on the same source is one nobody trusts.
func TestCycles_StableReport(t *testing.T) {
	src := `def a() { apply { b() return ok.x } }
	        def b() { apply { a() return ok.x } }
	        def m() { apply { n() return ok.x } }
	        def n() { apply { m() return ok.x } }`
	defs, resolve := defsOf(t, src)
	first := CheckCycles(defs, resolve)
	if first == nil {
		t.Fatal("expected a cycle")
	}
	for i := 0; i < 20; i++ {
		if got := CheckCycles(defs, resolve); got.Error() != first.Error() {
			t.Fatalf("report drifted between runs: %v vs %v", first, got)
		}
	}
}
