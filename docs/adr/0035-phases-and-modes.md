# ADR 0035 — Phase vocabulary: fold `pre-check`, drop `post`, rename `--check`

## Status

Active. Amends the def contract of [ADR-0013](0013-observe-state-contract.md) on
vocabulary only: `observe` and `apply`, the contract itself, are untouched. Renames the
flag that [ADR-0004](0004-control-flow-preview.md) calls `--check`.

## Context

Looking for where a content-validation phase could live surfaced three defects in the
phase vocabulary. Measured across the 31 stdlib instructions:

| Phase | Purpose | Instructions using it |
|---|---|---|
| `pre-check` | decide before acting; its outcome wins | **1** |
| `check` | same, read-only | **4** |
| `observe` | report current state; equal to desired = skip | **22** |
| `preview` | describe what apply would do | **2** |
| `apply` | act | **27** |
| `post` | after apply | **0** |

- **`pre-check` and `check` are one phase.** `internal/lang/eval.go` handles them on a
  single line (`case "pre-check", "check":`). The only divergence is that `status` runs
  `pre-check` and not `check`.
- **`post` is dead.** Nothing declares it, so nothing depends on it.
- **`check` the phase and `--check` the mode name different things.** `--check` runs
  `pre-check`, `check`, `observe` *and* `preview`; the phase `check` also runs during a
  real apply. The only phase exclusive to the mode is `preview`. One word, two meanings,
  and the phase actually tied to the mode is called something else.

The third is the one that costs: a reader who learns `--check` reasonably infers it runs
"the check phase", which is both incomplete and misleading.

## Decision

### 1. `pre-check` folds into `check`

One phase for "decide before acting". The `status` divergence goes with it: `status`
runs `check` like any other read-only phase. The single instruction that relied on
`pre-check` (`apt.install`) keeps its behaviour under `check` — its outcome still wins
and still halts.

### 2. `post` is removed

Unused, and its meaning was never settled beyond "after apply". Removing an empty
concept costs nothing and shrinks what an author must learn. Re-adding it later, with a
purpose, is a smaller change than keeping a placeholder that means nothing.

### 3. The phase keeps its name

`check` was a candidate for renaming, on the grounds that it says what it does less
precisely than "question" — the word ADR-0013 already uses for `dir.exists` and friends.
Rejected: the collision that motivated it is with the *mode*, and renaming the mode
settles it. Renaming the phase as well would be a break that fixes nothing, and `check`
is understood everywhere.
### 4. The mode becomes `--dry-run`

`--preview` was the obvious candidate and is rejected: it repeats the mistake, since a
`preview` phase exists. `--dry-run` is a command-line term, not a language one, so it
cannot collide with a phase now or later.

Ansible uses `--check`, so it will be typed. An unknown-flag error naming the
replacement handles that — not an alias, which would keep both names alive forever.

### 5. The mapping is documented

`docs/language.md` gains one table saying which phases each mode runs. Its absence is
how the collision went unnoticed:

| Mode | check | observe | preview | apply |
|---|---|---|---|---|
| `run` | yes | yes | no | yes |
| `run --dry-run` | yes | yes | yes | **no** |
| `status` | yes | yes | no | no |

## Rejected alternatives

- **Keep `pre-check` for the `status` divergence.** A whole phase kept alive for one
  instruction's behaviour in one mode. If that behaviour matters it can be expressed
  without a second phase name.
- **`--preview` for the mode.** Collides with the `preview` phase — the exact defect
  being fixed.
- **Modes named after phases** (`--check`, `--observe`, `--preview` as cumulative
  levels). Rejected: it makes every mode the name of a phase, so an operator must know
  the internals of instructions to pick a flag. A mode names an intent — show me, tell
  me where things stand, do it — while phases are the instruction author's business.
  Worse, a mode running only the `check` phase would print almost nothing, since 4
  instructions of 31 have one.
- **Aliases during a transition.** Same reasoning as [ADR-0032](0032-stdlib-naming.md):
  one consumer today, and a good error beats two spellings kept forever.

## Consequences

- Breaking for any plan passing `--check`, and for any def declaring `pre-check` or
  `post`. Both are stdlib only — no plan declares phases.
- The parser refuses the removed names, and the CLI refuses the removed flag, each
  naming its replacement — the mechanism ADR-0032 already established.
- `eval.go` loses a branch; the phase table loses two entries.
- Nothing changes about `observe`/`apply`, so ADR-0013's contract stands as written.
