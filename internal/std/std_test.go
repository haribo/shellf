package std

import (
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// fakeExec returns applyExit for effectful commands (curl/tar/git clone),
// guardExit otherwise (the read-only guard).
type fakeExec struct {
	guardExit, applyExit int
	calls                []string
}

func (f *fakeExec) Shell(script string, _ engine.Env) engine.ShellResult {
	f.calls = append(f.calls, script)
	for _, effect := range []string{"curl", "tar xzf", "git clone"} {
		if strings.Contains(script, effect) {
			return engine.ShellResult{Exit: f.applyExit}
		}
	}
	return engine.ShellResult{Exit: f.guardExit}
}

func eval(t *testing.T, name string, args map[string]string, f *fakeExec, mode engine.Mode) engine.Result {
	t.Helper()
	def, ok := Lookup(name)
	if !ok {
		t.Fatalf("stdlib def %q not found", name)
	}
	res, err := lang.EvalDef(def, args, f, mode)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestStdlib_AllPresent(t *testing.T) {
	for _, name := range []string{"apt-install", "download-file", "untar", "git-clone"} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("missing stdlib def %q", name)
		}
	}
}

func TestDownloadFile(t *testing.T) {
	args := map[string]string{"url": "http://x/a.tgz", "dst": "/tmp/a.tgz", "sha256": "abc"}

	// hash already matches → skip
	if got := eval(t, "download-file", args, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("guard-ok: got %s, want ok.already", got)
	}
	// not present → download
	if got := eval(t, "download-file", args, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.downloaded" {
		t.Fatalf("apply: got %s, want ok.downloaded", got)
	}
	// download fails → err.runtime
	if got := eval(t, "download-file", args, &fakeExec{guardExit: 1, applyExit: 22}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("apply-fail: got %s, want err.runtime", got)
	}
	// check mode never runs curl
	f := &fakeExec{guardExit: 1}
	if got := eval(t, "download-file", args, f, engine.Check).String(); got != "would.downloaded" {
		t.Fatalf("check: got %s, want would.downloaded", got)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "curl") {
			t.Fatal("check mode ran curl")
		}
	}
}

func TestUntarAndClone(t *testing.T) {
	if got := eval(t, "untar", map[string]string{"src": "/a.tgz", "dst": "/opt"}, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.extracted" {
		t.Fatalf("untar: got %s, want ok.extracted", got)
	}
	if got := eval(t, "git-clone", map[string]string{"url": "http://x/r", "dst": "/opt/r"}, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("git-clone guard: got %s, want ok.already", got)
	}
}
