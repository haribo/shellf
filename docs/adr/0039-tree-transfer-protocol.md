# ADR 0039 — Tree transfer: the agent sends a manifest, the control host answers a delta

## Status

Active. **Extends [ADR-0031](0031-control-channel-and-detachment.md)**, whose channel it
keeps — one socket, NDJSON, data only, allow-listed — and whose one-message answer it
generalises to a sequence. Nothing in ADR-0031 is reversed.

## Context

`~dir.sync` is decided ([ADR-0036](0036-primitive-and-location-markers.md) §5) and has no
code, because the channel cannot carry a tree. An ask is one message and an answer is one
message, with the payload base64'd inside it.

That is exactly what `dir.copy` does today, and its two costs are the reason to change:
a 32 MB ceiling above which a tree is simply refused, and the whole tree held in memory on
both sides on every run — even when nothing changed. (`dir.copy` *is* idempotent: each
expanded `file.put` guards on sha256, so nothing is rewritten. The bytes still travel.)

Two obvious designs, both bad:

- **One message, chunked.** Removes the ceiling, still sends everything every run.
- **One ask per file.** Sends only what is missing, at one round trip each. On 10 000
  files over a 50 ms link that is minutes of latency, spent doing nothing.

The cost that matters is round trips, and the information needed to avoid them — what the
target already has — lives on the target.

## Decision

### 1. The agent sends what it has; the control host answers what is missing

```
agent    → ask  dir.sync:<src>   { entries: [{path, size, mtime, sha256?}, …] }
control  → file <path> <mode>    (chunk, chunk, …)
control  → file <path> <mode>    (chunk, …)
control  → done  { written: N, delete: [paths…] }
```

One round trip, then a one-way stream. The side that owns the files decides what to push,
so no per-file negotiation happens.

The manifest is metadata only — a path, a size, an mtime, and a digest when `compare` asks
for one. Ten thousand entries is on the order of a megabyte, which the existing framing
carries without ceremony.

An empty target yields a full transfer; a converged target yields a `done` with nothing
before it, which is the honest idempotence `dir.copy` never had — **zero bytes on a second
run**, not merely zero writes.

### 2. An answer may be a sequence

ADR-0031's `answer` stays: one message, terminal, for every existing primitive. This adds
two kinds — a chunk and a terminator — carrying the ask's id so they cannot be confused
with another exchange on the same socket.

A transfer ends **only** on the terminator, which carries the number of files written. A
stream that stops without it is a failure, never a completed transfer. Without that rule a
dropped bridge is indistinguishable from a finished job, which is the failure mode most
worth designing against.

### 3. A file is staged and renamed, so a drop leaves no half-written destination

Each file is written next to its destination and renamed over it once its last chunk has
arrived — the same rule #298 already imposes on `file.write`, for
the same reason: a reader must never catch a partial file.

**Resume falls out of the manifest and needs no protocol of its own.** A transfer
interrupted by a dropped bridge fails the step; the retry ([ADR-0031](0031-control-channel-and-detachment.md)
§2, made real by #347) starts a new one, whose manifest now lists the files already
written — so they are not sent again. Restart is cheap by construction, and there is no
resume state to keep correct on either side.

Staging files are removed on the way out, so a failed transfer leaves the destination as
it was, plus nothing.

### 4. `compare` decides what "identical" means

`meta` — size and mtime — by default. `sha256` on request, computed on both sides while
building and reading the manifest.

sha256 and not md5: `file.download` and `file.put` already compare that way, and a second
digest convention would have to be justified at every reading.

The default's limit is documented rather than discovered: **size+mtime misses a change
that preserves both**. A restored backup with preserved timestamps is the realistic case.
It is stated in the def's comment and in `language.md`, next to `compare = "sha256"` as
the answer.

### 5. `delete` is a parameter, not a second primitive

The terminator carries the target paths absent from the source. The agent removes them
when `delete = "true"` and ignores the list otherwise.

Computed on the control host, from the manifest it already received: the side that knows
the source decides what is extra. Sent with the terminator rather than as it goes, so a
transfer that never terminates deletes nothing.

## Rejected alternatives

- **Chunk the existing one-message answer, no manifest.** Removes the ceiling and nothing
  else: every run re-sends a tree that is already there. The ceiling is the symptom; the
  bytes are the cost.
- **One ask per file.** Sends only what is missing, at the price of a round trip each —
  minutes on a large tree, over a link whose latency is the whole problem.
- **The control host sends its manifest and the agent asks for what it needs.** Symmetric,
  and worse: the agent's asks are either one per file (above) or a batched list, which is
  a second negotiation round for information the control host could have acted on
  directly.
- **A resume protocol** — offsets, partial-file state, a transfer that picks up where it
  stopped. Real complexity, on both sides, for a case the manifest already handles: a
  restarted transfer skips whole files that arrived. Only a single interrupted file is
  re-sent, and it was never valid at the destination.
- **Delegating to `rsync`.** Already rejected in ADR-0036: it must be installed on the
  target, which shellf assumes nowhere — the agent exists precisely to avoid that class of
  dependency.

## Consequences

- `internal/proto` gains the two message kinds and the manifest entry; `internal/agent`
  gains the walk, the staging writes and the delete pass; `internal/orchestrator` gains
  the delta computation.
- **`dir.copy` becomes a def** over `~dir.sync` with `delete = "false"`
  (ADR-0036 §6), and `resolveDirCopy` — the last control-side transformation, after
  `file.template` lost its own in #334 — is deleted with the 32 MB ceiling.
- **Breaking for existing plans**: `dir.copy("tree", …)` becomes
  `dir.copy(%"tree", …)`. The source is read on the control host, and since #332 that is
  what `%` says. Today the marker is absent because the expansion happens before the plan
  is sent; once it is a def, the argument crosses the boundary like any other.
- The allow-list ([ADR-0031](0031-control-channel-and-detachment.md) §3) covers the sync
  the same way it covers a read: the plan declares `%"tree"`, and a path the plan never
  wrote is refused by name.
- **`dir.sync` as a def** — a sync that deletes — is *not* decided here. ADR-0036 §6 names
  only `dir.copy`, and exposing deletion under a name one letter from `copy` deserves its
  own record rather than arriving as a side effect of this one.
