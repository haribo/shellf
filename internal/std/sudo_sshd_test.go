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
	res, err := lang.EvalDefWith(def, args, nil, ex, mode,
		func(n string) (lang.Def, bool) { return Lookup(n) }, []string{name}, nil)
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
		return engine.ShellResult{Exit: 0}
	case strings.Contains(script, "stat -c"):
		if c.converged {
			return engine.ShellResult{Stdout: "440\n"}
		}
		return engine.ShellResult{Stdout: "644\n"} // drift, so file.mode applies
	case strings.Contains(script, "cmp -s"):
		return boolExit(c.converged) // file.write's observe
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
