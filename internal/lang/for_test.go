package lang

import (
	"strings"
	"testing"
)

func TestFor_Unrolls(t *testing.T) {
	plan, err := ParsePlan(`on host { for port in ["80", "443"] { ufw.open("${port}", "tcp") } }`)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Args["port"] != "80" || steps[0].Args["proto"] != "tcp" {
		t.Fatalf("iter 0: %+v", steps[0].Args)
	}
	if steps[1].Args["port"] != "443" {
		t.Fatalf("iter 1: %+v", steps[1].Args)
	}
}

func TestFor_VarInsideString(t *testing.T) {
	plan, err := ParsePlan(`on host { for svc in ["traefik", "app"] { dir.ensure("/opt/${svc}/run") } }`)
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Steps[0].Args["path"] != "/opt/traefik/run" || plan[0].Steps[1].Args["path"] != "/opt/app/run" {
		t.Fatalf("var not substituted inside the string: %+v %+v", plan[0].Steps[0].Args, plan[0].Steps[1].Args)
	}
}

func TestFor_EmptyList(t *testing.T) {
	plan, err := ParsePlan(`on host { for x in [] { dir.ensure("/x") } dir.ensure("/after") }`)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan[0].Steps) != 1 || plan[0].Steps[0].Args["path"] != "/after" {
		t.Fatalf("an empty list should unroll to nothing: %+v", plan[0].Steps)
	}
}

func TestFor_Nested(t *testing.T) {
	plan, err := ParsePlan(`on host { for a in ["1", "2"] { for b in ["x", "y"] { dir.ensure("/${a}/${b}") } } }`)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 4 {
		t.Fatalf("nested loop should give 4 steps, got %d", len(steps))
	}
	got := []string{steps[0].Args["path"], steps[1].Args["path"], steps[2].Args["path"], steps[3].Args["path"]}
	want := []string{"/1/x", "/1/y", "/2/x", "/2/y"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nested unroll: got %v want %v", got, want)
		}
	}
}

func TestFor_WithIf(t *testing.T) {
	// The body may hold control flow; it is re-parsed per item.
	plan, err := ParsePlan(`on host { for p in ["a"] { if !dir.exists("/${p}") { dir.ensure("/${p}") } } }`)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan[0].Steps) != 1 || plan[0].Steps[0].If == nil {
		t.Fatalf("if inside for not unrolled: %+v", plan[0].Steps)
	}
}

func TestFor_MissingBrace(t *testing.T) {
	_, err := ParsePlan(`on host { for x in ["a"] dir.ensure("/x") }`)
	if err == nil || !strings.Contains(err.Error(), "expected '{'") {
		t.Fatalf("a for without a body block must error: %v", err)
	}
}

func TestInterpolate(t *testing.T) {
	look := func(n string) (string, bool) { v := map[string]string{"a": "1", "b": "two"}[n]; return v, v != "" }
	got, err := Interpolate("x=${a}, y=${b}", look)
	if err != nil || got != "x=1, y=two" {
		t.Fatalf("Interpolate: %q %v", got, err)
	}
	if _, err := Interpolate("${missing}", look); err == nil {
		t.Fatal("undefined var must error")
	}
	if _, err := Interpolate("${unclosed", look); err == nil {
		t.Fatal("unterminated must error")
	}
}

func TestTemplate(t *testing.T) {
	look := func(n string) (string, bool) { v := map[string]string{"a": "1"}[n]; return v, v != "" }
	// @{var} interpolates; ${x} and {{y}} pass through; @@ is a literal @.
	got, err := Template("a=@{a} b=${x} c={{y}} d=@@{z}", look)
	if err != nil || got != "a=1 b=${x} c={{y}} d=@{z}" {
		t.Fatalf("Template: %q %v", got, err)
	}
	if _, err := Template("@{missing}", look); err == nil {
		t.Fatal("undefined must error")
	}
	if _, err := Template("@{unclosed", look); err == nil {
		t.Fatal("unterminated must error")
	}
}
