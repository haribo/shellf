package lang

import "testing"

func TestParseCapture(t *testing.T) {
	plan, err := ParsePlan(`on s {
  x = dir-ensure("/opt")
  if x.changed { apt-install("nginx") }
  if x { apt-install("redis") }
}`)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if steps[0].Bind != "x" || steps[0].Instruction != "dir-ensure" {
		t.Fatalf("capture: %+v", steps[0])
	}
	if r := steps[1].If.CondRef; r == nil || r.Name != "x" || r.Field != "changed" {
		t.Fatalf("if x.changed: %+v", steps[1].If)
	}
	if r := steps[2].If.CondRef; r == nil || r.Field != "ok" { // `if x` → x.ok
		t.Fatalf("if x (sugar): %+v", steps[2].If)
	}
}

func TestParseIfQualifiedCallStaysCall(t *testing.T) {
	// docker.install() as a condition is a qualified call, not a ref
	plan, err := ParsePlan(`on s { if docker.install() { apt-install("x") } }`)
	if err != nil {
		t.Fatal(err)
	}
	st := plan[0].Steps[0]
	if st.If.CondRef != nil || st.If.Cond == nil || st.If.Cond.Instruction != "docker.install" {
		t.Fatalf("qualified call cond: %+v", st.If)
	}
}

func TestParseUnknownResultField(t *testing.T) {
	if _, err := ParsePlan(`on s { x = dir-ensure("/o") if x.bogus { apt-install("y") } }`); err == nil {
		t.Fatal("expected an error for an unknown result field")
	}
}

func TestParseCaptureRejectsIf(t *testing.T) {
	if _, err := ParsePlan(`on s { x = if dir-ensure("/o") { apt-install("y") } }`); err == nil {
		t.Fatal("expected an error capturing an if into a variable")
	}
}
