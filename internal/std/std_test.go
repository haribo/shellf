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
	res, err := lang.EvalDefFull(def, args, nil, nil, f, mode, nil, nil, nil, nil, nil)
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
	for _, name := range []string{"apt.install", "service.ensure", "ufw.enable", "ufw.open", "docker.install", "docker.network", "user.group", "dir.owner"} {
		def, ok := Lookup(name)
		if !ok || def.Become != "root" {
			t.Fatalf("%s should be intrinsic `as root`: ok=%v become=%q", name, ok, def.Become)
		}
	}
	for _, name := range []string{"dir.ensure", "file.write", "dir.exists"} {
		if def, _ := Lookup(name); def.Become != "" {
			t.Fatalf("%s should not be intrinsic: become=%q", name, def.Become)
		}
	}
}

func TestStdlib_AllPresent(t *testing.T) {
	for _, name := range []string{
		"apt.install", "file.download", "archive.extract", "git.clone",
		"dir.ensure", "file.write", "file.line", "file.delete",
		"user.group", "dir.owner", "dir.exists", "file.exists", "service.ensure",
		"docker.install", "docker.network", "docker.compose-up", "ufw.open", "ufw.enable",
		// deployment dogfood additions
		"file.mode", "file.replace", "systemd.daemon-reload", "service.restart", "service.reload",
		"apt.update", "ufw.default", "user.ensure", "git.sync", "http.check", "http.wait-for",
		"docker.compose-restart",
	} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("missing def %q", name)
		}
	}
}

func TestDeployDefs_ActionShaped(t *testing.T) {
	// Handlers/reloads always apply (no observe).
	cases := []struct {
		name, apply, tag string
		args             map[string]string
	}{
		{"systemd.daemon-reload", "daemon-reload", "reloaded", nil},
		{"service.restart", "restart", "restarted", map[string]string{"name": "sshd"}},
		{"service.reload", "reload", "reloaded", map[string]string{"name": "nginx"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eval(t, c.name, c.args, &fakeExec{apply: converged, applyMatch: c.apply}, engine.Apply).String(); got != "ok."+c.tag {
				t.Fatalf("action-shaped apply: got %s, want ok.%s", got, c.tag)
			}
		})
	}
}

