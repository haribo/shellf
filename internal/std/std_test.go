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

	// ADR-0050 re-reads `observe` after an apply that acted, so a fake has to model the
	// one thing every fake silently assumed until then: **that an apply changes what an
	// observe sees**. Without this, every drift case reports `err.unconfirmed` — correctly,
	// since the fake would be insisting the state never moved.
	//
	// `after` is the observe's answer once the apply has run. Nil means "exit 0", which
	// converges a truthy field; a def whose observe reports a *value* (file.mode reads
	// `stat -c %a`) must give the value its apply produces, since an empty stdout matches
	// no desired value.
	after *engine.ShellResult
	// stuck models the failure ADR-0050 exists to catch: the apply runs, exits 0, and the
	// state does not follow. The default (false) is the honest world; this is the exception
	// a test asks for on purpose.
	stuck   bool
	applied bool
	phase   string // set by Phase (engine.PhaseAware), so the script's text is not the signal
}

func (f *fakeExec) As(string) engine.Executor    { return f }
func (f *fakeExec) Using(string) engine.Executor { return f }

// Phase is engine.PhaseAware (#516): the evaluator says which phase is starting, so this
// fake no longer has to guess from the script's text. `applyMatch` stays for the cases that
// assert *which* command ran, but it is no longer what tells an observe from an apply —
// which is what silently broke when `ufw.open`'s observe came to contain the very command
// its apply runs.
func (f *fakeExec) Phase(name string) { f.phase = name }

