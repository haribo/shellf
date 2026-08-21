package engine

// ShellResult is the raw mechanics of a shell block — no judgment.
// It is NOT a Result (see result.go).
type ShellResult struct {
	Exit   int
	Stdout string
	Stderr string

	// Cmd is the shell block's source text, and Def/Line say where it was written
	// (#470). Filled by the evaluator rather than the executor: the executor receives a
	// script and knows nothing of the def it came from.
	//
	// The text is the **source**, with `$var` unexpanded, because that is what actually
	// ran — values reach the shell through the environment (see Env below), so no
	// substituted command line ever exists. Vars carries those values separately, which
	// keeps a secret out of a rendered command string.
	Cmd  string            `json:",omitempty"`
	Def  string            `json:",omitempty"` // qualified def name, e.g. "service.ensure"
	Line int               `json:",omitempty"` // 1-based line of `shell` within that def
	Vars map[string]string `json:",omitempty"` // the variables the block could read
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
	// As returns an executor that runs shells escalated to `user` (via the
	// configured method, sudo by default). An empty user returns the receiver
	// unchanged (no escalation). See ADR-0011.
	As(user string) Executor
	// Using returns an executor that runs shells under `interp` (sh/bash/dash/nu/
	// raw), which decides the binary and the injected prelude. An empty interp
	// returns the receiver unchanged (the sh default). See ADR-0012.
	Using(interp string) Executor
}
