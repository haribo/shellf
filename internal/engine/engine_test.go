package engine

import "testing"

// fakeExecutor is a deterministic lookup table — no real shell.
type fakeExecutor struct {
	responses map[string]ShellResult
	calls     []string
}

func (f *fakeExecutor) Shell(script string, _ Env) ShellResult {
	f.calls = append(f.calls, script)
	if r, ok := f.responses[script]; ok {
		return r
	}
	return ShellResult{Exit: 127} // command not found
}

func (f *fakeExecutor) called(script string) bool {
	for _, c := range f.calls {
		if c == script {
			return true
		}
	}
	return false
}

const (
	dpkgQuery = `dpkg -s "$pkg"`
	aptGet    = `apt-get install -y "$pkg"`
)

func TestApply_NotInstalled_Installs(t *testing.T) {
	f := &fakeExecutor{responses: map[string]ShellResult{
		dpkgQuery: {Exit: 1}, // not installed
		aptGet:    {Exit: 0}, // install succeeds
	}}
	if got := Run(AptInstall{Pkg: "nginx"}, f, Apply).String(); got != "ok.installed" {
		t.Fatalf("got %s, want ok.installed", got)
	}
}

func TestApply_AlreadyInstalled_Skips(t *testing.T) {
	f := &fakeExecutor{responses: map[string]ShellResult{
		dpkgQuery: {Exit: 0}, // already installed
	}}
	if got := Run(AptInstall{Pkg: "nginx"}, f, Apply).String(); got != "ok.alreadyInstalled" {
		t.Fatalf("got %s, want ok.alreadyInstalled", got)
	}
	if f.called(aptGet) {
		t.Fatal("apply ran despite guard skip")
	}
}

// The core property: Check mode decides but never mutates.
func TestCheck_NotInstalled_WouldNotMutate(t *testing.T) {
	f := &fakeExecutor{responses: map[string]ShellResult{
		dpkgQuery: {Exit: 1},
	}}
	if got := Run(AptInstall{Pkg: "nginx"}, f, Check).String(); got != "would.installed" {
		t.Fatalf("got %s, want would.installed", got)
	}
	if f.called(aptGet) {
		t.Fatal("check mode mutated: apt-get ran")
	}
}

func TestApply_InstallFails_ErrRuntime(t *testing.T) {
	f := &fakeExecutor{responses: map[string]ShellResult{
		dpkgQuery: {Exit: 1},
		aptGet:    {Exit: 100, Stderr: "E: locked"},
	}}
	res := Run(AptInstall{Pkg: "nginx"}, f, Apply)
	if res.String() != "err.runtime" {
		t.Fatalf("got %s, want err.runtime", res)
	}
	if res.Shell == nil || res.Shell.Exit != 100 {
		t.Fatalf("err.runtime should carry the ShellResult payload")
	}
}

func TestPreCheck_EmptyPkg_Errors(t *testing.T) {
	f := &fakeExecutor{}
	if got := Run(AptInstall{Pkg: ""}, f, Apply).String(); got != "err.pkgMustNotBeNull" {
		t.Fatalf("got %s, want err.pkgMustNotBeNull", got)
	}
	if len(f.calls) != 0 {
		t.Fatal("pre-check must not touch the executor")
	}
}
