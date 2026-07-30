package agent

import (
	"testing"

	"shellf/internal/proto"
)

func ifStep(condCmd string) proto.Step {
	return proto.Step{If: &proto.IfBlock{
		Cond: &proto.Step{Instruction: "shell", Args: map[string]string{"cmd": condCmd}},
		Then: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "thencmd"}}},
		Else: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "elsecmd"}}},
	}}
}

func TestAgentIf_ThenOnOk(t *testing.T) {
	f := newFake()
	f.set("condcmd", "", 0) // cond ok → then
	f.set("thencmd", "", 0)
	f.set("elsecmd", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{ifStep("condcmd")}})

	if !f.called("thencmd", "") || f.called("elsecmd", "") {
		t.Fatalf("cond-ok: should run then, not else")
	}
	if resp.Results[0].Category != "ok" {
		t.Fatalf("if result: %+v", resp.Results[0])
	}
}

func TestAgentIf_ElseOnErr(t *testing.T) {
	f := newFake()
	f.set("condcmd", "", 1) // cond err → else
	f.set("thencmd", "", 0)
	f.set("elsecmd", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{ifStep("condcmd")}})

	if f.called("thencmd", "") || !f.called("elsecmd", "") {
		t.Fatalf("cond-err: should run else, not then")
	}
	if resp.Halted {
		t.Fatalf("if must absorb the cond err (captured result), not halt")
	}
}

func TestAgentIf_QuestionDeterministicInCheck(t *testing.T) {
	// A read-only question (dir-exists) resolves in check → the if is
	// deterministic (NOT undetermined), unlike an effectful instruction.
	f := newFake()
	f.set(`test -d "$path"`, "", 0) // dir present → ok.present
	f.set("thencmd", "", 0)
	steps := []proto.Step{{If: &proto.IfBlock{
		Cond: &proto.Step{Instruction: "dir-exists", Args: map[string]string{"path": "/opt"}},
		Then: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "thencmd"}}},
	}}}
	resp := serve(t, f, proto.Request{Mode: "check", Steps: steps})

	if resp.Results[0].Category != "ok" {
		t.Fatalf("question condition must be deterministic (present → then), got %+v", resp.Results[0])
	}
}

func TestAgentIf_UndeterminedInCheck(t *testing.T) {
	f := newFake()
	// the cond shell has no guard → would in check → undetermined branch
	resp := serve(t, f, proto.Request{Mode: "check", Steps: []proto.Step{ifStep("condcmd")}})

	if resp.Results[0].Category != "undetermined" {
		t.Fatalf("cond-would in check should be undetermined: %+v", resp.Results[0])
	}
	if f.called("condcmd", "") {
		t.Fatalf("cond shell must not execute in check")
	}
}
