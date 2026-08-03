package lang

import "testing"

func TestParseInventory(t *testing.T) {
	src := `
# staging hosts
defaults = { user: "deploy", port: "22", key: "~/.ssh/id" }
host web1 = { address: "10.0.0.1" }
host db1  = { address: "10.0.0.9", user: "root" }
group web = [web1]
group all = [web1, db1]
`
	inv, err := ParseInventory(src)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Defaults.User != "deploy" || inv.Defaults.Port != "22" {
		t.Fatalf("defaults not applied: %+v", inv.Defaults)
	}
	// db1 overrides user, inherits the rest via Resolve.
	h, ok := inv.Resolve("db1")
	if !ok || h.Address != "10.0.0.9" || h.User != "root" || h.Port != "22" {
		t.Fatalf("resolve db1: %+v (ok=%v)", h, ok)
	}
	if got := inv.Members("all"); len(got) != 2 || got[0] != "web1" || got[1] != "db1" {
		t.Fatalf("group all: %v", got)
	}
}

func TestParsePlan(t *testing.T) {
	src := `
on db { apt.install("postgres") }
on web {
  file-copy("/tmp/nginx.conf", "/etc/nginx.conf")
  parallel {
    apt.install("nginx")
    apt.install("redis")
  }
}
`
	plan, err := ParsePlan(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(plan))
	}
	if plan[0].Target != "db" || plan[0].Steps[0].Args["pkg"] != "postgres" {
		t.Fatalf("block 0: %+v", plan[0])
	}
	web := plan[1]
	if web.Target != "web" || len(web.Steps) != 2 {
		t.Fatalf("block web: %+v", web)
	}
	fc := web.Steps[0]
	if fc.Instruction != "file-copy" || fc.Args["src"] != "/tmp/nginx.conf" || fc.Args["dst"] != "/etc/nginx.conf" {
		t.Fatalf("file-copy step: %+v", fc)
	}
	if par := web.Steps[1]; len(par.Parallel) != 2 {
		t.Fatalf("parallel step: %+v", par)
	}
}

func TestParsePlanVariables(t *testing.T) {
	src := `
owner = "haribo"
on server {
  user-group(owner, "docker")
  dir-owner("/opt", owner)
}
`
	base := map[string]string{}
	plan, err := ParsePlanWithVars(src, base, nil, defaultSig)
	if err != nil {
		t.Fatal(err)
	}
	// bare identifiers become refs; string args stay in Args
	ug := plan[0].Steps[0]
	if ug.Refs["user"] != "owner" || ug.Args["group"] != "docker" {
		t.Fatalf("user-group: %+v", ug)
	}
	if do := plan[0].Steps[1]; do.Refs["owner"] != "owner" {
		t.Fatalf("dir-owner: %+v", do)
	}
	// the top-level binding populated baseVars for later per-host resolution
	if base["owner"] != "haribo" {
		t.Fatalf("baseVars: %+v", base)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]func(string) error{
		`on web { unknown-instr("x") }`:   func(s string) error { _, e := ParsePlan(s); return e },
		`on web { apt.install("a","b") }`: func(s string) error { _, e := ParsePlan(s); return e },
		`host x = { address: "unterm`:     func(s string) error { _, e := ParseInventory(s); return e },
	}
	for src, run := range cases {
		if err := run(src); err == nil {
			t.Fatalf("expected error for: %s", src)
		}
	}
}

func TestParseWithBlock(t *testing.T) {
	base := map[string]string{"who": "root"}
	src := `on s {
		apt.install("nginx") with { version = "1.24", owner = "${who}" }
		shell { echo "$msg" } with { msg = "hi" }
	}`
	plan, err := ParsePlanWithVars(src, base, nil, defaultSig)
	if err != nil {
		t.Fatal(err)
	}
	ai := plan[0].Steps[0]
	if ai.With["version"] != "1.24" || ai.With["owner"] != "root" { // ${who} interpolated at parse
		t.Fatalf("call with: %+v", ai.With)
	}
	sh := plan[0].Steps[1]
	if sh.Instruction != "shell" || sh.With["msg"] != "hi" {
		t.Fatalf("shell with: %+v", sh)
	}
}

func TestParseWithBlock_Errors(t *testing.T) {
	for _, src := range []string{
		`on s { dir-ensure("/o") with { } }`,       // empty with
		`on s { dir-ensure("/o") with { x } }`,     // missing `=`
		`on s { dir-ensure("/o") with { x = "a" }`, // unterminated block
	} {
		if _, err := ParsePlanWithVars(src, nil, nil, defaultSig); err == nil {
			t.Fatalf("expected error for: %s", src)
		}
	}
}
