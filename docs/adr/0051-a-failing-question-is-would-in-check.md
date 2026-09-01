# ADR 0051 — A question that answers *no* in check answers `would`

## Status

Active. **Amends [ADR-0004](0004-control-flow-preview.md)**, which decided a question is
"deterministic in check — never `would`". That holds for one of its two answers, and this
ADR narrows it to that one; ADR-0004 keeps its body and carries the link back.

Applies the reasoning of [ADR-0041](0041-inert-apply-in-check-mode.md) — evaluate in check
what check can answer, refuse to answer what it cannot — to the def shape
[ADR-0013](0013-observe-state-contract.md) calls a *question*: a `check` phase, no `apply`.

## Context

A **question** is a def whose whole body is a `check` phase: no `observe`, no `apply`. The
stdlib has four — `http.check`, `http.wait-for`, `dir.exists`, `file.exists`. They read the
world and report `ok` or `err`; there is nothing to converge and nothing to apply.

`check` runs in every mode (ADR-0035), which is right for what that rule was written for:
a def refusing its arguments must refuse them in `status` too, or `status` reports state
for a call that could never run.

But a question has no arguments to validate. Its whole body **interrogates state** — and in
`--dry-run` the plan has applied nothing, so any state the plan itself produces does not
exist yet. The question therefore fails for a reason that will not exist at apply time, and
an `err` halts the run.

### Observed

From the #490 dogfood, previewing a plan that brings a stack up and then waits for it:

```
docker.compose-up(build=true, dir=/opt/hosting/app) would.up
http.wait-for(timeout=60, url=https://app.example.test/healthz) err.timeout
(halted)
```

Everything after that line — the backup unit, its timer, log rotation, image pruning — went
unseen. To preview the rest of the plan, the health check had to be commented out.

It also costs the full timeout: sixty seconds of a preview that touches nothing, spent
waiting for something that by definition is not there.

Deploy-then-verify is the most ordinary plan shape there is, and it cannot be previewed.

## Decision

### 1. In check mode, only a *failing* question is softened

A question still runs in check, and the asymmetry is the decision:

| the question answers | in check | why |
| --- | --- | --- |
| yes (`ok.<tag>`) | resolved, unchanged | the state is already there, and the plan does not remove it, so the answer holds at apply time |
| no (`err.<tag>`) | `would` | a `no` is often exactly the state the plan is about to create, so it is not knowable yet |

ADR-0004's reasoning — a question reads, so its answer is reliable — is right about the
**current** state. Check mode is precisely the mode in which the plan has not yet changed
that state, and a `no` about something the plan creates is an answer with a shelf life of
one apply.

`would` carries **no tag**. `would.present` over an absent path reads as a claim, and the
point is that nothing is being claimed.

### 2. `would` is what makes a conditional honest

The mechanism this feeds already exists. `runIf` treats a `WOULD` condition as undetermined
in check (`internal/agent/agent.go`, "Never-lie: an unapplied action's result is
undetermined in check"): the branch is **previewed** and never claimed to run.

So `if dir.exists("/opt/legacy") { … }` still resolves on a host where the directory is
there — ADR-0004's case, untouched. A plan that creates a directory and then asks about it
previews the branch instead of silently taking the `else`.

### 3. What this does not change

`status` still asks. A question is a read, and reporting the world is what `status` is for
— that mode makes no claim about a plan that has not run.

`apply` is untouched: the question runs, and its `err` halts, as it must.

## Consequences

- **A preview reaches the end of a plan that verifies its own deployment.** That is the
  point, and it is the second word of the project thesis.
- **A `--dry-run` still waits.** `http.wait-for` runs and spends its timeout before
  answering `would`; only the halt is removed. Making a question skip its own body in check
  was considered and dropped — it would also skip the `yes` that ADR-0004 relies on. If the
  wait becomes a real cost, it is the def's timeout to bound, not the mode's.
- **A preview stops reporting that an external URL is down.** Some plans check a dependency
  they do not deploy, and that answer was occasionally useful. It is given up deliberately:
  in check mode nothing distinguishes "a service this plan will start" from "a service that
  exists elsewhere", and guessing wrong in the first case breaks every preview.
- **Conditionals read differently in `--dry-run`.** An `if` on a question was resolved, and
  now reports `undetermined` with the `then` branch previewed. Both branches of a plan whose
  condition depends on its own effects become visible, which is more information, not less.

## The rule this does not cover, and must be written down elsewhere

This ADR fixes the shape where the *whole def* is a question. It does not fix a normal def
whose `check` interrogates state the plan can produce — and that is a defect in the def, not
in the model.

`systemd.unit` shipped with exactly that: it validated its content with `systemd-analyze
verify`, which refuses a unit whose `ExecStart` is not yet on disk, so a plan delivering a
script and the unit calling it could not be previewed (#525). The fix was in the def — judge
what `verify` says about the *file* and ignore what it says about the *environment* — not in
the phase model.

So: **a `check` must not depend on state the plan itself produces.** That belongs in
`docs/language.md` beside the phase table, because the next def will break it the same way.

## Rejected alternatives

### Remove the halt in check mode entirely

A preview touches nothing, so nothing justifies stopping it — and an operator would see
every problem at once instead of one per run. Rejected on a concrete objection: a def whose
`check` depends on an effect an earlier step did not apply would then report an error that
is **an artefact of the dry-run**, not a defect of the plan. Trading "the preview stops too
early" for "the preview reports errors that are not real" is a bad trade: the first is
visible, the second misleads.

### Split `check` into argument validation and state interrogation

A new phase, or a marker, separating what is safe in every mode from what is not. It is the
honest general answer and it costs a change to the phase model every def is written
against — for four defs, where the whole body is the interrogation and no split is needed.
If a normal def ever genuinely needs both halves, this comes back.

### Leave it, and document that a health check breaks a preview

Rejected: "previewable, unless your plan verifies anything" is not previewable.
