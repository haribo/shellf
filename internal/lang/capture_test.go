package lang

import "testing"

func TestParseCapture(t *testing.T) {
	plan, err := ParsePlan(`on s {
  x = dir-ensure("/opt")
  if x.changed { apt.install("nginx") }
  if x { apt.install("redis") }
  if x == ok.created { apt.install("etcd") }
  if x != err { apt.install("consul") }
}`)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if steps[0].Bind != "x" || steps[0].Instruction != "dir-ensure" {
		t.Fatalf("capture: %+v", steps[0])
	}
	if r := steps[1].If.CondRef; r == nil || r.Name != "x" || !r.Changed {
		t.Fatalf("if x.changed: %+v", steps[1].If)
	}
	if r := steps[2].If.CondRef; r == nil || r.Category != "ok" || r.Tag != "" { // `if x` → x == ok
		t.Fatalf("if x (sugar): %+v", steps[2].If)
	}
	if r := steps[3].If.CondRef; r == nil || r.Category != "ok" || r.Tag != "created" {
		t.Fatalf("if x == ok.created: %+v", steps[3].If)
	}
	if r := steps[4].If; r.CondRef == nil || r.CondRef.Category != "err" || !r.Negate { // `!=` negates
		t.Fatalf("if x != err: %+v", steps[4].If)
	}
}

func TestParseCaptureDeprecatedFields(t *testing.T) {
	for _, src := range []string{
		`on s { x = dir-ensure("/o") if x.ok { apt.install("y") } }`,  // `.ok` removed → `== ok`
		`on s { x = dir-ensure("/o") if x.err { apt.install("y") } }`, // `.err` removed → `== err`
	} {
		if _, err := ParsePlan(src); err == nil {
			t.Fatalf("expected a deprecation error for: %s", src)
		}
	}
}

func TestParseCatch_CaughtAndErrTest(t *testing.T) {
	plan, err := ParsePlan(`on s {
  x = apt.install("nginx")?
  if x == err.dbLocked { dir-ensure("/o") }
}`)
	if err != nil {
		t.Fatal(err)
	}
	if !plan[0].Steps[0].Caught {
		t.Fatal("`?` should mark the capture caught")
	}
	if r := plan[0].Steps[1].If.CondRef; r.Category != "err" || r.Tag != "dbLocked" {
		t.Fatalf("err test: %+v", r)
	}
}

func TestParseCatch_ErrTestRequiresCatch(t *testing.T) {
	// `== err` without `?` on the source is a dead branch → compile error.
	if _, err := ParsePlan(`on s {
  x = apt.install("nginx")
  if x == err.dbLocked { dir-ensure("/o") }
}`); err == nil {
		t.Fatal("expected an error: == err without a caught source")
	}
	// `!= err` stays free — it is reachable via the ok path.
	if _, err := ParsePlan(`on s {
  x = apt.install("nginx")
  if x != err { dir-ensure("/o") }
}`); err != nil {
		t.Fatalf("!= err should be allowed without `?`: %v", err)
	}
}

func TestParseCatch_InlineErrTest(t *testing.T) {
	plan, err := ParsePlan(`on s { if apt.install("nginx")? == err.dbLocked { dir-ensure("/o") } }`)
	if err != nil {
		t.Fatal(err)
	}
	ib := plan[0].Steps[0].If
	if ib.Cond == nil || !ib.Cond.Caught {
		t.Fatalf("inline cond should be caught: %+v", ib.Cond)
	}
	if ib.Match == nil || ib.Match.Category != "err" || ib.Match.Tag != "dbLocked" {
		t.Fatalf("inline match: %+v", ib.Match)
	}
}

func TestParseCatch_InlineErrTestRequiresCatch(t *testing.T) {
	if _, err := ParsePlan(`on s { if apt.install("nginx") == err.dbLocked { dir-ensure("/o") } }`); err == nil {
		t.Fatal("inline == err without `?` should error")
	}
}

func TestParseAsBlock(t *testing.T) {
	plan, err := ParsePlan(`on s {
  as root { apt.install("nginx") }
  shell as root { systemctl daemon-reload }
}`)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	// Anonymous escalated block: Become=root, Block holds the steps.
	if steps[0].Become != "root" || len(steps[0].Block) != 1 || steps[0].Block[0].Instruction != "apt.install" {
		t.Fatalf("as-block: %+v", steps[0])
	}
	// `shell as root { … }`: the shell step carries Become, body raw-captured.
	if steps[1].Instruction != "shell" || steps[1].Become != "root" {
		t.Fatalf("shell as root: %+v", steps[1])
	}
	if steps[1].Args["cmd"] != "systemctl daemon-reload" {
		t.Fatalf("shell body: %q", steps[1].Args["cmd"])
	}
}

func TestParseOnAsBlock(t *testing.T) {
	plan, err := ParsePlan(`on web as root { dir-ensure("/opt") }`)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 1 || steps[0].Become != "root" || len(steps[0].Block) != 1 {
		t.Fatalf("`on … as root` should wrap the block: %+v", steps)
	}
	if steps[0].Block[0].Instruction != "dir-ensure" {
		t.Fatalf("wrapped step: %+v", steps[0].Block[0])
	}
}

func TestParseIfQualifiedCallStaysCall(t *testing.T) {
	// docker.install() as a condition is a qualified call, not a ref
	plan, err := ParsePlan(`on s { if docker.install() { apt.install("x") } }`)
	if err != nil {
		t.Fatal(err)
	}
	st := plan[0].Steps[0]
	if st.If.CondRef != nil || st.If.Cond == nil || st.If.Cond.Instruction != "docker.install" {
		t.Fatalf("qualified call cond: %+v", st.If)
	}
}

func TestParseUnknownResultField(t *testing.T) {
	if _, err := ParsePlan(`on s { x = dir-ensure("/o") if x.bogus { apt.install("y") } }`); err == nil {
		t.Fatal("expected an error for an unknown result field")
	}
}

func TestParseCaptureRejectsIf(t *testing.T) {
	if _, err := ParsePlan(`on s { x = if dir-ensure("/o") { apt.install("y") } }`); err == nil {
		t.Fatal("expected an error capturing an if into a variable")
	}
}
