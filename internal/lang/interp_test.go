package lang

import (
	"strings"
	"testing"
)

func TestInterpolation(t *testing.T) {
	src := `
owner = "haribo"
on server {
  dir-owner("/opt", "${owner}:${owner}")
  file-write("/f", """OWNER=${owner} DB=${DATABASE_URL}""")
}
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if got := steps[0].Args["owner"]; got != "haribo:haribo" {
		t.Fatalf("simple string interpolated: got %q, want haribo:haribo", got)
	}
	// triple-quote is raw: both ${owner} and ${DATABASE_URL} stay verbatim
	if got := steps[1].Args["content"]; !strings.Contains(got, "${owner}") || !strings.Contains(got, "${DATABASE_URL}") {
		t.Fatalf("triple-quote must stay raw, got %q", got)
	}
}

func TestInterpolationUndefined(t *testing.T) {
	if _, err := ParsePlan(`on s { dir-owner("/opt", "${missing}") }`); err == nil {
		t.Fatal("expected error for undefined interpolation variable")
	}
}

func TestInterpolationInBinding(t *testing.T) {
	// a binding's value may itself interpolate an earlier binding
	plan, err := ParsePlan(`
owner = "haribo"
pair = "${owner}:${owner}"
on s { dir-owner("/opt", pair) }
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan[0].Steps[0].Args["owner"]; got != "haribo:haribo" {
		t.Fatalf("binding interpolation: got %q, want haribo:haribo", got)
	}
}
