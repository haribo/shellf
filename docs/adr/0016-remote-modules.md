# ADR 0016 — Remote modules: git, SHA-pinned, lockfile-verified

## Status

Active. Implements the **remote** half of [ADR-0002](0002-module-distribution.md)
(git + SHA, no registry) and extends the import mechanism of
[ADR-0015](0015-local-imports.md) to git-hosted packages. This is phase 3b; local
imports (3a) shipped.

## Context

ADR-0015 made `import <alias> "<path>"` load a local directory package. ADR-0002
already locked the direction for sharing across machines: defs live in ordinary
git repos, imported by URL + version, pinned to an immutable commit SHA, verified
by a lockfile — no registry, no transitive dependencies. This ADR fixes the
concrete remote mechanics ADR-0002 left open: the spec syntax, what is fetched,
how, and how integrity is enforced.

The design deliberately stays a **minimal fetcher**, not a package manager. The
ecosystem trap (a central index nobody populates, version-resolution hell) is
what killed the anti-Ansible tools; shellf is excellent with zero modules
(ADR-0002 §6), so remote sharing is an accelerator, never a dependency.

## Decision

### 1. Same `import`; remote iff the spec carries a version

`import <alias> "<spec>"` is unchanged from ADR-0015. The spec is **remote when
it contains `@<version>`**, local otherwise:

```
import web "github.com/alice/shellf-web@v1.2.0"   # remote
import lib "../shared"                             # local (ADR-0015)
```

A local path never carries `@`, so the rule is unambiguous. Remote defs are
alias-qualified exactly like local ones (`web.deploy(...)`) — the parser and the
transport (ADR-0015) are unchanged; only *resolution* differs.

### 2. Tag or full SHA only; branches rejected

`@<version>` is a **git tag** or a **full commit SHA**. A branch is rejected: it
is mutable, so it cannot be pinned reproducibly (contradicting the lockfile). A
tag resolves to a SHA at first lock; the SHA is authoritative thereafter
(ADR-0002 §5).

### 3. A module is a repo of defs (root `*.shellf`)

The imported module is the repo's **root** `*.shellf` files — a def-only package
(same rules as ADR-0015: no `on` blocks, no nested `import`). Importing a
subdirectory of a repo (`…/mono/web`) is deferred; v1 is repo-root.

### 4. Fetch by shelling out to `git`; resolution on the control host

The control host runs `git` (`fetch` + `archive`/checkout at the SHA) to obtain
the module — no go-git dependency (git is a reasonable control-host requirement,
and the target still needs neither git nor network, ADR-0002 §4). The fetched
`*.shellf` sources are then handed to the same import path as ADR-0015.

### 5. Content-addressed cache + `shellf.lock`; offline after first lock

- **Cache**: fetched modules live under `~/.cache/shellf/modules/<sha>/`
  (content-addressed by commit SHA). A cached SHA is never re-fetched.
- **Lockfile**: `shellf.lock`, next to the plan, records per import spec the
  resolved **commit SHA** and a **content hash** (sha256 over the module's sorted
  `*.shellf` sources).
  - Not locked yet → resolve the tag to a SHA, fetch, hash, **write** the lock.
  - Already locked → use the locked SHA; fetch it if not cached; **verify** the
    content hash against the lock. A mismatch (a moved tag whose content changed)
    is a hard **error** — this is the whole immutability/trust guarantee
    (ADR-0002 §3).
- Once locked and cached, a run needs **no network** (air-gap friendly). First
  resolution of a new spec is the only step that fetches.

## Rejected alternatives

- **A go-git library** instead of shelling out. A large dependency for what
  `git fetch` does in one line; git on the control host is an acceptable ask.
- **Branches as versions.** Mutable — unpinnable, un-reproducible. Tag or SHA
  only.
- **A registry / central index.** ADR-0002 §2: git is the distribution; a
  registry is ~95% infrastructure plus a moderation problem for the worst
  value/effort on the roadmap.
- **Transitive dependencies** (a module importing another). ADR-0002 §5: the
  version-resolution hell (Go MVS, npm) this design avoids in v1. An imported
  module with its own `import` is an error (ADR-0015 rule, unchanged).
- **Semver ranges / MVS.** One exact version pinned per import; no range
  solving.
- **Subdirectory modules** (`repo/sub`). Deferred; v1 imports the repo root.

## Consequences

- Cross-machine sharing works: `import web "github.com/alice/web@v1.2.0"`,
  reproducible via `shellf.lock`, verified by content hash, cached for offline
  re-runs.
- The control host gains a `git` requirement (only when a remote import is used);
  the target is unaffected — it still receives assembled, qualified def sources
  (ADR-0015 transport).
- New surface: a git resolver, a module cache, and lockfile read/write/verify —
  all on the control host, all reusing ADR-0015's parse/transport path (only the
  CLI's import resolution branches local vs remote).
- Supply-chain note (ADR-0002 rationale): a module is raw shell run on production;
  SHA pinning + content-hash verification is mandatory. Signing/audit may follow
  if public sharing grows — out of scope here.
- Implementation follows in slices: git resolver + cache → lockfile write/verify
  → CLI wiring + integration.
