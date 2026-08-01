# ADR 0012 — Shell interpreter selection

## Status

Active

## Context

`shell` blocks are hardwired to `/bin/sh -c` with `set -e` injected (`set -o
pipefail` dropped as a bashism — the spec drift flagged in #104). But fleets are
heterogeneous: bash-isms (arrays, `pipefail`) are legitimate, and non-POSIX
shells (`nu`) exist on some hosts. The interpreter must be selectable — without
ever letting a body written for one shell silently run under another. Converged
in discussion (2026-08-01). See #124.

## Decision

### 1. The interpreter is a property of the code, with a fleet default

A shell body is written in **one** language. The interpreter of a `shell` block
resolves in this order:

1. **Block annotation** — `shell(bash) { … }`: the code declares its own language.
2. **Def-declared interpreter** — a def pins the language of its blocks; the
   **stdlib pins `sh`**. **Inventory never applies inside def bodies** (a def's
   shell is authored code with a known language, not fleet configuration).
3. **Inventory per-host** — an `interpreter: "bash"` field, alongside user/port.
4. **Inventory `defaults`** — the fleet-wide default.
5. **`/bin/sh`** — the permanent fallback.

### 2. One `on` = one interpreter

At `on`-block resolution, every targeted host must resolve to the **identical**
interpreter — not merely the same *family* (`bash` + `dash` silently diverge on
bash-isms; "same family" is not "same behaviour"). A mismatch is a **pre-flight
error, before any SSH**: *"annotate the block or split the group."*

Checked at **use** (`on` resolution), not at group declaration: a host belongs to
several groups, and a mixed group that never receives shell steps is harmless. An
**annotated** block (`shell(bash) { … }`) is uniform by construction, so it works
across a heterogeneous fleet via separate `on` blocks.

### 3. Engine-known interpreter table (closed set, extensible)

| interpreter | injection |
|---|---|
| `sh`, `dash` | `set -e` |
| `bash` | `set -e` + `set -o pipefail` (finally legitimate — closes the #104 drift) |
| `nu` | none (it halts on error by default) |
| `raw` | none — keeps meaning "no net", as today |

An unknown interpreter name is a **parse/resolve error**. **Ship `sh` + `bash`
first**; `nu` is design-proofing, not a launch target.

### 4. Pre-flight existence check

`command -v <interp>` on every targeted host, with a clean error, so a missing
interpreter is discovered **before** the plan runs, not mid-apply.

## Rejected alternatives

- **A plan-level global interpreter** — the inventory `defaults` field *is* the
  fleet-wide global; a second global is redundant.
- **Per-family tolerance within an `on`** (allow `bash` + `dash` together) —
  bash-isms run fine under bash and silently misbehave under dash. Require the
  identical interpreter, not the same family.
- **Inventory applying inside def bodies** — a def's shell has a known authored
  language; letting the fleet reinterpret it would break the def on some hosts.

## Consequences

- **Grammar**: `shell(<interp>) { … }` / `shell(<interp>) <line>` (extend the
  existing raw-`shell` slot); a def may declare its interpreter; an unknown name
  errors at parse with line:col.
- **Inventory**: a reserved `interpreter` field (per-host + `defaults`), flowing
  like user/port.
- **Orchestrator**: resolve each shell step's interpreter per the order above;
  enforce one-`on`-one-interpreter at resolution (a pre-flight error naming the
  offending hosts); run the `command -v` pre-flight.
- **Engine/agent**: the interpreter table (per-interpreter injection); wire the
  chosen interpreter through `proto.Step`.
- **`docs/language.md`** gains a `shell(<interp>)` section — it may land with the
  implementation PR rather than this ADR.
- Implemented in #126 (follow-up); this ADR is docs-only.
