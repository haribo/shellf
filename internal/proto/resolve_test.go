package proto

import "testing"

func TestResolveRefs(t *testing.T) {
	steps := []Step{
		{Instruction: "dir.owner", Args: map[string]string{"path": "/opt"}, Refs: map[string]string{"owner": "owner"}},
		{Parallel: []Step{
			{Instruction: "user.group", Args: map[string]string{"group": "docker"}, Refs: map[string]string{"user": "owner"}},
		}},
	}
	out, err := ResolveRefs(steps, map[string]string{"owner": "alice"}, "")
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

func TestResolveRefs_PreservesBindAndCaught(t *testing.T) {
	// A captured, caught step must keep its Bind and Caught through resolution —
	// otherwise `x = call()?` loses its binding on the orchestrated path.
	steps := []Step{{
		Instruction: "apt.install",
		Args:        map[string]string{"pkg": "nginx"},
		Bind:        "x",
		Caught:      true,
	}}
	out, err := ResolveRefs(steps, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Bind != "x" {
		t.Fatalf("Bind dropped by ResolveRefs: %+v", out[0])
	}
	if !out[0].Caught {
		t.Fatalf("Caught dropped by ResolveRefs: %+v", out[0])
	}
}

func TestResolveRefs_ShellStepGetsEnv(t *testing.T) {
	// A plan-level shell step carries the per-host env so `$name` resolves (#106).
	out, err := ResolveRefs([]Step{{Instruction: "shell", Args: map[string]string{"cmd": "echo $name"}}},
		map[string]string{"name": "alice"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Env["name"] != "alice" {
		t.Fatalf("shell step should carry the per-host env: %+v", out[0].Env)
	}
	// A non-shell instruction gets its values via Args, not the env.
	out, _ = ResolveRefs([]Step{{Instruction: "dir.ensure", Args: map[string]string{"path": "/x"}}},
		map[string]string{"name": "alice"}, "")
	if out[0].Env != nil {
		t.Fatalf("non-shell step should not carry env: %+v", out[0].Env)
	}
}

func TestResolveRefs_ShellWithOverridesEnv(t *testing.T) {
	// A `with { }` binding overrides the per-host env for that shell only,
	// without mutating the shared host env (ADR-0022).
	host := map[string]string{"name": "alice", "role": "web"}
	out, err := ResolveRefs([]Step{{
		Instruction: "shell", Args: map[string]string{"cmd": "echo $name"},
		With: map[string]string{"name": "bob", "extra": "v"},
	}}, host, "")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Env["name"] != "bob" || out[0].Env["extra"] != "v" || out[0].Env["role"] != "web" {
		t.Fatalf("with should override/extend the shell env: %+v", out[0].Env)
	}
	if host["name"] != "alice" {
		t.Fatalf("the shared host env must not be mutated: %+v", host)
	}
}

func TestResolveRefs_ShellInterp(t *testing.T) {
	// An unannotated shell inherits the host interpreter (ADR-0012).
	out, _ := ResolveRefs([]Step{{Instruction: "shell", Args: map[string]string{"cmd": "x"}}}, nil, "bash")
	if out[0].Interp != "bash" {
		t.Fatalf("unannotated shell should inherit the host interp: %q", out[0].Interp)
	}
	// An annotated shell keeps its own interpreter.
	out, _ = ResolveRefs([]Step{{Instruction: "shell", Args: map[string]string{"cmd": "x"}, Interp: "nu"}}, nil, "bash")
	if out[0].Interp != "nu" {
		t.Fatalf("annotated shell must keep its interp: %q", out[0].Interp)
	}
}

func TestResolveRefs_Undefined(t *testing.T) {
	steps := []Step{{Instruction: "dir.owner", Refs: map[string]string{"owner": "owner"}}}
	if _, err := ResolveRefs(steps, map[string]string{}, ""); err == nil {
		t.Fatal("expected an error for an undefined ref")
	}
}

// Control says which arguments the plan marked `%"…"`. ResolveRefs rebuilds each step
// field by field, so a field left out is dropped silently — and dropping this one makes
// the agent read a control-host path on the target instead. It cost an end-to-end
// failure to find, because the local transport never goes through here (#334).
func TestResolveRefs_KeepsControl(t *testing.T) {
	in := []Step{
		{Instruction: "file.template", Args: map[string]string{"src": "conf.j2", "dst": "/etc/x"},
			Control: []string{"src"}},
		{Block: []Step{{Instruction: "x"}}, Control: []string{"a"}},
		{If: &IfBlock{CondRef: &ResultRef{Name: "r"}, Then: []Step{{Instruction: "y"}}}, Control: []string{"b"}},
	}
	out, err := ResolveRefs(in, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"src"}, {"a"}, {"b"}} {
		if len(out[i].Control) != len(want) || (len(want) > 0 && out[i].Control[0] != want[0]) {
			t.Errorf("step %d: control lost: got %v, want %v", i, out[i].Control, want)
		}
	}
}
