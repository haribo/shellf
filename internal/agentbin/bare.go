//go:build !bundled

package agentbin

// hasPeer says whether this build actually carries a foreign-architecture agent. The
// build tag is not the answer: a `bundled` build whose peer slot was never filled carries
// nothing, and must behave exactly like this one.
const hasPeer = false

// peer: a plain `go build` embeds nothing, so it can only target its own architecture.
// Keeping that the default is deliberate — the README's one-line install must stay one
// line, and a contributor's build must stay fast (ADR-0048 §2).
func peer(string) ([]byte, bool) { return nil, false }
