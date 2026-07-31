# ADR 0009 — Error handling: halt-on-err by default, `?` to catch

## Status

Active

## Context

Plans halt on the first `err` (halt-on-err, [ADR-0004](0004-control-flow-preview.md)):
a failing step stops the plan and its host, so nothing is built on a broken base.

[ADR-0008](0008-outcome-matching.md) added outcome matching (`s == err.dbLocked`)
to react to a *specific* error. But halt-on-err makes any error-testing branch
**unreachable**: the erroring instruction stops the plan *before* the
`if s == err…` runs. So `== err` is unusable unless the plan can say, for one
instruction, "I am handling this — don't halt yet." We must add that marker
without disarming the net for everything else, and without letting an
unanticipated error pass silently (the Ansible sin). See issue #91.

## Decision

### 1. Halt-on-err stays the default

An **unmarked** instruction that returns `err` stops the plan (and that host).
Unchanged. This is the safety net.

### 2. `?` marks a failible instruction as caught

A trailing `?` on an instruction call means *"do not halt automatically — I will
handle this result."* It **defers** the halt so the following branch(es) can test
the error.

```
x = apt.install("nginx")?                       # captured form
if apt.install("nginx")? == err.dbLocked { … }  # inline form
```

### 3. `== err[.tag]` requires a `?` on its source

Comparing a result to `err` (or `err.<tag>`) is only meaningful when the error
can actually reach the test. Without `?`, halt-on-err makes that branch **dead
code** — a lie in the source. So a `== err…` whose subject does not trace back to
a `?`-marked instruction is a **compile error**:

```
if apt.install("nginx") == err.dbLocked { … }   # ERROR: unreachable error test —
                                                 # mark the instruction with `?`
```

`== ok` / `!= err` stay free: success is always reachable, no `?` needed.

### 4. A caught error that no branch covers still halts

`?` means *"let me try to handle it"*, not *"ignore it"*. After the handling
branch(es):

- the error is **matched** by a branch (`== err.<tag>`) or by a trailing `else`
  → that branch runs, the plan continues;
- the error is covered by **no** branch and **no** `else` → **halt**.

Invariant: **no error passes silently — it is either explicitly handled or it
halts.**

### 5. `else` is the explicit catch-all

```
if apt.install("nginx")? == err.dbLocked { inst1() } else { inst2() }
```

reads *"dbLocked → inst1, otherwise → inst2"* and does exactly that — `inst2`
also runs on other errors and on `ok`. Writing `else` is **taking
responsibility** for the rest; it is visible in the source, not silent. Without
`else`, uncovered errors halt (§4). "Read = run."

### Worked example

```
# handle one error, let the rest halt
if apt.install("nginx")? == err.dbLocked {
    # apt lock busy → wait & retry
}
# diskFull, netTimeout, … → covered by nothing → halt

# handle several, explicit catch-all
x = apt.install("nginx")?
if x == err.dbLocked      { retry() }
else if x == err.netTimeout { backoff() }
else                        { report(x) }   # everything else (incl. ok) → report
```

`?` opens a catch, each `== err.<tag>` is a typed `catch`, an uncovered error is
a `throw` that halts, and `else` is the catch-all.

## Rejected alternatives

- **Capture alone = "handled"** (no `?`) — too broad. Capturing is often just for
  `.changed` / `== ok`, not error handling; treating every capture as "handled"
  would disarm the net when no error is actually handled, and let an
  unanticipated error (`diskFull` when only `dbLocked` is tested) pass silently.
  `?` makes the intent explicit and local.
- **Forbid `else` after `?`** — breaks "read = run": `if … else …` must do what it
  reads. `else` is the legitimate, visible catch-all. Kept.
- **Allow `== err` without `?`** — dead code, unreachable under halt-on-err; a
  lie in the source. Made a compile error instead.

## Consequences

- **Grammar**: a `?` postfix on an instruction call, in capture position
  (`x = call()?`) and inline-condition position (`if call()? == …`).
- **Static check**: a `== err[.tag]` test requires its subject to trace back to a
  `?`-marked instruction; otherwise a compile error.
- **Evaluator/agent**: `?` defers the halt; the engine tracks whether the caught
  error is covered by a branch or `else`; uncovered → halt. The implementation
  must define the *handling site* for the captured form (the `if`-chain that
  consumes `x`).
- **Depends on** inline-call outcome matching (deferred in ADR-0008) for the
  inline `if call()? == err` form; the captured form works with what exists.
- Implemented in follow-up `type: feature` issue(s); this ADR is docs-only.
