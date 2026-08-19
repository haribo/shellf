# ADR 0048 — The agent matches the target's architecture, and travels inside the binary

## Status

Active.

## Context

shellf pushes **itself** as the agent: the CLI hands `os.Executable()` to the transport,
which reads those bytes and streams them to the target. That is what makes the promise
"one static binary, nothing installed on the target" true, and it is the reason the
resident agent can be cached by hash and self-erase.

It also means the target must share the control host's architecture. `release.yaml` builds
`linux/amd64` alone, so every arm64 machine — Graviton, an ARM VPS, a Pi — is unreachable.
The failure is not even a refusal: the wrong binary lands, is marked executable, and the
target answers with `exec format error` from a process shellf did not write.

An agentless tool that cannot reach half the fleet is not agentless, it is amd64-only.

## Decision

### 1. A release binary embeds the agent for the other architecture

`//go:embed`, behind a `bundled` build tag. A release binary for amd64 carries an arm64
agent, and vice versa. Nothing to install, nothing to fetch, nothing to configure: the
property the product is built on survives the fix.

### 2. A plain `go build` stays a plain `go build`

Without the `bundled` tag the embed is empty, so `go build ./cmd/shellf` keeps working,
keeps its size, and keeps its speed — the README's install instructions do not change.
Such a binary targets its own architecture only, and **says so** when asked for another:

```
target <host> is aarch64, and this binary carries no arm64 agent
(a release binary does — see the project's releases)
```

Refusing is the point. The current behaviour is to push a binary the target cannot exec,
and a refusal on the control host beats a corpse on the target.

### 3. The embedded agent is bare

The two-pass build produces bare binaries first, then embeds each into the other. So an
agent pushed to a foreign-architecture target carries no peer of its own and cannot push
to a third architecture.

That is not a limitation in practice — a pushed agent executes a plan, it never
orchestrates — but it is stated here because it is invisible in the code: the embedded
bytes look like an ordinary shellf binary, and they are not fully one.

### 4. The target's architecture is probed on the connection that is already open

`uname -m`, mapped to a GOARCH (`x86_64` → `amd64`, `aarch64`/`arm64` → `arm64`). Anything
else is a named error rather than a guess — a wrong guess is the `exec format error` this
ADR exists to remove.

It is a **separate command** from the `/dev/shm` probe rather than one combined round
trip. Merging them would save one exec per job and would rewrite a seam that is covered by
its own tests; the saving is not worth the churn, and a measurement can revisit it.

The remote cache needs no change: the agent path is keyed on the hash of the pushed bytes,
so two architectures already resolve to two paths, two workdirs and two resident agents.

## Consequences

- The release binary roughly doubles. Measured on the two-pass build at the time of
  writing: bare 5.8 MB (amd64) and 5.4 MB (arm64), bundled 11.2 MB each. That is the
  price of the promise; the alternatives that keep it small all break something else
  (below).
- `release.yaml` builds in two passes and publishes `shellf-linux-amd64` and
  `shellf-linux-arm64`.
- One extra `uname -m` exec per job.
- Cross-**distro** remains out of scope: this is about the instruction set, not about
  libc or the init system.

## Alternatives rejected

- **A second file beside the binary, or an `--agent-bin` flag.** Cheapest to build and it
  keeps the binary small. It also trades the product's first promise — a single binary,
  nothing to install — for 8 MB of disk. Wrong trade: the operator now carries, ships and
  version-matches two files, and the failure mode when they drift is the `exec format
  error` we started from.
- **Fetching the agent from the GitHub release on first need.** Small binary, simple
  build. But it puts a network dependency in the middle of a deploy, on a control host
  that may deliberately have none, and adds a supply-chain surface the project refuses
  everywhere else — it already declines to re-push over a binary it does not recognise
  (ADR-0005 lineage, #391). Verifying a checksum would narrow the risk without removing
  the dependency.
- **Building the agent on the target.** No toolchain is guaranteed there; the whole point
  is that the target needs nothing installed.
- **Shipping one binary per architecture with no embedding, and requiring the control host
  to match its fleet.** That is the status quo dressed as a decision: it fails the moment
  a fleet is mixed, which is the common case it must serve.
