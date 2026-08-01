package engine

import "testing"

const (
	netCreate  = `docker network create web`
	netInspect = `docker network inspect web`
)

func TestShell_NoGuard_Runs(t *testing.T) {
	f := &fcFake{responses: map[string]ShellResult{`docker compose up -d`: {Exit: 0}}}
	if got := Run(Shell{Cmd: "docker compose up -d"}, f, Apply).String(); got != "ok.ran" {
		t.Fatalf("got %s, want ok.ran", got)
	}
}

func TestShell_UnlessSatisfied_Skips(t *testing.T) {
	f := &fcFake{responses: map[string]ShellResult{netInspect: {Exit: 0}}} // guard satisfied
	got := Run(Shell{Cmd: netCreate, Unless: netInspect}, f, Apply).String()
	if got != "ok.alreadySatisfied" {
		t.Fatalf("got %s, want ok.alreadySatisfied", got)
	}
	if f.calls[netCreate] {
		t.Fatal("command ran despite the guard being satisfied")
	}
}

func TestShell_Check_WouldNotMutate(t *testing.T) {
	f := &fcFake{responses: map[string]ShellResult{netInspect: {Exit: 1}}} // guard not satisfied
	got := Run(Shell{Cmd: netCreate, Unless: netInspect}, f, Check).String()
	if got != "would.ran" {
		t.Fatalf("got %s, want would.ran", got)
	}
	if f.calls[netCreate] {
		t.Fatal("check mode ran the command")
	}
}

// envFake records the env of the last shell it ran.
type envFake struct {
	gotEnv Env
	result ShellResult
}

func (e *envFake) Shell(_ string, env Env) ShellResult { e.gotEnv = env; return e.result }
func (e *envFake) As(string) Executor                  { return e }
func (e *envFake) Using(string) Executor               { return e }

func TestShell_Apply_InjectsEnv(t *testing.T) {
	f := &envFake{result: ShellResult{Exit: 0}}
	Shell{Cmd: "echo $name", Env: Env{"name": "alice"}}.Apply(f)
	if f.gotEnv["name"] != "alice" {
		t.Fatalf("Apply must pass Env to the executor (#106), got %+v", f.gotEnv)
	}
}

func TestShell_Guard_InjectsEnv(t *testing.T) {
	f := &envFake{result: ShellResult{Exit: 1}} // guard fails → command would run
	Shell{Cmd: "act", Unless: "test -f $path", Env: Env{"path": "/tmp/x"}}.Guard(f)
	if f.gotEnv["path"] != "/tmp/x" {
		t.Fatalf("Guard must pass Env to the executor (#106), got %+v", f.gotEnv)
	}
}
