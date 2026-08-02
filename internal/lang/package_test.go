package lang

import (
	"strings"
	"testing"
)

func pkg(t *testing.T, planSrc string, libs map[string]string) ([]Def, error) {
	t.Helper()
	_, defs, err := ParsePackage(planSrc, libs, map[string]string{}, map[string]string{}, testStdSig)
	return defs, err
}

func TestPackage_InlineDefUsedInPlan(t *testing.T) {
	plan, defs, err := ParsePackage(
		"def my-x(msg: str) { apply { shell { echo \"$msg\" } } }\non web { my-x(\"hi\") }",
		nil, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "my-x" {
		t.Fatalf("user def not collected: %+v", defs)
	}
	step := plan[0].Steps[0]
	if step.Instruction != "my-x" || step.Args["msg"] != "hi" {
		t.Fatalf("call not resolved against the user def: %+v", step)
	}
}

func TestPackage_DefInSiblingFile(t *testing.T) {
	// A def written in one file is usable from the plan file — no import.
	libs := map[string]string{"lib.shellf": `def helper(path: str) { apply { shell { touch "$path" } } }`}
	plan, defs, err := ParsePackage(`on web { helper("/tmp/x") }`, libs, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "helper" {
		t.Fatalf("sibling def not collected: %+v", defs)
	}
	if plan[0].Steps[0].Args["path"] != "/tmp/x" {
		t.Fatalf("sibling def call not resolved: %+v", plan[0].Steps[0])
	}
}

func TestPackage_DuplicateDef(t *testing.T) {
	_, err := pkg(t, "def a(x: str) { apply { shell { echo hi } } }\ndef a(y: str) { apply { shell { echo ho } } }\non web { }", nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate def") {
		t.Fatalf("duplicate def must error: %v", err)
	}
}

func TestPackage_ShadowStdlibNeedsOverride(t *testing.T) {
	// A plain def with a stdlib name is an error…
	_, err := pkg(t, `def dir-ensure(path: str) { apply { shell { mkdir "$path" } } }`, nil)
	if err == nil || !strings.Contains(err.Error(), "shadows a stdlib def") {
		t.Fatalf("shadowing must error without override: %v", err)
	}
	// …but `override def` is allowed and wins.
	defs, err := pkg(t, `override def dir-ensure(path: str) { apply { shell { mkdir "$path" } } }`, nil)
	if err != nil {
		t.Fatalf("override should be allowed: %v", err)
	}
	if len(defs) != 1 || !defs[0].Override {
		t.Fatalf("override def not recorded: %+v", defs)
	}
}

func TestPackage_OverrideNothing(t *testing.T) {
	_, err := pkg(t, `override def brand-new(x: str) { apply { shell { echo hi } } }`, nil)
	if err == nil || !strings.Contains(err.Error(), "overrides nothing") {
		t.Fatalf("override of an unknown name must error: %v", err)
	}
}

func TestPackage_SiblingCannotTargetHosts(t *testing.T) {
	libs := map[string]string{"web.shellf": `on web { }`}
	_, err := pkg(t, `on app { }`, libs)
	if err == nil || !strings.Contains(err.Error(), "may only contain defs") {
		t.Fatalf("an `on` block in a sibling must error: %v", err)
	}
}

func TestPackage_ForwardRefInPlanFile(t *testing.T) {
	// Documented v1 constraint: within the plan file a def must precede its use
	// (sibling files are order-free). A forward reference is an unknown call.
	_, err := pkg(t, "on web { later() }\ndef later() { apply { shell { echo hi } } }", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown instruction") {
		t.Fatalf("a forward-referenced def should be unknown: %v", err)
	}
}
