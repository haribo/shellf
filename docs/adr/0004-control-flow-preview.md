# ADR 0004 — Control flow and preview honesty

## Status

Active

> **Note (ADR-0008):** the captured-result **category** test spelled `x.ok` / `x.err`
> in this ADR is superseded by the outcome pattern `x == ok` / `x == err`
> (with optional tag, e.g. `x == err.dbLocked`). `x.changed` and the `if`/preview
> model below are unchanged. Examples here predate that spelling.

## Context

Plans need conditionals to read better than Ansible's `var + stat + when`. But **previewability** is a thesis pillar, so we must decide exactly how an `if` behaves in check mode — otherwise the preview lies like Ansible's `--check`. See issue #45.

## Decisions

### 1. `if <call> { then } [else { … }]`

The condition is an **instruction call**; the branch is taken on the call's Result `.ok`. `if s1` is sugar for `if s1.ok` (bare identifier = its Result's `.ok`). The condition runs **on the target** (the agent), which becomes a small flow interpreter.

### 2. Preview — never-lie (option B)

In check mode the condition is evaluated, and:

- Cond yields **`ok`** (idempotent state already reached) or **`err`** → **deterministic**: take the corresponding branch and preview it.
- Cond yields **`would.*`** (the effect would run but is not applied in check) → the branch is **`undetermined`**: the agent reports the condition as undetermined and previews the then-branch as *conditional*, never silently guessing it will pass.

This is honest: what we cannot know (the result of an unapplied action) is marked, not invented.

### 3. Read-only questions are evaluated (documented limit)

A pure read-only question (`dir.exists`, a `test` shell) **is** evaluated in check — it must be, or the whole preview (which rests on read-only guards) is worthless. It reflects state **at check time**, so it can differ after apply if it reads an effect the plan itself produces. This residual dishonesty is **not detectable** (the shell is opaque). The fix is design, not machinery: put the effect **in** the `if` (`if dir-create("/opt") { … }`) so it falls under §2's never-lie case, instead of splitting the effect and a later `test`.

## Rejected alternatives

- **Ansible-style best-effort** — silently evaluate the condition against current state and guess the branch, including for unapplied effects. This is the `--check` lie shellf exists to beat.
- **`if <action>` as a raw boolean** — an action is not a bool; `if s1` is defined as `if s1.ok`, keeping the question/instruction split (ADR-0003-adjacent).

## Consequences

- `proto.Step` gains a conditional form (`If{Cond, Then, Else}`); the agent interprets it.
- Delivered in increments (epic #45): flow with an instruction condition (PR1), named result capture + `if s1.ok/.changed` (PR2), read-only questions (PR3), `if !cond` negation + removal of plan-level `unless` (PR4).
- **Negation before deprecation** (PR4): `unless` is a *negative* guard; `if` had no negation, so removing `unless` outright would force a verbose, inverted rewrite. Adding `if !cond` first lets `if !shell { g } { cmd }` subsume `unless` concisely; only then is `unless` removed from plans.
- **Read-only questions need no keyword** (PR3): a question is a def whose decision lives entirely in read-only phases (no `apply`), so it resolves in pass 1 and is deterministic in check — never `would`, hence never `undetermined` as an `if` condition. The read/write distinction is carried by naming (`-exists` vs `-ensure`).
  - **Amended by [ADR-0051](0051-a-failing-question-is-would-in-check.md)**: deterministic in check only when the answer is *yes*. A question that answers **no** in check reports `would`, because a `no` is often the state the plan is about to create — the reasoning above holds for the current state, and check mode is precisely where the plan has not yet changed it.
- `docs/language.md` documents `if`/`else` and the preview semantics.
