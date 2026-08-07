package transport

import (
	"bytes"
	"fmt"
	"os/exec"
)

// Local runs the agent on the control host itself: it executes `agentBin __agent`
// as a subprocess with the request on stdin and reads the response from stdout —
// the same ephemeral agent, request, and protocol SSH uses for a one-shot job,
// minus push/deposit/poll (the binary is already here). Selected by a host with
// `local: "true"` in the inventory (ADR-0027).
type Local struct{}

var _ Transport = Local{}

func (Local) Run(agentBin string, req []byte) ([]byte, error) {
	cmd := exec.Command(agentBin, "__agent")
	cmd.Stdin = bytes.NewReader(req)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("local agent: %v: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
