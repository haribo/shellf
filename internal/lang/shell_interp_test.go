package lang

import "testing"

func TestParseShellInterp(t *testing.T) {
	plan, err := ParsePlan(`on s {
  shell(bash) { echo hi | grep h }
  shell(nu) as root { ls }
  shell { plain }
}`)
	if err != nil {
		t.Fatal(err)
	}
	s := plan[0].Steps
	if s[0].Interp != "bash" || s[0].Args["cmd"] != "echo hi | grep h" {
		t.Fatalf("shell(bash): %+v", s[0])
	}
	if s[1].Interp != "nu" || s[1].Become != "root" {
		t.Fatalf("shell(nu) as root: %+v", s[1])
	}
	if s[2].Interp != "" {
		t.Fatalf("no annotation → empty interp: %q", s[2].Interp)
	}
}

func TestParseShellInterp_Unknown(t *testing.T) {
	if _, err := ParsePlan(`on s { shell(fish) { x } }`); err == nil {
		t.Fatal("an unknown interpreter must error at parse")
	}
}