func (f *fakeExec) Shell(script string, _ engine.Env) engine.ShellResult {
	f.calls = append(f.calls, script)
	if f.phase == "apply" || (f.phase == "" && f.applyMatch != "" && strings.Contains(script, f.applyMatch)) {
		f.applied = true
		return f.apply
	}
	if f.applied && !f.stuck {
		if f.after != nil {
			return *f.after
		}
		return engine.ShellResult{Exit: 0}
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
	// `after`: file.mode's observe reads `stat -c %a`, a value — so once the chmod has run
	// the fake has to answer with the mode it set, or the re-observation of ADR-0050 sees
	// an empty stdout and reports `err.unconfirmed`.
	if got := eval(t, "file.mode", fm, &fakeExec{observe: engine.ShellResult{Stdout: "644\n"}, apply: converged, applyMatch: "chmod", after: &engine.ShellResult{Stdout: "755\n"}}, engine.Apply).String(); got != "ok.changed" {
		t.Fatalf("file-mode drift: got %s", got)
	}
}

func TestDeployDefs_Questions(t *testing.T) {
	// http-check is a read-only question (check phase): match → ok, else err.
	args := map[string]string{"url": "http://x", "status": "200"}
	if got := eval(t, "http.check", args, &fakeExec{observe: converged}, engine.Apply).String(); got != "ok.match" {
		t.Fatalf("http-check match: got %s", got)
	}
	if got := eval(t, "http.check", args, &fakeExec{observe: drift}, engine.Apply).String(); got != "err.mismatch" {
		t.Fatalf("http-check mismatch: got %s", got)
	}

	// In check mode a *failing* question answers `would` instead (ADR-0051): the service it
	// asks about is often the one the plan is about to deploy, so the failure is an artefact
	// of previewing rather than a fact about the run. A match still resolves — the state is
	// there and the plan does not remove it.
	if got := eval(t, "http.check", args, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.match" {
		t.Fatalf("http-check match in check: got %s, want ok.match", got)
	}
	if got := eval(t, "http.check", args, &fakeExec{observe: drift}, engine.Check).String(); got != "would" {
		t.Fatalf("http-check mismatch in check: got %s, want would", got)
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
		// `ufw allow "` and not `ufw allow`: the observe now looks for that same text inside
		// `ufw show added`, so the bare word no longer tells the two apart (#515).
		{"ufw.open", `ufw allow "`, "opened", map[string]string{"port": "443", "proto": "tcp"}},
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
	// absent (empty remote) → clone. `after` is the remote the clone leaves behind: the
	// observe reads a value, so ADR-0050's re-read needs the value the apply produced.
	if got := eval(t, "git.clone", args, &fakeExec{observe: engine.ShellResult{Stdout: ""}, apply: engine.ShellResult{Exit: 0}, applyMatch: "git clone", after: &engine.ShellResult{Stdout: "http://x/r\n"}}, engine.Apply).String(); got != "ok.cloned" {
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
	// A question resolves in pass 1, and in CHECK the resolution is asymmetric (ADR-0051,
	// amending ADR-0004): a `yes` holds — the state is there and the plan does not remove it
	// — while a `no` is exactly what the plan may be about to create, so it answers `would`
	// rather than an error that would halt the preview.
	if got := eval(t, "dir.exists", map[string]string{"path": "/opt"}, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.present" {
		t.Fatalf("dir-exists present: got %s, want ok.present", got)
	}
	if got := eval(t, "dir.exists", map[string]string{"path": "/opt"}, &fakeExec{observe: drift}, engine.Check).String(); got != "would" {
		t.Fatalf("dir-exists absent in check: got %s, want would", got)
	}
	// In apply the answer is a fact, and an absent path is an error like any other.
	if got := eval(t, "dir.exists", map[string]string{"path": "/opt"}, &fakeExec{observe: drift}, engine.Apply).String(); got != "err.absent" {
		t.Fatalf("dir-exists absent in apply: got %s, want err.absent", got)
	}
	if got := eval(t, "file.exists", map[string]string{"path": "/etc/x"}, &fakeExec{observe: converged}, engine.Check).String(); got != "ok.present" {
		t.Fatalf("file-exists present: got %s, want ok.present", got)
	}
}

// ADR-0050 / #495: an apply proposes a verdict, the re-observation establishes it.
//
// Before this, a def's outcome came from the exit code of its apply shell and nothing else:
// every command exited 0, so the def reported success, and nothing checked that the effect
// landed. Six defects had that shape — #390, #411, #418, #480, #486, #507 — and the case
// that named the cause was `service.ensure` reporting `ok.converged` over a unit that had
// been stopped between `start` and `enable`, with an observe that was itself correct.
//
// `stuck` is the fake's way of saying "the apply ran, exited 0, and the state did not
// follow" — the one thing a fake could never express before, because every fake silently
// assumed an apply changes what an observe sees.
func TestConfirm_RefusesASuccessTheStateDoesNotSupport(t *testing.T) {
	t.Run("an apply whose effect does not land is refused", func(t *testing.T) {
		f := &fakeExec{observe: drift, apply: converged, applyMatch: "apt-get install", stuck: true}
		res := eval(t, "apt.install", map[string]string{"pkg": "nginx"}, f, engine.Apply)
		if res.Category != engine.ERR || res.Tag != "unconfirmed" {
			t.Fatalf("an apply the state does not confirm must not report success, got %s", res)
		}
		// The message names the field that did not move: "the apply did not take effect"
		// sends the reader nowhere (same rule as #470).
		if res.Shell == nil || !strings.Contains(res.Shell.Stderr, "installed") {
			t.Fatalf("the failure must name the field that did not move, got %+v", res.Shell)
		}
	})

	t.Run("an apply whose effect lands reports its own verdict", func(t *testing.T) {
		f := &fakeExec{observe: drift, apply: converged, applyMatch: "apt-get install"}
		if got := eval(t, "apt.install", map[string]string{"pkg": "nginx"}, f, engine.Apply).String(); got != "ok.installed" {
			t.Fatalf("got %s, want ok.installed", got)
		}
	})

	// The gates of ADR-0050 §2, each one a round trip not taken.
	t.Run("an action-shaped def is not re-observed", func(t *testing.T) {
		// service.restart declares no observe: a restart restarting is the point.
		f := &fakeExec{apply: converged, applyMatch: "restart", stuck: true}
		if got := eval(t, "service.restart", map[string]string{"name": "sshd"}, f, engine.Apply).String(); got != "ok.restarted" {
			t.Fatalf("a def with no observe has no state to confirm, got %s", got)
		}
	})

	t.Run("a converged def never reaches the re-observation", func(t *testing.T) {
		// Pass 1 returns `already`, so `stuck` is never consulted — and no extra shell runs.
		f := &fakeExec{observe: converged, applyMatch: "apt-get install", stuck: true}
		res := eval(t, "apt.install", map[string]string{"pkg": "nginx"}, f, engine.Apply)
		if res.String() != "ok.already" {
			t.Fatalf("got %s, want ok.already", res)
		}
		if len(f.calls) != 1 {
			t.Fatalf("a converged run must cost one observe, got %d shells: %v", len(f.calls), f.calls)
		}
	})

	t.Run("a failed apply keeps its own error", func(t *testing.T) {
		// Re-reading state here would replace a precise message with a vague one.
		f := &fakeExec{observe: drift, apply: engine.ShellResult{Exit: 100}, applyMatch: "apt-get install", stuck: true}
		if got := eval(t, "apt.install", map[string]string{"pkg": "nginx"}, f, engine.Apply).String(); got != "err.runtime" {
			t.Fatalf("got %s, want err.runtime", got)
		}
	})
}

// #516: a fake tells an observe from an apply by the phase the evaluator announces, not by
// the text of the script.
//
// The proof is the absence of `applyMatch`. Before `engine.PhaseAware`, this test could not
// be written: with no text to match, every shell looked like an observe and the apply's
// result was never returned. Three def fixes in one week broke tests in other packages for
// that reason — and `ufw.open` was worse than broken, since its observe came to contain the
// command its apply runs, so a substring match read one as the other.
func TestFake_TellsPhasesApartWithoutMatchingText(t *testing.T) {
	f := &fakeExec{observe: drift, apply: converged} // no applyMatch at all
	if got := eval(t, "apt.install", map[string]string{"pkg": "nginx"}, f, engine.Apply).String(); got != "ok.installed" {
		t.Fatalf("the apply's result must be returned on its own phase, got %s", got)
	}
	if !f.applied {
		t.Fatal("the apply phase was never recognised")
	}

	// And a converged observe still skips it — the phase is a signal, not an override.
	f = &fakeExec{observe: converged, apply: converged}
	if got := eval(t, "apt.install", map[string]string{"pkg": "nginx"}, f, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("got %s, want ok.already", got)
	}
	if f.applied {
		t.Fatal("a converged def must not reach its apply")
	}
}
