# ADR 0030 — calling a def from a def

## Status

Active. Extends the def contract of [ADR-0013](0013-observe-state-contract.md) and
the grammar of [ADR-0006](0006-def-grammar-v2.md) by allowing an instruction call
inside a def body. Applies [ADR-0011](0011-privilege-escalation.md) (escalation),
[ADR-0009](0009-error-handling.md) (halting and `?`) and
[ADR-0003](0003-variable-scoping.md) (scoping) unchanged to this new position —
it deliberately introduces no rule of its own where an existing one answers.

## Context

`internal/lang/eval.go` refuses every instruction call inside a def body:

    ev.fail("instruction calls are not supported yet: %q", x.Name)

So the only tool available inside a def is a raw `shell { }` block. Not one stdlib
def calls another — they are not permitted to. The stdlib does not compose: every
def is an island.

The cost is concrete. A `def sudoers(name, content)` that drops a file in
`/etc/sudoers.d/` and validates it with `visudo` cannot reuse `file-write` to place
the file — not even without changing anything about it. It must reimplement writing
a file in shell, and so must every user def that adapts an existing behaviour to a
system shellf does not ship support for. That is the language's real closedness, and
for a config language it is a standing adoption blocker
([ADR-0020](0020-design-for-the-user-base.md)): a user who cannot build on what
exists rewrites it, or leaves.

Writing one's own defs and importing them already works
([ADR-0014](0014-user-defs-directory-package.md),
[ADR-0015](0015-local-imports.md), [ADR-0016](0016-remote-modules.md)) — the
language is open for *writing*. It is closed for *reuse*.

The call syntax already parses; only the evaluator refuses, and the `yet` in that
message records the limit as deferred rather than decided against. Lifting it,
however, settles six semantics at once, none of which any ADR covers. This ADR
settles them; the implementation is a separate change.

## Decision

### 1. A callee sees its arguments, nothing else

The called def receives its declared parameters (plus their defaults, plus any
`with` binding on that call) and **no** let or parameter of the caller. Precedence
inside the callee is unchanged: `with` > per-host > global
([ADR-0022](0022-with-block.md)).

This is the same contract a def already has when a plan calls it, so a def means the
same thing wherever it is called from — the property that lets a reader understand
`file-write` by reading `file-write`. Inheriting the caller's scope would make a def
behave differently depending on who called it, and force reading every call site to
understand a callee.

### 2. Escalation is inherited; an intrinsic `as` still wins

A callee with no `as` runs under the caller's escalation. A callee declaring its own
`as <user>` runs as that user, overriding the caller's.

This is exactly ADR-0011's existing rule for an enclosing `as <user> { … }` block,
applied to a def body — a def is an enclosing scope like any other. The alternative
(a callee always starting unprivileged) would make most of the stdlib unusable from
a def needing root, since `def sudoers(...) as root` could not delegate to
`file-write`.

### 3. `changed` propagates automatically

When a callee's Result is `changed`, the caller's Result is `changed` too, without
the caller having to restate it. Capturing the callee (`r = file-write(...)`) and
reading `r.changed` stays available for a def that needs to branch on it.

`changed` means "something actually acted" ([ADR-0010](0010-result-and-shellresult-model.md));
if a callee wrote a file, the caller did act, and saying otherwise is a lie in the
sense of ADR-0013. The failure mode of the alternative decides it: a def author
forgetting to restate `changed` yields an instruction that modifies a file yet
reports `ok.already` — every downstream `if x.changed { service-restart(...) }`
silently stops firing, with no error anywhere. A one-line omission must not produce
an invisible outage.

### 4. An error halts the caller; `?` is available

An `err` from a callee halts the caller, and through it the run — the ordinary rule
of ADR-0009, applied unchanged. A def that wants to handle the failure itself marks
the call `?`, exactly as in a plan.

The point is that a call behaves the same in a def body as in a plan; two halting
rules depending on where the call sits would be a trap.

### 5. Legal in `observe` and in `apply`, with Check honouring each

A call is allowed in the read-only decision phases (`pre-check`, `check`, `observe`)
and in the effectful ones (`apply`, `post`).

In `--check`, the existing per-phase contract governs the callee as it governs any
step: a call reached from a read-only phase runs (it only reads); a call reached from
`apply`/`post` does not. The callee is evaluated in the caller's mode, so nothing
effectful executes in Check.

Restricting calls to `apply` would amputate the main source of duplication:
observing state is where defs repeat each other most (a `test -f`, a `cmp`, a
`systemctl is-active`).

### 6. Call cycles are rejected when defs are loaded

A cycle (`a` → `b` → `a`) is detected at def-load time and refused with the chain in
the message:

    cycle: sudoers -> file-write -> sudoers

A cycle is a writing error, like a syntax error: it must surface from reading the
files, before anything runs on a target. A runtime depth cap would surface it
mid-deploy, after part of the plan has already acted.

## Rejected alternatives

- **Per-call phase overriding** — `template(...) { validate { … } }`, the original
  proposal. Deferred, not dismissed. It presupposes what does not exist: one cannot
  specialise a brick that cannot yet be stacked, so composition comes first. It also
  breaks the local-reading property the language claims explicitly for `with`
  ("explicit, local inputs — no need to read the file to know what it uses",
  `docs/language.md`) — under overriding, knowing what `template(...)` does requires
  reading every call site. And it would need its own answers to: replace or compose
  with the original phase, which phases are open vs sealed (overriding `apply`
  redefines the instruction under its own name), and what runs in `--check`. Revisit
  if composition proves insufficient in practice, with the failing cases in hand.
- **Callee inherits the caller's scope.** Shorter to write, but a def would then
  behave differently per call site, and a rename in the caller could silently change
  a callee's behaviour.
- **Caller must restate `changed`.** More explicit control, but its failure mode is
  a silent one: modified file, handler never fired, no error. Rejected on that
  asymmetry alone.
- **Calls allowed only in `apply`.** Simpler to guarantee in Check, but leaves the
  most duplicated code — observation — non-reusable.
- **Runtime depth cap for cycles.** Simpler to implement, fails in production
  instead of at load.

## Consequences

- A def may call any other def — stdlib or user — so `def sudoers(...)` reuses
  `file-write` and adds its `visudo` check on top, instead of reimplementing file
  writing in shell. The stdlib becomes a base to build on rather than a fixed set.
- No grammar change: the call syntax already parses. The evaluator resolves the name
  through the registry plans already use, evaluates the callee with the caller's
  mode and escalation, merges `changed`, and propagates `err` under ADR-0009.
- Def loading gains a cycle check over the call graph.
- `--check` keeps its guarantee: nothing effectful runs, because the callee inherits
  the caller's mode and the existing per-phase rule decides.
- The def contract of ADR-0013 is unchanged: a def still declares state and actions;
  it may now delegate part of them.
