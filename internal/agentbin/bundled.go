//go:build bundled

package agentbin

import (
	_ "embed"
	"runtime"
)

// hasPeer reflects what was embedded, not that the tag was set: the peer file is
// committed empty and filled by the release build's first pass, so a `bundled` build from
// a clean checkout legitimately carries nothing.
var hasPeer = len(peerAgent) > 0

// The peer agent for the architecture this build does not run on. Deposited by the
// release build's first pass (ADR-0048 §3) — it is a *bare* binary, carrying no peer of
// its own, so an agent pushed across architectures cannot itself push to a third one.
//
//go:embed peer/agent
var peerAgent []byte

func peer(goarch string) ([]byte, bool) {
	if len(peerAgent) == 0 {
		return nil, false
	}
	// One peer per build, and it is whichever architecture this one is not.
	if runtime.GOARCH == "amd64" && goarch == "arm64" {
		return peerAgent, true
	}
	if runtime.GOARCH == "arm64" && goarch == "amd64" {
		return peerAgent, true
	}
	return nil, false
}
