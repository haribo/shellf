package lang

import "testing"

func TestParseVars(t *testing.T) {
	vars, err := ParseVars(`
# a vars file
owner = "haribo"
pair  = "${owner}:${owner}"
`)
	if err != nil {
		t.Fatal(err)
	}
	if vars["owner"] != "haribo" || vars["pair"] != "haribo:haribo" {
		t.Fatalf("vars: %+v", vars)
	}
}

func TestVarPrecedence(t *testing.T) {
	src := `
owner = "plan"
on s { dir-owner("/opt", owner) }
`
	// --vars only: a plan binding overrides the --vars default
	pl, err := ParsePlanWithVars(src, map[string]string{"owner": "vars"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := pl[0].Steps[0].Args["owner"]; got != "plan" {
		t.Fatalf("plan binding should override --vars: got %q, want plan", got)
	}

	// --set pins the key: the plan binding cannot override it
	pl, err = ParsePlanWithVars(src, map[string]string{"owner": "set"}, map[string]bool{"owner": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := pl[0].Steps[0].Args["owner"]; got != "set" {
		t.Fatalf("--set should win over the plan binding: got %q, want set", got)
	}
}

func TestGlobalVarResolvesInPlan(t *testing.T) {
	// a global var (no plan binding) is usable directly
	pl, err := ParsePlanWithVars(`on s { dir-owner("/opt", owner) }`, map[string]string{"owner": "haribo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := pl[0].Steps[0].Args["owner"]; got != "haribo" {
		t.Fatalf("global var: got %q, want haribo", got)
	}
}
