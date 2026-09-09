package engine

import "strings"

// Shell runs a raw shell command on the target — the thesis's first-class citizen. It
// always runs and, like any raw shell, cannot be previewed: a plan that needs a guard
// writes `if !shell { <guard> } { shell { <cmd> } }`, which the language can express and
// preview.
//
// It carried an `Unless` guard until #619. Nothing could set it: the parser refuses the
// keyword by name (`internal/lang/parser.go`), #415 removed the def-side remnant, and the
// only remaining writer read it out of a step's free-form arguments — reachable by a forged
// request and by nothing a plan can produce. A capability with no way to express it is a
// trap for the next reader of this file, not a feature.
type Shell struct {
	Cmd string
	Env Env // per-host variables, injected as $name (injection-safe) (#106)
}

func (s Shell) Name() string       { return "shell" }
func (s Shell) ChangedTag() string { return "ran" }

func (s Shell) PreCheck() *Result {
	if strings.TrimSpace(s.Cmd) == "" {
		r := Err("emptyCommand")
		return &r
	}
	return nil
}

// Guard: a raw shell has none — it always runs (#619).
func (s Shell) Guard(Executor) *Result { return nil }

func (s Shell) Apply(ex Executor) Result {
	r := ex.Shell(s.Cmd, s.Env)
	if !r.OK() {
		return ErrShell("runtime", r)
	}
	return Ok(s.ChangedTag())
}

func (s Shell) Preview(ex Executor) *ShellResult { return nil }
