package engine

import "testing"

type svcFake struct {
	resp  map[string]ShellResult
	calls map[string]bool
}

func newSvcFake() *svcFake {
	return &svcFake{resp: map[string]ShellResult{}, calls: map[string]bool{}}
}

func (f *svcFake) Shell(script string, _ Env) ShellResult {
	f.calls[script] = true
	if r, ok := f.resp[script]; ok {
		return r
	}
	return ShellResult{Exit: 1}
}

const (
	isActive  = `systemctl is-active --quiet "$unit"`
	isEnabled = `systemctl is-enabled --quiet "$unit"`
	svcApply  = `systemctl "$act" "$unit" && systemctl "$en" "$unit"`
)

func TestService_BothDimensionsMatch_Skips(t *testing.T) {
	f := newSvcFake()
	f.resp[isActive] = ShellResult{Exit: 0}  // running
	f.resp[isEnabled] = ShellResult{Exit: 0} // enabled
	got := Run(Service{Unit: "nginx", Running: true, Enabled: true}, f, Apply).String()
	if got != "ok.alreadyConverged" {
		t.Fatalf("got %s, want ok.alreadyConverged", got)
	}
	if f.calls[svcApply] {
		t.Fatal("apply ran despite both dimensions matching")
	}
}

func TestService_OneDimensionDiffers_Applies(t *testing.T) {
	f := newSvcFake()
	f.resp[isActive] = ShellResult{Exit: 3}  // stopped, but we want running
	f.resp[isEnabled] = ShellResult{Exit: 0} // enabled (already fine)
	f.resp[svcApply] = ShellResult{Exit: 0}
	got := Run(Service{Unit: "nginx", Running: true, Enabled: true}, f, Apply).String()
	if got != "ok.converged" {
		t.Fatalf("got %s, want ok.converged", got)
	}
}

func TestService_Check_WouldNotMutate(t *testing.T) {
	f := newSvcFake()
	f.resp[isActive] = ShellResult{Exit: 3} // differs from desired
	f.resp[isEnabled] = ShellResult{Exit: 0}
	got := Run(Service{Unit: "nginx", Running: true, Enabled: true}, f, Check).String()
	if got != "would.converged" {
		t.Fatalf("got %s, want would.converged", got)
	}
	if f.calls[svcApply] {
		t.Fatal("check mode mutated")
	}
}
