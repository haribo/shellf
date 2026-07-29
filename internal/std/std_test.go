package std

import (
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// fakeExec returns guardExit for the first shell (the read-only guard, pass 1)
// and applyExit for any later shell (the apply, pass 2). Robust for the simple
// one-guard-one-apply defs here, no script pattern-matching.
type fakeExec struct {
	guardExit, applyExit int
	n                    int
	calls                []string
}

func (f *fakeExec) Shell(script string, _ engine.Env) engine.ShellResult {
	f.calls = append(f.calls, script)
	f.n++
	if f.n == 1 {
		return engine.ShellResult{Exit: f.guardExit}
	}
	return engine.ShellResult{Exit: f.applyExit}
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
	for _, name := range []string{
		"apt-install", "file-download", "archive-extract", "git-clone",
		"dir-ensure", "file-write", "file-line", "file-delete",
		"docker.install", "docker.network", "docker.compose-up", "ufw.open",
	} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("missing def %q", name)
		}
	}
}

func TestComposeAndUfw(t *testing.T) {
	// compose-up has no guard: its single shell is the apply (n==1 → guardExit here).
	if got := eval(t, "docker.compose-up", map[string]string{"dir": "/opt/app"}, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.up" {
		t.Fatalf("docker.compose-up: got %s, want ok.up", got)
	}
	// ufw.open: rule absent → allow
	if got := eval(t, "ufw.open", map[string]string{"port": "443/tcp"}, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.opened" {
		t.Fatalf("ufw.open apply: got %s, want ok.opened", got)
	}
	// ufw.open: rule present → skip
	if got := eval(t, "ufw.open", map[string]string{"port": "443/tcp"}, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("ufw.open guard-ok: got %s, want ok.already", got)
	}
}

func TestDownloadFile(t *testing.T) {
	args := map[string]string{"url": "http://x/a.tgz", "dst": "/tmp/a.tgz", "sha256": "abc"}

	if got := eval(t, "file-download", args, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("guard-ok: got %s, want ok.already", got)
	}
	if got := eval(t, "file-download", args, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.downloaded" {
		t.Fatalf("apply: got %s, want ok.downloaded", got)
	}
	if got := eval(t, "file-download", args, &fakeExec{guardExit: 1, applyExit: 22}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("apply-fail: got %s, want err.runtime", got)
	}
	f := &fakeExec{guardExit: 1}
	if got := eval(t, "file-download", args, f, engine.Check).String(); got != "would.downloaded" {
		t.Fatalf("check: got %s, want would.downloaded", got)
	}
	if len(f.calls) != 1 { // only the guard ran; apply skipped in check
		t.Fatalf("check mode ran %d shells, want 1 (guard only)", len(f.calls))
	}
}

func TestArchiveAndClone(t *testing.T) {
	if got := eval(t, "archive-extract", map[string]string{"src": "/a.tgz", "dst": "/opt"}, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.extracted" {
		t.Fatalf("archive-extract: got %s, want ok.extracted", got)
	}
	if got := eval(t, "git-clone", map[string]string{"url": "http://x/r", "dst": "/opt/r"}, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("git-clone guard: got %s, want ok.already", got)
	}
}

func TestFileWriteDirDelete(t *testing.T) {
	// file-write: content already matches → skip
	if got := eval(t, "file-write", map[string]string{"path": "/etc/x", "content": "a\nb\n"}, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("file-write guard-ok: got %s, want ok.already", got)
	}
	// file-write: differs → write
	if got := eval(t, "file-write", map[string]string{"path": "/etc/x", "content": "a\nb\n"}, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.written" {
		t.Fatalf("file-write apply: got %s, want ok.written", got)
	}
	// dir-ensure: absent → create
	if got := eval(t, "dir-ensure", map[string]string{"path": "/opt/x"}, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.created" {
		t.Fatalf("dir-ensure: got %s, want ok.created", got)
	}
	// file-delete: already gone → skip
	if got := eval(t, "file-delete", map[string]string{"path": "/tmp/gone"}, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("file-delete guard-ok: got %s, want ok.already", got)
	}
}

func TestDockerPackage(t *testing.T) {
	if got := eval(t, "docker.install", nil, &fakeExec{guardExit: 0}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("docker.install: got %s, want ok.already", got)
	}
	if got := eval(t, "docker.network", map[string]string{"name": "web"}, &fakeExec{guardExit: 1, applyExit: 0}, engine.Apply).String(); got != "ok.created" {
		t.Fatalf("docker.network: got %s, want ok.created", got)
	}
}
