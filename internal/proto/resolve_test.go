package proto

import (
	"strings"
	"testing"
)

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

// ADR-0052: a Template is an argument still carrying `${inventory.<field>}` when the plan
// was read. It is expanded here, per host, in the same pass as a Ref — the difference being
// that a Ref *is* a name while a Template is a string with names inside it.
func TestResolveRefs_ExpandsInventoryTemplates(t *testing.T) {
	env := map[string]string{
		"domain":                    "global.example",
		InventoryPrefix + "domain":  "app.example.test",
		InventoryPrefix + "name":    "web",
		InventoryPrefix + "address": "10.0.0.9",
	}
	steps := []Step{{
		Instruction: "http.check",
		Args:        map[string]string{"status": "200"},
		Templates: map[string]string{
			"url":   "https://${inventory.domain}/healthz",
			"label": "${inventory.name}@${inventory.address}",
		},
	}}

	out, err := ResolveRefs(steps, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out[0].Args["url"], "https://app.example.test/healthz"; got != want {
		t.Fatalf("url: got %q, want %q", got, want)
	}
	// Two fields in one string, which a bare reference cannot express — the whole point.
	if got, want := out[0].Args["label"], "web@10.0.0.9"; got != want {
		t.Fatalf("label: got %q, want %q", got, want)
	}
	// A global of the same name is a different variable: the prefix names its source.
	if out[0].Args["url"] == "https://global.example/healthz" {
		t.Fatal("an inventory field must not fall back to a global of the same name")
	}
	if len(out[0].Templates) != 0 {
		t.Fatalf("templates must be resolved away before the agent sees them: %v", out[0].Templates)
	}
}

// An unknown field errors, naming it.
//
// The host used to be added by the orchestrator; since ADR-0054 this package says it
// itself, because a cross-host read has two ways to be misspelled and they are different
// mistakes — see TestResolveRefs_NamesWhatIsMissing.
func TestResolveRefs_UnknownInventoryField(t *testing.T) {
	steps := []Step{{
		Instruction: "file.write",
		Templates:   map[string]string{"content": "${inventory.nope}"},
	}}
	_, err := ResolveRefs(steps, map[string]string{}, "")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("want an error naming the field, got %v", err)
	}
}

// #547, ADR-0054 §4: a cross-host read can name a host that does not exist or a field that
// host does not have. "undefined variable inventory.bd.address" makes the reader check
// both; saying which one moved is the whole value of the message.
func TestResolveRefs_NamesWhatIsMissing(t *testing.T) {
	env := map[string]string{"inventory.db.address": "10.0.0.40"}
	for _, tc := range []struct{ name, tmpl, want string }{
		{"unknown host", "${inventory.bd.address}", `undefined host "bd"`},
		{"unknown field of a known host", "${inventory.db.port}", `host "db" declares no field "port"`},
		{"unknown field of this host", "${inventory.nope}", `this host declares no field "nope"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []Step{{Instruction: "file.write", Templates: map[string]string{"content": tc.tmpl}}}
			_, err := ResolveRefs(steps, env, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
