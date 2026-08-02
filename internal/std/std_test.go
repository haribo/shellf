package std

import (
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// fakeExec drives the ADR-0013 observe/apply split: any shell whose script
// contains applyMatch is the apply (pass 2) and returns `apply`; every other
// shell is an observe read (pass 1) and returns `observe`. A converged run has
// its observe field satisfied (exit 0 for a truthy field, or a matching stdout
// for a value field); a drift has it unsatisfied.
type fakeExec struct {
	observe    engine.ShellResult
	apply      engine.ShellResult
	applyMatch string
	calls      []string
}

func (f *fakeExec) As(string) engine.Executor    { return f }
func (f *fakeExec) Using(string) engine.Executor { return f }

func (f *fakeExec) Shell(script string, _ engine.Env) engine.ShellResult {
	f.calls = append(f.calls, script)
	if f.applyMatch != "" && strings.Contains(script, f.applyMatch) {
		return f.apply
	}
	return f.observe
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

// converged is an observe result that satisfies a truthy field (exit 0).
var converged = engine.ShellResult{Exit: 0}

// drift is an observe result that fails a truthy field (exit 1).
var drift = engine.ShellResult{Exit: 1}

func TestStd_IntrinsicBecome(t *testing.T) {
	for _, name := range []string{"apt.install", "service", "ufw.enable", "ufw.open", "docker.install", "docker.network", "user-group", "dir-owner"} {
		def, ok := Lookup(name)
		if !ok || def.Become != "root" {
			t.Fatalf("%s should be intrinsic `as root`: ok=%v become=%q", name, ok, def.Become)
		}
	}
	for _, name := range []string{"dir-ensure", "file-write", "dir-exists"} {
		if def, _ := Lookup(name); def.Become != "" {
			t.Fatalf("%s should not be intrinsic: become=%q", name, def.Become)
		}
	}
}

func TestStdlib_AllPresent(t *testing.T) {
	for _, name := range []string{
		"apt.install", "file-download", "archive-extract", "git-clone",
		"dir-ensure", "file-write", "file-line", "file-delete",
		"user-group", "dir-owner", "dir-exists", "file-exists", "service",
		"docker.install", "docker.network", "docker.compose-up", "ufw.open", "ufw.enable",
	} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("missing def %q", name)
		}
	}
}

// A converged truthy-field resource skips apply with the uniform `ok.already`
// (ADR-0013); drift runs apply to the def's own success tag.
func TestTruthyResources(t *testing.T) {
	cases := []struct {
		name, apply, tag string
		args             map[string]string
	}{
		{"apt.install", "apt-get install", "installed", map[string]string{"pkg": "nginx"}},
		{"docker.compose-up", "compose up", "up", map[string]string{"dir": "/opt/app"}},
		{"docker.install", "get.docker.com", "installed", nil},
		{"docker.network", "network create", "created", map[string]string{"name": "web"}},
		{"ufw.enable", "--force enable", "enabled", nil},
		{"ufw.open", "ufw allow", "opened", map[string]string{"port": "443", "proto": "tcp"}},
		{"dir-ensure", "mkdir", "created", map[string]string{"path": "/opt/x"}},
		{"file-line", ">>", "added", map[string]string{"path": "/etc/x", "line": "z"}},
		{"file-delete", "rm -rf", "deleted", map[string]string{"path": "/tmp/gone"}},
		{"archive-extract", "tar", "extracted", map[string]string{"src": "/a.tgz", "dst": "/opt"}},
		{"user-group", "usermod", "added", map[string]string{"user": "x", "group": "docker"}},
		{"file-download", "curl", "downloaded", map[string]string{"url": "http://x", "dst": "/d", "sha256": "abc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// converged → skip
			if got := eval(t, c.name, c.args, &fakeExec{observe: converged, applyMatch: c.apply}, engine.Apply).String(); got != "ok.already" {
				t.Fatalf("converged: got %s, want ok.already", got)
			}
			// drift → apply success
			if got := eval(t, c.name, c.args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 0}, applyMatch: c.apply}, engine.Apply).String(); got != "ok."+c.tag {
				t.Fatalf("drift apply: got %s, want ok.%s", got, c.tag)
			}
			// drift + apply fails → err.runtime
			if got := eval(t, c.name, c.args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 1}, applyMatch: c.apply}, engine.Apply).String(); got != "err.runtime" {
				t.Fatalf("drift apply-fail: got %s, want err.runtime", got)
			}
			// check + drift → would.<tag>, apply never runs
			f := &fakeExec{observe: drift, applyMatch: c.apply}
			if got := eval(t, c.name, c.args, f, engine.Check).String(); got != "would."+c.tag {
				t.Fatalf("check drift: got %s, want would.%s", got, c.tag)
			}
			for _, s := range f.calls {
				if strings.Contains(s, c.apply) {
					t.Fatal("check must not run the apply shell")
				}
			}
		})
	}
}

