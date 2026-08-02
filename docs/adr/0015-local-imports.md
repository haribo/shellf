# ADR 0015 — Local imports: alias-qualified directory packages

## Status

Active. Implements the **local** half of [ADR-0002](0002-module-distribution.md)
(§5 "local imports ship before remote") and extends the package model of
[ADR-0014](0014-user-defs-directory-package.md) across directory boundaries.
Remote (git + SHA + lockfile) is a later step (phase 3b); nothing here touches
the network.

## Context

ADR-0014 made the plan's directory a package: user defs written in a sibling
file resolve by name, no flag, no import. But reuse stops at the directory —
using a def from *another* directory was explicitly deferred to "the imports
phase". This ADR does the first, local slice of it.

ADR-0002 already locked the direction (git + SHA, no registry, control-host
resolution, direct imports only, local before remote). The open questions are
the concrete **local** mechanics: syntax, namespacing, and how imported defs —
resolved on the control host — reach the agent.

## Decision

### 1. `import <alias> "<relative-dir>"` imports a directory package

An import binds an **alias** to another package (a directory), by a path
relative to the importing file's directory:

```
import web "../shared"
```

A directory, not a file, is imported — the package is the unit (ADR-0014). The
imported directory's `*.shellf` files contribute their defs.

### 2. Calls are alias-qualified

An imported def is called `<alias>.<def>`; its source name stays bare.

```
on target {
    web.deploy("8080")   # the `deploy` def from package ../shared
}
```

Qualification makes cross-package names collision-free by construction — the
same shape as the stdlib's own qualified names (`apt.install`), whose prefix
comes from the embed directory. Flat merging was rejected (below).

### 3. An imported package is a def-only library

The imported directory provides **defs only**. It may not contain `on` blocks
(it is a library, not a plan) and may not itself `import` — there are **no
transitive dependencies** in v1 (ADR-0002 §5: this avoids version-resolution
hell). Both are errors, raised locally.

### 4. Resolution on the control host; qualified defs ship to the agent

Imports resolve before the agent is pushed (ADR-0002 §4): the control host reads
the imported directories, qualifies their defs, and ships them to the target
with the rest of the package. The agent needs no filesystem access to the
imported paths.

This generalizes ADR-0014's transport. A def source is bare (`def deploy(...)`),
but it must register on the agent under its **qualified** name (`web.deploy`).
So the per-host request carries a **name → source map** (keyed by the resolved
instruction name — bare for the local package, qualified for imports) rather
than ADR-0014's single concatenated blob. The agent parses each source and
registers it under its key.

### 5. Errors, all raised locally before any host

- a duplicate alias (`import web …` twice);
- a qualified name that collides with a stdlib qualified name (e.g. aliasing
  `apt` and defining `install`);
- an import path that is not an existing directory;
- an imported package that contains an `on` block or its own `import`.

## Rejected alternatives

- **Flat namespacing** (imported defs merge into the caller's namespace).
  Collision-prone across packages, and needs a shadowing rule; alias
  qualification is collision-free and mirrors the stdlib. Rejected in discussion.
- **Importing a single file** (`import "../shared/web.shellf"`). The package unit
  is the directory (ADR-0014); importing a file would introduce a second, finer
  unit for no gain.
- **Transitive imports** (an imported package importing another). Deferred
  (ADR-0002 §5) — it is the version-resolution hell this design avoids in v1.
- **Remote imports in this slice.** git + SHA + lockfile + integrity is phase 3b;
  local is ~80% of the value with no network and no trust problem.
- **Shipping parsed defs.** Same reason as ADR-0014: interface-typed AST fields
  aren't JSON-round-trippable and `proto` must not import `lang`; source travels
  as text, now keyed by name.

## Consequences

- Cross-directory reuse works locally: `import web "../shared"` then
  `web.deploy(...)`, no network, no lockfile.
- The grammar gains `import <alias> "<path>"`; call names may be alias-qualified.
- The transport becomes a name → source map (from ADR-0014's blob); the agent
  registers each def under its resolved name.
- Bare-name limitation of ADR-0014 is unchanged for the *local* package; imported
  defs are reachable only through their alias, so a bare stdlib name is still not
  shadowable by an import (an override still needs `override def` in the local
  package).
- Remote distribution (git + SHA + `shellf.lock`, integrity) remains phase 3b,
  building on this resolver.
