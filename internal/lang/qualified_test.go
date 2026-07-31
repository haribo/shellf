package lang

import "testing"

func TestParseQualifiedCall(t *testing.T) {
	src := `
on server {
  docker.install()
  docker.network("web")
  apt.install("nginx")
}
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(steps))
	}
	if steps[0].Instruction != "docker.install" || len(steps[0].Args) != 0 {
		t.Fatalf("step 0: %+v", steps[0])
	}
	if steps[1].Instruction != "docker.network" || steps[1].Args["name"] != "web" {
		t.Fatalf("step 1: %+v", steps[1])
	}
	if steps[2].Instruction != "apt.install" || steps[2].Args["pkg"] != "nginx" {
		t.Fatalf("step 2 (bare still works): %+v", steps[2])
	}
}