func TestValueResource_Service(t *testing.T) {
	args := map[string]string{"name": "nginx", "running": "true", "enabled": "true"}
	// Both observe fields (is-active/is-enabled) true == desired → converged.
	if got := eval(t, "service", args, &fakeExec{observe: converged, applyMatch: "systemctl start"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("service converged: got %s, want ok.already", got)
	}
	// Not running → running "false" ≠ "true" → drift → apply.
	if got := eval(t, "service", args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 0}, applyMatch: "systemctl start"}, engine.Apply).String(); got != "ok.converged" {
		t.Fatalf("service drift: got %s, want ok.converged", got)
	}
	if got := eval(t, "service", args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 1}, applyMatch: "systemctl start"}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("service apply-fail: got %s, want err.runtime", got)
	}
}

func TestValueResource_DirOwner(t *testing.T) {
	args := map[string]string{"path": "/opt", "owner": "haribo:haribo"}
	// observed owner matches the argument → converged.
	if got := eval(t, "dir-owner", args, &fakeExec{observe: engine.ShellResult{Stdout: "haribo:haribo\n"}, applyMatch: "chown"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("dir-owner converged: got %s, want ok.already", got)
	}
	// observed owner differs → chown.
	if got := eval(t, "dir-owner", args, &fakeExec{observe: engine.ShellResult{Stdout: "root:root\n"}, apply: engine.ShellResult{Exit: 0}, applyMatch: "chown"}, engine.Apply).String(); got != "ok.changed" {
		t.Fatalf("dir-owner drift: got %s, want ok.changed", got)
	}
}

func TestValueResource_GitClone(t *testing.T) {
	args := map[string]string{"url": "http://x/r", "dst": "/opt/r"}
	// origin remote matches the wanted url → converged.
	if got := eval(t, "git-clone", args, &fakeExec{observe: engine.ShellResult{Stdout: "http://x/r\n"}, applyMatch: "git clone"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("git-clone matching: got %s, want ok.already", got)
	}
	// absent (empty remote) → clone.
	if got := eval(t, "git-clone", args, &fakeExec{observe: engine.ShellResult{Stdout: ""}, apply: engine.ShellResult{Exit: 0}, applyMatch: "git clone"}, engine.Apply).String(); got != "ok.cloned" {
		t.Fatalf("git-clone absent: got %s, want ok.cloned", got)
	}
	// wrong remote → drift → clone fails on an existing dst → err.runtime
	// (the v1 tradeoff: no precise err.wrongRemote).
	if got := eval(t, "git-clone", args, &fakeExec{observe: engine.ShellResult{Stdout: "http://other\n"}, apply: engine.ShellResult{Exit: 128}, applyMatch: "git clone"}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("git-clone wrong remote: got %s, want err.runtime", got)
	}
}

func TestFileWrite_ContentSync(t *testing.T) {
	args := map[string]string{"path": "/etc/x", "content": "a\nb\n"}
	// synced (content matches) → skip.
	if got := eval(t, "file-write", args, &fakeExec{observe: converged, applyMatch: " > "}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("file-write synced: got %s, want ok.already", got)
	}
	// differs → write.
	if got := eval(t, "file-write", args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 0}, applyMatch: " > "}, engine.Apply).String(); got != "ok.written" {
		t.Fatalf("file-write drift: got %s, want ok.written", got)
	}
}

func TestReadOnlyQuestions(t *testing.T) {
	// A question resolves in pass 1: deterministic even in CHECK (never `would`).
	if got := eval(t, "dir-exists", map[string]string{"path": "/opt"}, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.present" {
		t.Fatalf("dir-exists present: got %s, want ok.present", got)
	}
	if got := eval(t, "dir-exists", map[string]string{"path": "/opt"}, &fakeExec{observe: drift}, engine.Check).String(); got != "err.absent" {
		t.Fatalf("dir-exists absent: got %s, want err.absent", got)
	}
	if got := eval(t, "file-exists", map[string]string{"path": "/etc/x"}, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.present" {
		t.Fatalf("file-exists present: got %s, want ok.present", got)
	}
}
