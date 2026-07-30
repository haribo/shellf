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

func ifRef(name, field string) proto.Step {
	return proto.Step{If: &proto.IfBlock{
		CondRef: &proto.ResultRef{Name: name, Field: field},
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

func TestAgentCapture_UndefinedResultErrs(t *testing.T) {
	f := newFake()
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{ifRef("missing", "ok")}})
	if resp.Results[0].Category != "err" {
		t.Fatalf("undefined captured result should err: %+v", resp.Results[0])
	}
}
