// Package transport ships the ephemeral agent to a target and runs it there.
// Injectable like engine.Executor: the real one uses SSH, tests use a fake.
package transport

// Transport pushes the agent binary to a target, runs it in agent mode with
// req fed on stdin, returns the agent's stdout, and removes the binary
// afterwards (ephemeral — nothing stays installed).
type Transport interface {
	Run(agentBin string, req []byte) (resp []byte, err error)
}
