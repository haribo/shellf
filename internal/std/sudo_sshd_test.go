package std

import (
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// evalWith is eval() for an executor of our own — the shared helper is typed on
// *fakeExec, and these defs need a fake that answers a checker.
func evalWith(t *testing.T, name string, args map[string]string, ex engine.Executor, mode engine.Mode) engine.Result {
	t.Helper()
	def, ok := Lookup(name)
	if !ok {
		t.Fatalf("stdlib def %q not found", name)
	}
	res, err := lang.EvalDefFull(def, args, nil, nil, ex, mode, func(n string) (lang.Def, bool) { return Lookup(n) }, []string{name}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// #328. These two exist because an invalid file locks the operator out: a broken
// sudoers breaks the escalation shellf itself uses, a broken sshd_config breaks its
// transport. The validation is in `check`, so a refused content never reaches the
// write — and `check` runs in dry-run too.
func TestSudoWrite(t *testing.T) {
	args := map[string]string{"name": "haribo", "content": "haribo ALL=(ALL) NOPASSWD: ALL"}

	t.Run("a refused content never writes", func(t *testing.T) {
		// The name check passes, visudo refuses.
		res := evalWith(t, "sudo.write", args, &checkFake{nameOK: true, visudoOK: false}, engine.Apply)
		if res.String() != "err.validation" {
			t.Fatalf("got %s, want err.validation", res.String())
		}
	})

	t.Run("a bad drop-in name is refused before visudo", func(t *testing.T) {
		// sudo skips names with a dot, so the file would be silently inert — visudo
		// itself says nothing about this.
		bad := map[string]string{"name": "my.file", "content": "x"}
		res := evalWith(t, "sudo.write", bad, &checkFake{nameOK: false, visudoOK: true}, engine.Apply)
		if res.String() != "err.badName" {
			t.Fatalf("got %s, want err.badName", res.String())
		}
	})

	t.Run("an accepted content is written and made 0440", func(t *testing.T) {
		f := &checkFake{nameOK: true, visudoOK: true}
		res := evalWith(t, "sudo.write", args, f, engine.Apply)
		if res.Category == engine.ERR {
			t.Fatalf("must succeed: %s", res.String())
		}
		// 0440 is not cosmetic: sudo ignores a drop-in with any other mode, so the rule
		// would look installed and do nothing. Found by running visudo -c for real.
		if !f.sawChmod0440 {
			t.Fatal("the drop-in must be set to 0440, or sudo ignores it")
		}
	})

	t.Run("validation happens in dry-run too", func(t *testing.T) {
		res := evalWith(t, "sudo.write", args, &checkFake{nameOK: true, visudoOK: false}, engine.Check)
		if res.String() != "err.validation" {
			t.Fatalf("dry-run must surface a refused content: %s", res.String())
		}
	})
}

func TestSshdConfig(t *testing.T) {
	args := map[string]string{"name": "99-hardening", "content": "PermitRootLogin no"}

	if res := evalWith(t, "sshd.config", args, &checkFake{sshdOK: false}, engine.Apply); res.String() != "err.validation" {
		t.Fatalf("a refused config must not be written: %s", res.String())
	}
	f := &checkFake{sshdOK: true}
	if res := evalWith(t, "sshd.config", args, f, engine.Apply); res.Category == engine.ERR {
		t.Fatalf("a valid config must be written: %s", res.String())
	}
	if !f.sawChmod0600 {
		t.Fatal("sshd refuses to start on a group-writable config, so the mode must be set")
	}
}

// checkFake drives the check phase: it answers the name grep, the checker, and records
// the modes applied.
type checkFake struct {
	nameOK, visudoOK, sshdOK   bool
	sawChmod0440, sawChmod0600 bool
	converged                  bool // the file and its mode are already right

	// ADR-0050 re-reads `observe` after an apply, and these defs compose `file.write` +
	// `file.mode`, so three observes now run after work: the two callees' own, and this
	// def's. Without modelling what the apply produced, every write reports
	// `err.unconfirmed` because the fake keeps insisting nothing moved.
	//
	// The two facts are tracked apart on purpose. A single "applied" flag makes the write
	// converge `file.mode` as well, so the chmod never runs and the def silently stops
	// setting the mode sudo insists on — measured while writing this.
	wrote    bool // file.write's staged rename landed
	chmodded bool // file.mode's chmod ran
}

func (c *checkFake) As(string) engine.Executor    { return c }
func (c *checkFake) Using(string) engine.Executor { return c }

func (c *checkFake) Shell(script string, env engine.Env) engine.ShellResult {
	switch {
	case strings.Contains(script, "grep -qE"):
		return boolExit(c.nameOK)
	case strings.Contains(script, "visudo -cf"):
		return boolExit(c.visudoOK)
	case strings.Contains(script, "sshd -t -f"):
		return boolExit(c.sshdOK)
	case strings.Contains(script, "chmod"):
		// The mode travels in the environment, not the script text.
		switch env["mode"] {
		case "440":
			c.sawChmod0440 = true
		case "600":
			c.sawChmod0600 = true
		}
		c.chmodded = true
		return engine.ShellResult{Exit: 0}
	case strings.Contains(script, "mv -f"):
		c.wrote = true
		return engine.ShellResult{Exit: 0}
	case strings.Contains(script, "stat -c"):
		if c.converged || c.chmodded {
			return engine.ShellResult{Stdout: env["mode"] + "\n"}
		}
		return engine.ShellResult{Stdout: "644\n"} // drift, so file.mode applies
	case strings.Contains(script, "cmp -s"):
		return boolExit(c.converged || c.wrote) // file.write's observe
	}
	return engine.ShellResult{Exit: 0}
}

func boolExit(ok bool) engine.ShellResult {
	if ok {
		return engine.ShellResult{Exit: 0}
	}
	return engine.ShellResult{Exit: 1}
}

// A converged run must not report changed. Found by running it: sudo.write's `check`
// runs shells, and those were counting as "acted" — so every re-run fired the handler
// gated on `.changed`, which is the exact false positive idempotence exists to prevent.
func TestSudoWrite_ConvergedRunIsNotChanged(t *testing.T) {
	f := &checkFake{nameOK: true, visudoOK: true, converged: true}
	res := evalWith(t, "sudo.write",
		map[string]string{"name": "haribo", "content": "haribo ALL=(ALL) NOPASSWD: ALL"},
		f, engine.Apply)
	if res.Category == engine.ERR {
		t.Fatalf("must succeed: %s", res.String())
	}
	if res.Changed {
		t.Fatal("nothing changed, so the result must not be changed — a check-phase shell is a read")
	}
}

// #340: a def that composes in `apply` and declares no `observe` of its own has no
// opinion in check mode — `apply` never runs there, so its callees' `observe` phases
// never run either. `sudo.write` therefore announced `would.written` on a host where the
// drop-in was already correct: a preview of a write that would not happen, in a tool
// whose thesis is that a run is readable before it happens.
func TestSudoWrite_ConvergedHostPreviewsNoAction(t *testing.T) {
	f := &checkFake{nameOK: true, visudoOK: true, converged: true}
	res := evalWith(t, "sudo.write",
		map[string]string{"name": "haribo", "content": "haribo ALL=(ALL) NOPASSWD: ALL"},
		f, engine.Check)
	if res.Category == engine.ERR {
		t.Fatalf("must not error: %s", res.String())
	}
	if res.Tag != "already" {
		t.Fatalf("a converged drop-in must preview as already, got %s", res.String())
	}
}

// The other half: drift must still preview the action, or the fix would be "always say
// already", which reports nothing and breaks the run it is meant to describe.
func TestSudoWrite_DriftedHostPreviewsTheWrite(t *testing.T) {
	f := &checkFake{nameOK: true, visudoOK: true, converged: false}
	res := evalWith(t, "sudo.write",
		map[string]string{"name": "haribo", "content": "haribo ALL=(ALL) NOPASSWD: ALL"},
		f, engine.Check)
	if res.Category != engine.WOULD {
		t.Fatalf("a drifted drop-in must preview the write, got %s", res.String())
	}
}

// sshd.config has the same shape and the same defect: composed apply, no observe.
func TestSshdConfig_ConvergedHostPreviewsNoAction(t *testing.T) {
	f := &checkFake{sshdOK: true, converged: true}
	res := evalWith(t, "sshd.config",
		map[string]string{"name": "hardening", "content": "PermitRootLogin no"},
		f, engine.Check)
	if res.Tag != "already" {
		t.Fatalf("a converged sshd drop-in must preview as already, got %s", res.String())
	}
}

// dropInFake separates the two things a drop-in must get right, which checkFake conflates
// behind a single `converged` flag. Content and mode drift independently, and the mode
// half is the one that fails silently: sudo ignores a drop-in that is not 0440, so a rule
// with perfect content and mode 644 looks installed and does nothing.
type dropInFake struct {
	contentOK, modeOK bool
}

func (d *dropInFake) As(string) engine.Executor    { return d }
func (d *dropInFake) Using(string) engine.Executor { return d }

func (d *dropInFake) Shell(script string, _ engine.Env) engine.ShellResult {
	switch {
	case strings.Contains(script, "grep -qE"), strings.Contains(script, "visudo -cf"),
		strings.Contains(script, "sshd -t -f"):
		return engine.ShellResult{Exit: 0} // validation passes; not what these tests probe
	case strings.Contains(script, "cmp -s"):
		return boolExit(d.contentOK)
	case strings.Contains(script, "stat -c"):
		return boolExit(d.modeOK)
	}
	return engine.ShellResult{Exit: 0}
}

// #340: each half of convergence must be able to fail on its own. A def observing only
// the content would report `already` on a drop-in sudo is silently skipping.
func TestDropIn_ModeDriftAloneIsNotConverged(t *testing.T) {
	for _, tc := range []struct {
		name              string
		def               string
		args              map[string]string
		contentOK, modeOK bool
		want              string
	}{
		{"sudo content and mode right", "sudo.write",
			map[string]string{"name": "haribo", "content": "x"}, true, true, "ok.already"},
		{"sudo mode wrong alone", "sudo.write",
			map[string]string{"name": "haribo", "content": "x"}, true, false, "would.written"},
		{"sudo content wrong alone", "sudo.write",
			map[string]string{"name": "haribo", "content": "x"}, false, true, "would.written"},
		{"sshd content and mode right", "sshd.config",
			map[string]string{"name": "hardening", "content": "x"}, true, true, "ok.already"},
		{"sshd mode wrong alone", "sshd.config",
			map[string]string{"name": "hardening", "content": "x"}, true, false, "would.written"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &dropInFake{contentOK: tc.contentOK, modeOK: tc.modeOK}
			res := evalWith(t, tc.def, tc.args, f, engine.Check)
			if res.String() != tc.want {
				t.Fatalf("got %s, want %s", res.String(), tc.want)
			}
		})
	}
}
