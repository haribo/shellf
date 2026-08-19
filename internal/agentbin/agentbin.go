// Package agentbin answers one question: which bytes should be pushed to a target of a
// given architecture?
//
// shellf pushes itself as the agent, so the target has always had to share the control
// host's architecture — the wrong binary landed, was made executable, and the target
// answered `exec format error` from a process shellf did not write (#453). A release
// binary therefore carries the agent for the other architecture, embedded
// (ADR-0048); a plain `go build` carries none and refuses rather than pushing bytes
// the target cannot run.
package agentbin

import (
	"fmt"
	"runtime"
	"strings"
)

// ArchFromUname maps what `uname -m` reports to a GOARCH.
//
// An unrecognised value is an error, never a default: defaulting is precisely how the
// wrong binary reaches the target, and the operator learns about it as `exec format
// error` instead of a refusal on the control host.
func ArchFromUname(uname string) (string, error) {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "":
		return "", fmt.Errorf("the target reported no architecture (`uname -m` returned nothing)")
	default:
		return "", fmt.Errorf("unsupported target architecture %q: shellf ships agents for amd64 and arm64", strings.TrimSpace(uname))
	}
}

// For returns the agent bytes to push to a target running goarch. `self` is the running
// binary's own bytes, used when the target shares this architecture.
func For(goarch string, self []byte) ([]byte, error) {
	if goarch == runtime.GOARCH {
		return self, nil
	}
	if b, ok := peer(goarch); ok {
		return b, nil
	}
	return nil, fmt.Errorf(
		"this %s build carries no %s agent, so it cannot target %s — a release binary carries both (ADR-0048)",
		runtime.GOARCH, goarch, goarch)
}