func TestDeployDefs_TruthyAndValue(t *testing.T) {
	// A truthy-field resource (file-replace): converged skips, drift applies.
	rep := map[string]string{"path": "/etc/x", "key": "K", "value": "v"}
	if got := eval(t, "file.replace", rep, &fakeExec{observe: converged, applyMatch: "awk"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("file-replace converged: got %s", got)
	}
	if got := eval(t, "file.replace", rep, &fakeExec{observe: drift, apply: converged, applyMatch: "awk"}, engine.Apply).String(); got != "ok.set" {
		t.Fatalf("file-replace drift: got %s", got)
	}
	// A value-field resource (file-mode): observed mode matches the arg → skip.
	fm := map[string]string{"path": "/b", "mode": "755"}
	if got := eval(t, "file.mode", fm, &fakeExec{observe: engine.ShellResult{Stdout: "755\n"}, applyMatch: "chmod"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("file-mode converged: got %s", got)
	}
	if got := eval(t, "file.mode", fm, &fakeExec{observe: engine.ShellResult{Stdout: "644\n"}, apply: converged, applyMatch: "chmod"}, engine.Apply).String(); got != "ok.changed" {
		t.Fatalf("file-mode drift: got %s", got)
	}
}

func TestDeployDefs_Questions(t *testing.T) {
	// http-check is a read-only question (check phase): match → ok, else err.
	args := map[string]string{"url": "http://x", "status": "200"}
	if got := eval(t, "http.check", args, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.match" {
		t.Fatalf("http-check match: got %s", got)
	}
	if got := eval(t, "http.check", args, &fakeExec{observe: drift}, engine.Check).String(); got != "err.mismatch" {
		t.Fatalf("http-check mismatch: got %s", got)
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
		{"docker.install", "get.docker.com", "installed", nil},
		{"docker.network", "network create", "created", map[string]string{"name": "web"}},
		{"ufw.enable", "--force enable", "enabled", nil},
		{"ufw.open", "ufw allow", "opened", map[string]string{"port": "443", "proto": "tcp"}},
		{"dir.ensure", "mkdir", "created", map[string]string{"path": "/opt/x"}},
		{"file.line", ">>", "added", map[string]string{"path": "/etc/x", "line": "z"}},
		{"file.delete", "rm -rf", "deleted", map[string]string{"path": "/tmp/gone"}},
		{"archive.extract", "tar", "extracted", map[string]string{"src": "/a.tgz", "dst": "/opt"}},
		{"user.group", "usermod", "added", map[string]string{"user": "x", "group": "docker"}},
		{"file.download", "curl", "downloaded", map[string]string{"url": "http://x", "dst": "/d", "sha256": "abc"}},
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

func TestComposeUp_ActionShaped(t *testing.T) {
	// No observe: `docker compose up -d` is itself idempotent, so it always runs
	// (a "is it running" guard would wrongly skip a compose/image change).
	if got := eval(t, "docker.compose-up", map[string]string{"dir": "/opt/app", "build": "false"},
		&fakeExec{apply: converged, applyMatch: "compose up"}, engine.Apply).String(); got != "ok.up" {
		t.Fatalf("compose-up should always apply: got %s, want ok.up", got)
	}
	// build=true rebuilds; a failing apply surfaces err.runtime.
	if got := eval(t, "docker.compose-up", map[string]string{"dir": "/opt/app", "build": "true"},
		&fakeExec{apply: drift, applyMatch: "compose up"}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("compose-up apply-fail: got %s, want err.runtime", got)
	}
	// Omitting the optional `build` still resolves (default) and runs.
	if got := eval(t, "docker.compose-up", map[string]string{"dir": "/opt/app"},
		&fakeExec{apply: converged, applyMatch: "compose up"}, engine.Apply).String(); got != "ok.up" {
		t.Fatalf("compose-up without build: got %s, want ok.up", got)
	}
}

// #287 — a restart is a handler: no observe, it always acts, and the caller
// gates it on `.changed` (same contract as service-restart).
func TestComposeRestart_ActionShaped(t *testing.T) {
	// Whole stack: omitting the optional `service` resolves to the default and runs.
	if got := eval(t, "docker.compose-restart", map[string]string{"dir": "/opt/app"},
		&fakeExec{apply: converged, applyMatch: "compose restart"}, engine.Apply).String(); got != "ok.restarted" {
		t.Fatalf("compose-restart whole stack: got %s, want ok.restarted", got)
	}
	// A named service restarts just that one.
	if got := eval(t, "docker.compose-restart", map[string]string{"dir": "/opt/app", "service.ensure": "grafana"},
		&fakeExec{apply: converged, applyMatch: "compose restart"}, engine.Apply).String(); got != "ok.restarted" {
		t.Fatalf("compose-restart named service: got %s, want ok.restarted", got)
	}
	// A failing apply surfaces err.runtime.
	if got := eval(t, "docker.compose-restart", map[string]string{"dir": "/opt/app", "service.ensure": "grafana"},
		&fakeExec{apply: drift, applyMatch: "compose restart"}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("compose-restart apply-fail: got %s, want err.runtime", got)
	}
	// `--dry-run` previews via the read-only `--dry-run` and never runs the real
	// restart: every shell it issues carries the flag.
	f := &fakeExec{observe: converged, apply: converged, applyMatch: "compose restart"}
	if got := eval(t, "docker.compose-restart", map[string]string{"dir": "/opt/app", "service.ensure": "grafana"},
		f, engine.Check).String(); got != "would.restarted" {
		t.Fatalf("compose-restart check: got %s, want would.restarted", got)
	}
	if len(f.calls) == 0 {
		t.Fatal("check must issue the preview shell (a missing preview block would pass vacuously)")
	}
	for _, s := range f.calls {
		if !strings.Contains(s, "--dry-run") {
			t.Fatalf("check must not run the real restart, got shell: %s", s)
		}
	}
}

func TestValueResource_Service(t *testing.T) {
	args := map[string]string{"name": "nginx", "running": "true", "enabled": "true"}
	// Both observe fields (is-active/is-enabled) true == desired → converged.
	if got := eval(t, "service.ensure", args, &fakeExec{observe: converged, applyMatch: "systemctl start"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("service converged: got %s, want ok.already", got)
	}
	// Not running → running "false" ≠ "true" → drift → apply.
	if got := eval(t, "service.ensure", args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 0}, applyMatch: "systemctl start"}, engine.Apply).String(); got != "ok.converged" {
		t.Fatalf("service drift: got %s, want ok.converged", got)
	}
	if got := eval(t, "service.ensure", args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 1}, applyMatch: "systemctl start"}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("service apply-fail: got %s, want err.runtime", got)
	}
}

func TestValueResource_DirOwner(t *testing.T) {
	args := map[string]string{"path": "/opt", "owner": "haribo:haribo"}
	// The comparison happens in the shell now, not by matching an observed string against
	// the argument (#367): `stat` always prints `user:group`, so a plan asking for a bare
	// user never matched and the def reported `changed` forever. The fake therefore
	// answers the comparison's exit code rather than an owner.
	//
	// owner matches → converged.
	if got := eval(t, "dir.owner", args, &fakeExec{observe: engine.ShellResult{Exit: 0}, applyMatch: "chown"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("dir-owner converged: got %s, want ok.already", got)
	}
	// owner differs → chown.
	if got := eval(t, "dir.owner", args, &fakeExec{observe: engine.ShellResult{Exit: 1}, apply: engine.ShellResult{Exit: 0}, applyMatch: "chown"}, engine.Apply).String(); got != "ok.changed" {
		t.Fatalf("dir-owner drift: got %s, want ok.changed", got)
	}
}

func TestValueResource_GitClone(t *testing.T) {
	args := map[string]string{"url": "http://x/r", "dst": "/opt/r"}
	// origin remote matches the wanted url → converged.
	if got := eval(t, "git.clone", args, &fakeExec{observe: engine.ShellResult{Stdout: "http://x/r\n"}, applyMatch: "git clone"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("git-clone matching: got %s, want ok.already", got)
	}
	// absent (empty remote) → clone.
	if got := eval(t, "git.clone", args, &fakeExec{observe: engine.ShellResult{Stdout: ""}, apply: engine.ShellResult{Exit: 0}, applyMatch: "git clone"}, engine.Apply).String(); got != "ok.cloned" {
		t.Fatalf("git-clone absent: got %s, want ok.cloned", got)
	}
	// wrong remote → drift → clone fails on an existing dst → err.runtime
	// (the v1 tradeoff: no precise err.wrongRemote).
	if got := eval(t, "git.clone", args, &fakeExec{observe: engine.ShellResult{Stdout: "http://other\n"}, apply: engine.ShellResult{Exit: 128}, applyMatch: "git clone"}, engine.Apply).String(); got != "err.runtime" {
		t.Fatalf("git-clone wrong remote: got %s, want err.runtime", got)
	}
}

func TestFileWrite_ContentSync(t *testing.T) {
	args := map[string]string{"path": "/etc/x", "content": "a\nb\n"}
	// synced (content matches) → skip.
	if got := eval(t, "file.write", args, &fakeExec{observe: converged, applyMatch: " > "}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("file-write synced: got %s, want ok.already", got)
	}
	// differs → write.
	if got := eval(t, "file.write", args, &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 0}, applyMatch: " > "}, engine.Apply).String(); got != "ok.written" {
		t.Fatalf("file-write drift: got %s, want ok.written", got)
	}
}

func TestStatusMode(t *testing.T) {
	// A truthy field (dir-ensure `present`, no parameter): converged when the
	// observe shell succeeds, drift otherwise — reported, never applied.
	conv := &fakeExec{observe: converged, applyMatch: "mkdir"}
	res := eval(t, "dir.ensure", map[string]string{"path": "/opt/x"}, conv, engine.Status)
	if res.Category != engine.OK || len(res.Fields) != 1 || res.Fields[0].Name != "present" || !res.Fields[0].Converged {
		t.Fatalf("dir-ensure status converged: %s %+v", res, res.Fields)
	}
	for _, s := range conv.calls {
		if strings.Contains(s, "mkdir") {
			t.Fatal("status must not run apply")
		}
	}
	dr := &fakeExec{observe: drift, applyMatch: "mkdir"}
	res = eval(t, "dir.ensure", map[string]string{"path": "/opt/x"}, dr, engine.Status)
	if res.Category != engine.WOULD || res.Fields[0].Converged || res.Fields[0].Current != "false" || res.Fields[0].Desired != "true" {
		t.Fatalf("dir-ensure status drift: %s %+v", res, res.Fields)
	}
}

func TestReadOnlyQuestions(t *testing.T) {
	// A question resolves in pass 1: deterministic even in CHECK (never `would`).
	if got := eval(t, "dir.exists", map[string]string{"path": "/opt"}, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.present" {
		t.Fatalf("dir-exists present: got %s, want ok.present", got)
	}
	if got := eval(t, "dir.exists", map[string]string{"path": "/opt"}, &fakeExec{observe: drift}, engine.Check).String(); got != "err.absent" {
		t.Fatalf("dir-exists absent: got %s, want err.absent", got)
	}
	if got := eval(t, "file.exists", map[string]string{"path": "/etc/x"}, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.present" {
		t.Fatalf("file-exists present: got %s, want ok.present", got)
	}
}
