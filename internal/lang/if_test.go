package lang

import "testing"

func TestParseIf(t *testing.T) {
	src := `on s {
  if dir.ensure("/opt") {
    apt.install("nginx")
  } else {
    apt.install("apache")
  }
}`
	plan, err := parsePlan(src, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := plan[0].Steps[0]
	if st.If == nil {
		t.Fatalf("expected an if step: %+v", st)
	}
	if st.If.Cond.Instruction != "dir.ensure" || st.If.Cond.Args["path"] != "/opt" {
		t.Fatalf("cond: %+v", st.If.Cond)
	}
	if len(st.If.Then) != 1 || st.If.Then[0].Args["pkg"] != "nginx" {
		t.Fatalf("then: %+v", st.If.Then)
	}
	if len(st.If.Else) != 1 || st.If.Else[0].Args["pkg"] != "apache" {
		t.Fatalf("else: %+v", st.If.Else)
	}
}

func TestParseIfNoElse(t *testing.T) {
	plan, err := parsePlan(`on s { if dir.ensure("/opt") { apt.install("nginx") } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := plan[0].Steps[0]; st.If == nil || len(st.If.Else) != 0 {
		t.Fatalf("no-else: %+v", st)
	}
}

func TestParseIfNegation(t *testing.T) {
	plan, err := parsePlan(`on s { if !dir.exists("/opt") { dir.ensure("/opt") } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := plan[0].Steps[0]
	if st.If == nil || !st.If.Negate {
		t.Fatalf("expected a negated if: %+v", st.If)
	}
	if st.If.Cond == nil || st.If.Cond.Instruction != "dir.exists" {
		t.Fatalf("cond: %+v", st.If.Cond)
	}
}

func TestParseIfShellCond(t *testing.T) {
	// the condition may be a shell block
	plan, err := parsePlan(`on s { if shell { test -d /opt } { apt.install("nginx") } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := plan[0].Steps[0]
	if st.If == nil || st.If.Cond.Instruction != "shell" {
		t.Fatalf("shell cond: %+v", st)
	}
	if st.If.Cond.Args["cmd"] != "test -d /opt" {
		t.Fatalf("shell cond cmd: %q", st.If.Cond.Args["cmd"])
	}
}
