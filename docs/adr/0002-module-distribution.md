# ADR 0002 — Module distribution

## Status

Accepted (direction). The decisions that carry it out have their own records:
[ADR-0015](0015-local-imports.md) for local imports, [ADR-0016](0016-remote-modules.md)
for remote modules.

## Context

Real plans (e.g. the claude-fleet deployment) are mostly raw `shell {}` blocks,
which are neither idempotent nor previewable. Reusable, guarded instructions
(`download-file`, `untar`, `git-clone`, …) and a way to share them are wanted.

The open question is **how instructions are distributed** without falling into
the ecosystem trap that killed the anti-Ansible tools: their cold-start was a
central registry no one populated. shellf's thesis (`docs/design.md` §02) is the
opposite — be excellent with raw shell, so no ecosystem is *required*.

## Decisions

### 1. Instructions are plain files in git, imported and pinned by SHA

A `def` lives in a `.shellf` file in an ordinary git repo. A plan imports it by
URL + version; shellf pins the resolved **immutable commit SHA**.

```
import "github.com/alice/shellf-web@v1.2.0"
```

### 2. No registry — git is the distribution (the Go-modules model)

No shellf server, no accounts, no central index. `git fetch` at a SHA. GitHub /
GitLab / an internal git host all work. GitHub *is* the "registry".

### 3. Lockfile pins SHA + content hash

A `shellf.lock` (à la `go.sum`) records the commit SHA and a content hash. A
moved tag whose content no longer matches the lock is **rejected**. This is the
whole immutability/trust guarantee.

### 4. Resolution runs on the control host

Imports are resolved before the ephemeral agent is pushed; the plan reaches the
target already assembled. The agent needs no git and no network to GitHub —
consistent with the two-plane architecture (control resolves, agent executes).

### 5. Direct imports only; local before remote

| Rule | Reason |
|---|---|
| No transitive deps in v1 (a module cannot import) | avoids the version-resolution hell (Go MVS, npm node_modules) |
| Local imports (`./lib/web.shellf`) ship before remote | same mechanism, no network, no trust — ~80% of the value |
| SHA is authoritative; a tag is only writing sugar | mutable tags cannot be trusted |

### 6. Sharing is an optional accelerator, never a dependency

shellf must stay excellent with zero third-party modules. The day it is only
useful *with* an ecosystem, it dies of cold-start like its predecessors.

## Rationale

- A registry is ~95% infrastructure plus a trust/moderation problem, for the
  worst value/effort ratio on the roadmap. git + SHA gives distribution,
  immutability, and pinning for free.
- A shellf "module" is **raw shell run on production as root** — a sharper
  supply-chain risk than Ansible's structured modules. SHA pinning (never a
  mutable tag) is mandatory; signing/audit may follow if sharing goes public.

## Rejected alternative — a separate stdlib repo with `import core`

*(Amendment 2026-08-01: debated and rejected; recorded so it is not
re-litigated. Same decision clarified — Status unchanged, per ADR-0001 §4.)*

Proposal: move the stdlib to a separate `shellf-stdlib` repo, fetched via an
`import core` mechanism from GitHub. **Rejected**, because:

- The agent binary **embeds** the stdlib. An external repo means either re-embed
  at build time (the split is just repo plumbing) or fetch at runtime — which
  kills the air-gap / agentless story and adds supply-chain surface *on the
  targets*.
- `import core` needs the full phase-3 import design (resolution, lockfile,
  integrity/SHA, wire-shipping of defs) — a package manager, premature with zero
  third-party modules and zero users.
- The def grammar is still unstable (three breaking changes in one month:
  grammar v2, outcome matching, `?` catch), each migrating the stdlib **in the
  same commit**. A split turns every language change into lockstep PRs across two
  repos plus a shellf×libs compatibility matrix.
- The stdlib is the language's de-facto conformance suite: a language change that
  breaks a def must fail CI **in the same PR**.
- Precedent: Go/Rust/Python keep their stdlib in the toolchain repo even though
  it is written in the target language and maintained by different people;
  Terraform's provider split needed a registry + protocol and a massive user base
  to amortise it. Different-maintainer needs are covered in-repo by CODEOWNERS on
  `std/`.

**North star (locked):** the stdlib stays *extractable* — pure `.shellf`, zero Go
adhesion (#107 removed the last coupling), a documented contract. **Extraction
trigger:** the def grammar stable across N releases **and** the phase-3
import/distribution design done. `import core` as forward-compatible syntax
resolving to the embedded stdlib may be evaluated *within* that import design —
not before.

## Consequences

- 0.2+ needs: import syntax, a resolver, and a lockfile. Local imports first.
- No registry to build or operate.
- Transitive dependencies are explicitly out of scope for v1; revisit only if a
  concrete need appears.
- A small set of guarded builtins (`download-file`, `untar`, `git-clone`) is the
  near-term step — in Go, then expressible as custom `def`.
