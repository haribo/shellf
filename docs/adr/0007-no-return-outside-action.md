# ADR 0007 — No `return` outside an action: the nominal return lives in `apply`

## Status

Active

## Context

ADR-0006 unified def conditionals on `if`/`return`, but left the **nominal
success outcome** as a bare `return <outcome>` at the def top level, outside any
phase:

```
def install(pkg: str) {
    pre-check { … }
    guard { … }
    apply { … }
    return ok.installed   ← floating: outside every action
}
```

A `return` outside an action is a design smell: it has no home. Every other
outcome is produced **inside** a phase (`pre-check` validates, `guard` skips,
`apply` errors). Read-only questions (`dir-exists`) already return only inside
`check` — they never float. Only instructions carried this floating return.

The tension that produced it: in `--check`, `apply` is **not executed** (we do
not mutate the target), yet the preview must still announce `would.installed`.
So the nominal tag must be knowable *without running `apply`*. ADR-0006 solved
that by hoisting the outcome to def level — trading uniformity for a place to
read the tag from. That trade was wrong; the tag can live in `apply` and still
be read statically. See issue #84.

## Decision

### 1. No `return` at def top level

Every `return` lives inside a phase. A `return` between phases is a parse error.

### 2. The nominal success outcome is the last statement of `apply`

The outcome of a successful `apply` **belongs to** `apply`:

```
def install(pkg: str) {
    pre-check {
        if pkg == "" { return err.pkgMustNotBeNull }
    }
    guard {
        r = shell { dpkg -s "$pkg" }
        if r.ok { return ok.alreadyInstalled }
    }
    apply {
        r = shell { apt-get install -y "$pkg" }
        if r.exit != 0 { return err.runtime(r) }
        return ok.installed
    }
}
```

### 3. `would` is derived statically from apply's trailing return

In `--check`, `apply` is never executed. The engine reads the **last top-level
`return` of `apply`** (the nominal return) to synthesize `would.<tag>` — a
static read, no effect runs. This is the same tag that a real `apply` would
reach on success, so preview and apply agree by construction.

### 4. Implicit `ok`

If `apply` has no trailing nominal `return` (only effects and conditional error
returns), the def returns `ok` **implicitly** — an `ok` with no tag. In check,
that is a `would` with no tag. Instructions are not forced to name their success.

## Rejected alternatives

- **Keep the floating def-level return** (ADR-0006) — a `return` outside an
  action; non-uniform with questions; the smell this ADR removes.
- **A dedicated `result` / `outcome` phase** (`result { return ok.installed }`)
  — a phase whose only job is to declare a tag. Extra concept for something that
  already belongs to `apply`: the outcome of a successful apply *is* the apply's
  result. More ceremony, no gain.

## Consequences

- **Parser**: `def()` no longer accepts a top-level `return` (error). `def.Return`
  becomes a **derived** field — the last top-level `ReturnStmt` of the `apply`
  phase — not standalone syntax.
- **Evaluator**: `retTag` reads apply's trailing nominal return; `ok` with no tag
  when absent. Pass-2 `apply` still short-circuits on any earlier `return`
  (error/guard-style); reaching the end yields the nominal outcome.
- **Stdlib**: migrate all 11 defs — move each floating `return ok.<tag>` to the
  end of its `apply` block.
- **Refines ADR-0006** (return *placement*); does not reverse it — `if`/`return`
  and the phase model stand. ADR-0006 stays Active.
- Implemented in a follow-up `type: feature` issue (this ADR is docs-only).
