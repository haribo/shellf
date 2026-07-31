package agent

import (
	"testing"

	"shellf/internal/proto"
)

func capture(cmd, unless, bind string) proto.Step {
	args := map[string]string{"cmd": cmd}
	if unless != "" {
		args["unless"] = unless
	}
	return proto.Step{Instruction: "shell", Args: args, Bind: bind}
}

func ifRef(name, test string) proto.Step {
	ref := &proto.ResultRef{Name: name}
	if test == "changed" {
		ref.Changed = true
	} else {
		ref.Category = test // outcome pattern: "ok" | "err" | "would"
	}
	return proto.Step{If: &proto.IfBlock{
		CondRef: ref,
		Then:    []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "thencmd"}}},
	}}
}

func TestAgentCapture_ChangedRunsThen(t *testing.T) {
	// x = shell { doit } (no guard) → apply ran → changed → then runs
	f := newFake()
	f.set("doit", "", 0)
	f.set("thencmd", "", 0)
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		capture("doit", "", "x"),
		ifRef("x", "changed"),
	}})
	if !f.called("thencmd", "") {
		t.Fatalf("x.changed true → then should run")
	}
}

func TestAgentCapture_NotChangedSkipsThen(t *testing.T) {
	// x = shell { doit } unless { guard-ok } → skipped → not changed → then skipped
	f := newFake()
	f.set("guardcmd", "", 0) // guard satisfied → shell skipped
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		capture("doit", "guardcmd", "x"),
		ifRef("x", "changed"),
	}})
	if f.called("doit", "") {
		t.Fatalf("guard satisfied → shell must be skipped")
	}
	if f.called("thencmd", "") {
		t.Fatalf("x.changed false → then must NOT run")
	}
}

func TestAgentCapture_OkSugar(t *testing.T) {
	// x = shell { doit } exit 0 → ok → `if x` (= x.ok) → then
	f := newFake()
	f.set("doit", "", 0)
	f.set("thencmd", "", 0)
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		capture("doit", "", "x"),
		ifRef("x", "ok"),
	}})
	if !f.called("thencmd", "") {
		t.Fatalf("x.ok true → then should run")
	}
}

func TestAgentCapture_OutcomePattern(t *testing.T) {
	// x = shell { doit } exit 0 → ok. `x == ok` runs its then; `x == err` doesn't.
	f := newFake()
	f.set("doit", "", 0)
	f.set("yes", "", 0)
	f.set("no", "", 0)
	then := func(cmd string) []proto.Step {
		return []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": cmd}}}
	}
	serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		capture("doit", "", "x"),
		{If: &proto.IfBlock{CondRef: &proto.ResultRef{Name: "x", Category: "ok"}, Then: then("yes")}},
		{If: &proto.IfBlock{CondRef: &proto.ResultRef{Name: "x", Category: "err"}, Then: then("no")}},
	}})
	if !f.called("yes", "") {
		t.Fatalf("x == ok → then should run")
	}
	if f.called("no", "") {
		t.Fatalf("x == err → then must NOT run")
	}
}

func TestAgentCapture_UndefinedResultErrs(t *testing.T) {
	f := newFake()
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{ifRef("missing", "ok")}})
	if resp.Results[0].Category != "err" {
		t.Fatalf("undefined captured result should err: %+v", resp.Results[0])
	}
}
