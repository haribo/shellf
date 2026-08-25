# ADR 0050 — A verdict is observed, not asserted

## Status

Active. Extends [ADR-0013](0013-observe-state-contract.md) (observe/desired decides
whether to act) to what happens *after* acting, and narrows
[ADR-0037](0037-explicit-verdict.md): an `apply` still names its verdict, but that name is
now a proposal rather than an establishment.

## Context

An instruction's outcome comes from the exit code of its apply shell, and from nothing
else. The evaluator runs its decision phases, and if the state has drifted it runs the
apply and reports whatever that phase returned (`internal/lang/eval.go:166-177`):

```
apply {
    r = shell { … }
    if !r { return err.runtime(r) }
    return ok.converged          <- asserted, never checked
}
```

`observe` runs **before** the apply, to decide whether to act
(`internal/lang/eval.go:120-133`). Nothing runs after it. A def whose commands all exit 0
and whose effect never landed reports success, and the run is green over a wrong machine.

### The evidence

Six defects, one shape: a green verdict over a machine in the wrong state. Each was fixed
on its own terms — a sharper `observe` for #480, #486 and #507, parameter type checking for
#418 ([ADR-0045](0045-parameter-types-are-checked-by-value.md)) — and none of the fixes
addressed the reason a wrong verdict could be printed at all.

| Issue | Reported | Actual state |
| ----- | -------- | ------------ |
| #390 | `ok.copied` | tree landed as the wrong user |
| #411 | `ok.done` | file was empty |
| #418 | `ok.converged` | service stopped |
| #480 | `ok.already` | key inside the tree still root-owned |
| #486 | `ok.already` | package not installed (dpkg record only) |
| #507 | `ok.already` | directory did not exist |

The seventh was found while building the harness meant to catch the sixth, and it is the
one that names the cause, because here the `observe` was **not** at fault:

```
service.ensure(name=ssh, running=true, enabled=true)   ->  ok.converged
systemctl is-active ssh                                ->  inactive
```

The observe read the state correctly and saw drift. The apply ran `start` then `enable`,
both exited 0. The unit was stopped by the time the def returned. `ok.converged` was
printed by a def that had every means to know better and never looked.

The trigger there is specific to `ssh` in the e2e image and is not claimed to be general
systemd behaviour. What is general is the shape: **any cause that undoes an apply between
its commands, or after them, produces a green lie.** A unit that fails after starting, a
package whose `postinst` reverts, a write to a filesystem that fills up — the def cannot
tell, because it stops looking the moment it starts acting.

### Why sharpening observes is not the answer

Each fix was correct and none of them generalises. They improve the inputs to the
*decision* to act; the defect is in the *report* of having acted. A stdlib of 38 defs, about to grow
by eight (#498), cannot be kept honest by hoping every author remembers — the rule has to
live where the phases are evaluated.

## Decision

### 1. A def that observes confirms what it did

When an `apply` acts, the evaluator re-runs the def's `observe` phase and compares it to
the desired state, exactly as it did before acting. The verdict is then:

| Re-observed state | Verdict |
| ----------------- | ------- |
| matches desired | the apply's own outcome (`ok.<tag>`, `Changed`) |
| still differs | `err.unconfirmed`, naming the field that did not move |

The apply's `return` proposes the verdict. The re-observation establishes it.

### 2. It runs only where it can mean something

| Condition | Re-observed |
| --------- | ----------- |
| the def declares no `observe` (action-shaped, [ADR-0029](0029-action-preview.md)) | no — there is no state to confirm, and a restart restarting is the point |
| the apply did not act (`ev.acted` false) | no — nothing happened to confirm |
| the apply returned an `err` | no — the failure is already reported, and re-reading state would replace a precise message with a vague one |
| mode is `Check` or `Status` | no — the apply does not run |
| the def is a delegation ([ADR-0037](0037-explicit-verdict.md) §2) | no — the callee confirms its own work |

So the cost is one extra observe round trip, on the acting path only. A converged run —
the common case for a plan that has run before — costs exactly what it costs today.

### 3. `err.unconfirmed` names the field

The message states which observed field still disagrees and with what, because "the apply
did not take effect" sends the reader nowhere:

```
service.ensure(name=ssh, running=true, enabled=true) err.unconfirmed
    ! running: observed false, desired true — the apply ran and the state did not change
```

This is the same rule as [#470](https://github.com/haribo/shellf/issues/470): a failure
reports what was actually seen, not a summary of it.

### 4. It is not opt-in

No marker, no per-def flag. A def that needs confirming is precisely the one whose author
did not think about it — the six defects above were all written by someone who believed the
apply worked. An opt-in rule is a rule that protects the defs which never needed it.

## Consequences

- **Some currently-green runs turn red.** That is the point, and it is a breaking change in
  behaviour rather than in syntax: a def whose observe is stricter than what its apply
  produces will now say so instead of lying. Each one found is a bug that already existed.
- **A def's `observe` becomes load-bearing twice.** An imprecise observe now produces false
  `err.unconfirmed` as well as false `already`. That raises the bar on writing one, which is
  the correct direction — it is the phase the whole model rests on.
- **The stdlib must be swept before this lands**, def by def, for observes too coarse to
  confirm their own apply. Doing it after would mean discovering them as CI failures.
- **Cost is bounded and measurable.** One shell per acting def. The coverage plan is the
  place to measure it: count the shells issued before and after, and record the number
  rather than asserting it is negligible.
- **It does not make a composite atomic.** A multi-step apply that fails halfway still
  leaves a partial state; confirming the final state says the step failed, not that it was
  undone. Rollback stays out of scope
  ([ADR-0040](0040-rerunnable-steps-and-unsafe-shell.md)).

## Rejected alternatives

### Re-observe, but only report — never fail

The verdict would carry "acted, state not confirmed" without turning the run red. Rejected:
a plan that continues past a step which did not take effect is the current behaviour with
extra words. Every one of the six defects above was survivable-looking at the moment it
happened, and damaging later — a key unreadable by its daemon, a service down after reboot.

### An opt-in phase or marker on the def

Cheapest, and it rots. See §4.

### Make `apply` idempotent-by-construction instead

Require every apply to end in a state its own observe would accept, and verify that
statically. There is no static analysis of shell that could do this — the same reason the
`unsafe shell` detector is a documented heuristic (ADR-0040 §5).

### Leave it, and keep sharpening observes

The status quo. Six defects in, each fixed individually, with the seventh found by accident
while testing the sixth. The next one is already written and not yet noticed.

## Implementation notes

Not part of the decision, recorded to save the next reader the search:

- The two passes are `internal/lang/eval.go:120-133` (decision) and `:166-177` (effect).
  `ev.acted` (`changedIfActed`) already carries "did this apply do anything", which is the
  gate for §2.
- `EvalDefFull` already evaluates `observe` through `ev.evalObserve` and compares with
  `converged(observed, desired)`; the re-observation reuses both, with the same `desired`
  map computed at entry.
- The stdlib sweep of §Consequences is issue-tracked, not ADR material: the decision is
  what a verdict means, not which defs currently fail it.
