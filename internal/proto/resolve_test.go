package proto

import "testing"

func TestResolveRefs(t *testing.T) {
	steps := []Step{
		{Instruction: "dir-owner", Args: map[string]string{"path": "/opt"}, Refs: map[string]string{"owner": "owner"}},
		{Parallel: []Step{
			{Instruction: "user-group", Args: map[string]string{"group": "docker"}, Refs: map[string]string{"user": "owner"}},
		}},
	}
	out, err := ResolveRefs(steps, map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Args["owner"] != "alice" || out[0].Args["path"] != "/opt" {
		t.Fatalf("resolved step: %+v", out[0].Args)
	}
	if out[0].Refs != nil {
		t.Fatalf("refs must be cleared after resolution: %+v", out[0].Refs)
	}
	if out[1].Parallel[0].Args["user"] != "alice" {
		t.Fatalf("nested (parallel) resolution: %+v", out[1].Parallel[0])
	}
}

func TestResolveRefs_Undefined(t *testing.T) {
	steps := []Step{{Instruction: "dir-owner", Refs: map[string]string{"owner": "owner"}}}
	if _, err := ResolveRefs(steps, map[string]string{}); err == nil {
		t.Fatal("expected an error for an undefined ref")
	}
}
