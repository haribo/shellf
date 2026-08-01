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

func TestBareIdentBecomesRef(t *testing.T) {
	// A bare identifier argument is NOT resolved at parse — it becomes a Ref,
	// resolved per host at orchestration time.
	pl, err := ParsePlanWithVars(`on s { dir-owner("/opt", owner) }`, map[string]string{"owner": "x"}, nil, defaultSig)
	if err != nil {
		t.Fatal(err)
	}
	st := pl[0].Steps[0]
	if st.Refs["owner"] != "owner" {
		t.Fatalf("bare ident should be a ref: %+v", st)
	}
	if _, ok := st.Args["owner"]; ok {
		t.Fatalf("bare ident must not be resolved into Args: %+v", st.Args)
	}
}

func TestPlanBindingEnrichesBaseVars(t *testing.T) {
	// A top-level binding is appended to baseVars (mutated in place) so the
	// caller can resolve the plan's refs per host afterwards.
	base := map[string]string{}
	_, err := ParsePlanWithVars("owner = \"haribo\"\non s { dir-owner(\"/opt\", owner) }", base, nil, defaultSig)
	if err != nil {
		t.Fatal(err)
	}
	if base["owner"] != "haribo" {
		t.Fatalf("plan binding should enrich baseVars: %+v", base)
	}
}

func TestInterpolationPrecedence(t *testing.T) {
	// Interpolation resolves at parse; --set (setVars) wins over base.
	pl, err := ParsePlanWithVars(`on s { dir-owner("/opt", "${owner}") }`,
		map[string]string{"owner": "base"}, map[string]string{"owner": "set"}, defaultSig)
	if err != nil {
		t.Fatal(err)
	}
	if got := pl[0].Steps[0].Args["owner"]; got != "set" {
		t.Fatalf("interpolation: --set should win over base, got %q", got)
	}
}
