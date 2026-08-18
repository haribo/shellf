# `agent` — the peer architecture's agent

Empty in the repository, and **replaced at release time**.

The release build runs two passes (ADR-0048 §3): it builds a bare binary for each
architecture, then rebuilds each release binary with `-tags bundled` after copying the
*other* architecture's bare binary here. A build that finds this file empty carries no
peer and refuses a foreign target by name, which is the documented behaviour of a plain
`go build`.

It is committed empty rather than absent so that `go build -tags bundled ./...` compiles
from a clean checkout — `//go:embed` fails on a missing file, and a build path CI cannot
compile is a build path nobody checks.
