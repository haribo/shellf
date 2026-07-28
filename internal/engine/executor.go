package engine

// ShellResult is the raw mechanics of a shell block — no judgment.
// It is NOT a Result (see result.go).
type ShellResult struct {
	Exit   int
	Stdout string
	Stderr string
}

// OK is sugar for "the command succeeded".
func (s ShellResult) OK() bool { return s.Exit == 0 }

// Env maps language variables to values. They are injected into the shell
// environment, never concatenated into the command text — so a value like
// "nginx; rm -rf /" cannot inject a command.
type Env map[string]string

// Executor runs shell blocks. Injectable from commit 1: the real one talks to
// /bin/sh, the fake one is a lookup table for deterministic tests.
type Executor interface {
	Shell(script string, env Env) ShellResult
}
