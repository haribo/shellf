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

func caughtCap(cmd, bind string) proto.Step {
	s := capture(cmd, "", bind)
	s.Caught = true
	return s
}

func shellStep(cmd string) proto.Step {
	return proto.Step{Instruction: "shell", Args: map[string]string{"cmd": cmd}}
}

func TestAgentCatch_HandledContinues(t *testing.T) {
	// x = shell{fail}? exit 1 → err, caught. `if x == err` handles → then runs, no halt.
	f := newFake()
	f.set("fail", "", 1)
	f.set("thencmd", "", 0)
	f.set("after", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		caughtCap("fail", "x"),
		ifRef("x", "err"),
		shellStep("after"),
	}})
	if !f.called("thencmd", "") {
		t.Fatal("caught + handled → then should run")
	}
	if !f.called("after", "") {
		t.Fatal("caught + handled → the sequence should continue past the if")
	}
	if resp.Halted {
		t.Fatal("caught + handled → must not halt")
	}
}

func TestAgentCatch_UncoveredHalts(t *testing.T) {
	// x = shell{fail}? → err. `if x == ok` does not cover it, no else → halt.
	f := newFake()
	f.set("fail", "", 1)
	f.set("after", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		caughtCap("fail", "x"),
		ifRef("x", "ok"),
		shellStep("after"),
	}})
	if !resp.Halted {
		t.Fatal("caught but uncovered → must halt")
	}
	if f.called("after", "") {
		t.Fatal("halt → later steps must not run")
	}
}

func TestAgentCatch_ElseCatchAll(t *testing.T) {
	// x = shell{fail}? → err with no specific tag. `if x == err.specific {} else {}`
	// → tag doesn't match, else catches → no halt.
	f := newFake()
	f.set("fail", "", 1)
	f.set("elsecmd", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		caughtCap("fail", "x"),
		{If: &proto.IfBlock{
			CondRef: &proto.ResultRef{Name: "x", Category: "err", Tag: "specific"},
			Then:    []proto.Step{shellStep("thencmd")},
			Else:    []proto.Step{shellStep("elsecmd")},
		}},
	}})
	if resp.Halted {
		t.Fatal("else catch-all → must not halt")
	}
	if !f.called("elsecmd", "") {
		t.Fatal("err not matching the tag → else should run")
	}
	if f.called("thencmd", "") {
		t.Fatal("tag mismatch → then must not run")
	}
}

func inlineCaught(cmd, tag string, then, els []proto.Step) proto.Step {
	ib := &proto.IfBlock{
		Cond:  &proto.Step{Instruction: "shell", Args: map[string]string{"cmd": cmd}, Caught: true},
		Match: &proto.ResultRef{Category: "err", Tag: tag},
		Then:  then,
		Else:  els,
	}
	return proto.Step{If: ib}
}

func TestAgentCatch_InlineHandled(t *testing.T) {
	// if shell{fail}? == err { then } — err matches → then runs, sequence continues.
	f := newFake()
	f.set("fail", "", 1)
	f.set("thencmd", "", 0)
	f.set("after", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		inlineCaught("fail", "", []proto.Step{shellStep("thencmd")}, nil),
		shellStep("after"),
	}})
	if !f.called("thencmd", "") {
		t.Fatal("inline caught + matched → then should run")
	}
	if !f.called("after", "") {
		t.Fatal("handled → sequence should continue")
	}
	if resp.Halted {
		t.Fatal("handled → must not halt")
	}
}

func TestAgentCatch_InlineUncoveredHalts(t *testing.T) {
	// if shell{fail}? == err.nevermatches { then } — tag mismatch, no else → halt.
	f := newFake()
	f.set("fail", "", 1)
	f.set("after", "", 0)
	resp := serve(t, f, proto.Request{Mode: "apply", Steps: []proto.Step{
		inlineCaught("fail", "nevermatches", []proto.Step{shellStep("thencmd")}, nil),
		shellStep("after"),
	}})
	if !resp.Halted {
		t.Fatal("inline caught but uncovered → must halt")
	}
	if f.called("after", "") {
		t.Fatal("halt → later steps must not run")
	}
	if f.called("thencmd", "") {
		t.Fatal("tag mismatch → then must not run")
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
