# ADR 0041 — An `apply` that cannot act in check mode is evaluated there

## Status

Active. **Extends [ADR-0029](0029-action-preview.md)**, which defined the action-shaped def
and its `preview`, and [ADR-0035](0035-phases-and-modes.md), whose phase/mode table gains
one row. Neither is reversed.

## Context

Measured against a converged host, side by side (#380):

| Def | `--dry-run` says | Truth |
|---|---|---|
| `file.template` | `ok.already` | nothing would happen |
| `dir.sync` | `would.synced` + *`0 file(s) would be transferred`* | nothing would happen |
| `dir.copy` | `would.copied` | nothing would happen |
| `file.copy` | `would.copied` | nothing would happen |

Three defs announce writes that would not happen, and `dir.sync` contradicts its own
verdict in its preview line. shellf's thesis is that a run is readable *before* it happens;
an operator who learns that `would` means "maybe" stops reading it.

The information is not missing. In check mode `engine.Run` runs the instruction's `Guard`
**before** deciding, so a converged `~file.write` already answers `already`
(`internal/engine/instruction.go`), and `~dir.sync` exchanges a manifest and counts without
writing a byte. The answer exists; it is produced inside `apply`, which check mode never
runs.

`file.template` escapes by being a delegation ([ADR-0037](0037-explicit-verdict.md) §2):
the callee's own phases run in every mode.

The obvious repair — give these defs an `observe` — is the one that must not be taken. It
is what #378 removed: an `observe` re-deciding what the primitive already knows is a second
implementation of one rule, free to drift, and it did drift. `file.copy` reported
`already` over a stale file for months because of exactly that.

## Decision

### 1. An `apply` with no possible effect in check mode is evaluated in check mode

A def with **no `observe`** whose `apply` contains only primitive calls, control flow and
`return` is evaluated in check mode. Every primitive is inert there by construction —
`~file.read`, `~dir.list` and `~file.render` are reads; `~file.write` and `~dir.sync` go
through `engine.Run`, which guards then reports without applying — so evaluating the apply
changes nothing on the target.

The verdict then comes from what the evaluation found:

- nothing would change (no primitive reported a change) → **`ok.already`**
- something would → **`would.<tag>`**, the tag from the `return` the apply reached

One implementation of one rule, asked in two modes. No second question to keep in sync,
which is the property #378 was lost over.

### 2. `shell { }` in the `apply` disqualifies it, silently

A shell block can do anything, so an apply containing one cannot be evaluated in check
mode and keeps today's `would.<tag>`. This is a fallback, not an error: writing an apply
around a shell is legitimate and common, it merely costs dry-run precision.

Silently, because the alternative is a warning on a def that is written correctly. The cost
is stated in `language.md` instead, where an author looking for the precision will find how
to get it.

### 3. `preview` keeps its job, and only its job

A `preview` still describes what would happen and never decides ([ADR-0029](0029-action-preview.md)).
Under §1 it runs only when the apply found drift — on a converged host there is nothing to
describe. `dir.sync` keeps its preview: naming the files it would delete is worth a second
manifest exchange, and that exchange only happens when something would actually be deleted.

### 4. What this does not claim

An apply evaluated in check mode is a *prediction*, not a reservation. Between the dry-run
and the real run the target can change, and the two verdicts can differ. That was already
true of every `observe`; it is not made worse here, and it is not made better.

## Rejected alternatives

- **Give the three defs an `observe` again.** The #378 bug, reintroduced deliberately: a
  second implementation of the primitive's own question, free to drift from it.
- **Let `preview` return an outcome.** Smaller change, and it re-creates the same
  duplication one phase over — the preview would call the primitive, the apply would call
  it again, and the two could disagree. It also contradicts ADR-0029, which made `preview`
  describe rather than decide.
- **Make the three defs delegations.** `file.copy` would delegate to the `file.write` def,
  whose body pushes content through a shell variable — a null byte truncates the file
  silently. Binary safety is why `file.copy` uses the primitive at all.
- **Report `would` and let the preview text carry the truth**, as `dir.sync` does today.
  It asks the reader to trust the small print over the headline, and only works for defs
  that bothered to write a preview.
- **Evaluate *every* apply in check mode with the executor stubbed out.** A stub that
  answers plausibly is the fake-executor failure this project already records: it proves
  whatever the stub was written to prove.

## Consequences

- `file.copy`, `dir.copy` and `dir.sync` report `already` on a converged host in
  `--dry-run`, and `dir.sync`'s preview line stops contradicting its own verdict.
- A user def of the same shape gets the same precision with nothing to write.
- On drift, `dir.sync` costs two manifest exchanges in dry-run — one to decide, one to
  describe. Stated rather than discovered; dry-run is not the hot path, and a converged
  run pays nothing extra.
- The phase/mode table in `docs/language.md` gains the row, and the cost of putting a
  `shell` in an apply becomes documented rather than folklore.
