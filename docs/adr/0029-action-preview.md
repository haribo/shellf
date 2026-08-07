# ADR 0029 — `preview`: informational preview for action-shaped defs

## Status

Active. Extends the def contract of [ADR-0013](0013-observe-state-contract.md) with
one optional, read-only phase. Does **not** touch the resource/action classification
or the never-lie rule — it works within them.

## Context

An action-shaped def (no `observe`) always applies; in `--check` it prints
`would.<tag>` and nothing more. For `docker.compose-up` that means the preview —
shellf's headline promise — cannot say whether a run will recreate containers or do
nothing. On a deploy plan, that is the one thing the operator wants to see.

The obvious fixes are already ruled out (ADR-0013 §6, and #263):

- **A config-hash or image-digest `observe`** is a *lying* observe: it does not see an
  image updated under a moving tag, so the engine would skip `apply` on a stack that
  `up -d` would in fact recreate. An observe that is right most of the time is worse
  than none — it turns a visible `would.up` into an invisible wrong skip.
- **Comparing the built image** requires building during `--check`, which must touch
  nothing.

What is missing is not an observe. It is a way to *describe* what the action would do,
clearly separate from a claim about convergence. `docker compose up --dry-run` reports
the per-service recreate/start decisions without mutating anything — exactly that kind
of description.

## Decision

### 1. An optional `preview` phase

A def may declare a `preview` phase. It is **read-only** and runs **only in `--check`**.
Its shells describe what `apply` would do; their combined output becomes the step's
**informational preview**. `preview` never returns a `state()` and never decides
convergence — it cannot change whether `apply` runs. It is generic: any def may add
one, and it is most useful on action-shaped defs.

```
def compose-up(dir: str, build: str = "false") as root {
    preview { shell { cd "$dir"; docker compose up -d --dry-run --remove-orphans } }
    apply   { … }   # unchanged
}
```

### 2. Rendered as a preview, never as a convergence claim

The output rides in a distinct `StepResult` field (not a `FieldDiff`, which means
"observed vs desired → will skip"). The report renders it as a clearly marked block
under the step's `would.<tag>`, e.g.:

```
compose-up(dir=/opt/app)   would.up
    preview ▸ Recreate app-web-1
    preview ▸ Recreate app-worker-1
```

The `would.<tag>` still says "this will run"; the `preview ▸` lines say "here is what
it would do". The two are typographically distinct, so a preview can never be misread
as "converged, will skip".

### 3. A failing preview is non-fatal

If the preview shell fails — Compose too old for `--dry-run`, the daemon unreachable,
the command absent — the check **degrades to a plain `would.<tag>`** with no preview.
A preview is a courtesy, never a gate: it must not fail the check or halt the run.

### 4. Scope: `--check` only; `status` unchanged

`preview` runs in Check mode only. `status` still reports an action-shaped def as
`action (no observable state)` — honest: it has no state. Previewing what an action
would do is a `--check` question, not a state report.

### 5. `docker.compose-up`

Gains a `preview` using `docker compose up --dry-run` (Compose ≥ 2.29, where
`--dry-run` reports create/recreate/start per service). Caveat, stated in the def and
its preview: `--dry-run` does **not** build, so with `build: "true"` the preview is
partial — it shows the orchestration, not the image rebuild.

## Rejected alternatives

- **A config-hash / image-digest `observe`.** A lying observe (ADR-0013 §6) — the
  rejected request; it would skip a real recreate.
- **Building during `--check`.** Violates the inert-check contract.
- **A compose-only special case in the engine.** A generic `preview` phase is more
  honest and costs no more; other action-shaped defs (a restart, a migration) can use
  it too.
- **Reusing a `FieldDiff` to carry the text.** A field diff means "observed vs desired"
  and reads as a convergence/skip claim — the exact confusion to avoid. A distinct
  field, distinctly rendered.
- **Running `preview` in `status`.** `status` is a state report; an action has no
  state. Keeping it out of status keeps that honest.

## Consequences

- `shellf run --check` on a compose stack shows the per-service recreate/start plan,
  clearly a preview and never a convergence claim; apply still always runs.
- The def grammar gains a `preview` phase; the engine runs it read-only in Check and
  attaches its output to a new `StepResult` preview field; the report renders it as a
  marked block. `apply`/`observe` and the never-lie rule are untouched.
- The preview is best-effort: absent or partial (old Compose, `build: "true"`) it
  degrades gracefully, never failing the check.
