package engine

import "strings"

// Shell runs a raw shell command on the target — the thesis's first-class
// citizen. Idempotence and previewability come only from an optional `unless`
// guard: without it the command always runs (and, like any raw shell, cannot be
// previewed). The user is responsible for the guard, as in Chef `not_if` /
// Puppet `unless`.
type Shell struct {
	Cmd    string
	Unless string // read-only guard; empty = no guard
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

// Guard: with an `unless`, exit 0 means the desired state already holds → skip.
func (s Shell) Guard(ex Executor) *Result {
	if s.Unless == "" {
		return nil // no guard → always run
	}
	if ex.Shell(s.Unless, nil).OK() {
		r := Ok("alreadySatisfied")
		return &r
	}
	return nil
}

func (s Shell) Apply(ex Executor) Result {
	r := ex.Shell(s.Cmd, nil)
	if !r.OK() {
		return ErrShell("runtime", r)
	}
	return Ok(s.ChangedTag())
}

func (s Shell) Preview(ex Executor) *ShellResult { return nil }
