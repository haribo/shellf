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
