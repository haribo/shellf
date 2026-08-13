# ADR 0037 — A def delegates outside its phases, and an `apply` names its verdict

## Status

Active. **Supersedes [ADR-0007](0007-no-return-outside-action.md)**, whose §1–3 it keeps
— no floating `return`, the nominal outcome is the last statement of `apply`, `would` is
derived from it without running `apply` — and whose §4, the implicit `ok`, it reverses.

## Context

### The implicit `ok` is a default nobody chose

ADR-0007 §4 let an `apply` end without a `return`: the def then yields an `ok` with no
tag, on the stated ground that "instructions are not forced to name their success".

Nothing uses it. All **31** `apply` blocks across `internal/std/**/*.shellf` end with an
explicit `return`. What the escape hatch actually buys is that a *missing* `return` and a
deliberate "succeeded, nothing to declare" produce the same report — an omission reads as
a success.

### A def cannot wrap another without lying in `--dry-run`

`apply` is never evaluated in check mode (`internal/lang/eval.go`: pass 1 runs the
decision phases, then check mode returns `would` without reaching pass 2). That is the
design and it is right — nothing effectful may run.

The consequence was not: **a def called from inside an `apply` never has its own `check`
and `observe` run in `--dry-run`.** The wrapper previews as if the callee had no opinion.

`sudo.write` shows the damage. It has no `observe` and calls `file.write` and `file.mode`
from its `apply`; on a host where the drop-in is already correct, `--dry-run` announces
`would.written`. It claims it would rewrite `/etc/sudoers.d/<name>` and it would not
touch it. A false preview, in a tool whose thesis is that a run is readable before it
happens.

Making `file.template` a def ([ADR-0036](0036-primitive-and-location-markers.md) §7) hit
the same wall from the other side: it had to grow its own `observe`, re-deciding the
idempotence `file.write` already knows, because putting the call in `apply` would have
lost that decision in every dry-run.

The two are one question: **where does a def's verdict come from — and is it stated?**

## Decision

### 1. An `apply` ends with a `return`

The last statement of an `apply` phase must be a `return`. Anything else is a parse error
naming the fix. The implicit `ok` of ADR-0007 §4 is gone.

A def with no `apply` is unaffected: a question declares only `check`
([ADR-0035](0035-phases-and-modes.md)), and `observe` returns a `state(…)` record, not a
verdict.

### 2. A def delegates by calling outside any phase

A def body may hold a call at **body level**, outside every phase:

```
def template(src: str, dst: str) {
    file.write(dst, ~file.render(~file.read(src)))
}
```

This def **is** `file.write` with rebound arguments. The engine evaluates the callee in
the caller's mode, with all of the callee's phases: its `check` refuses in `--dry-run`,
its `observe` decides convergence, its `preview` describes, its `apply` acts. The
caller's verdict is the callee's — `ok.already` on a converged host, `ok.written`
otherwise, and `would.written` in a dry-run that would act.

This is the shape `apply` cannot have. `apply` means "the effectful pass"; a delegation
has no pass of its own to run.

**Exactly one call.** A second body-level call is a parse error pointing at `apply`. Two
calls are not a delegation — a def cannot *be* two defs — and they have no single verdict:

```
def write(name: str, content: str) as root {
    file.write("/etc/sudoers.d/${name}", "${content}\n")   # says already
    file.mode("/etc/sudoers.d/${name}", "440")             # says changed
}
```

There is no defensible answer to what that def reports. A sequence of effects is what
`apply` is for, and that is where it stays.

A def may still declare its own `check` alongside its delegation — argument validation
belongs to the wrapper, and `check` runs in every mode (ADR-0035), so it keeps working.

### 3. A delegation's arguments are read-only

An argument expression in a delegation body may use parameters and the primitives
(`~file.read`, `~file.render`, `~dir.list`) — never a `shell` block.

The reason is the whole point of §2: these arguments are evaluated in **every** mode,
including `--dry-run`, because the callee's `observe` needs the value to decide whether
it is already in sync. A `shell` there would run for real during a dry-run. Refused at
parse time, naming the rule.

### 4. `changed` propagates from the callee

A delegating def is `changed` when its callee changed something — which `ev.acted`
already does ([ADR-0030](0030-def-composition.md) §3). Written down because it was
observed behaviour, and observed behaviour changes without anyone deciding to.

## Rejected alternatives

- **Keep the implicit `ok` (ADR-0007 §4).** Measured cost of removing it: zero stdlib
  defs. Cost of keeping it: an omission that reports as a success. There is no external
  ecosystem yet, so the migration price will never be lower than today.
- **`return <def call>` from inside `apply`.** The first shape considered, and it looks
  like it solves the wrapping problem. It does not: `apply` still does not run in
  `--dry-run`, so the callee's `check` and `observe` are still skipped and the preview is
  still wrong. It fixes the label and leaves the lie — worse than the disease, because
  the label then looks trustworthy.
- **Warn when a def call appears in an `apply` during `--dry-run`.** A warning says "this
  preview may be wrong" without saying what it would be. That is a disclaimer, not a
  preview. It would also fire across most of the stdlib, which is how a warning teaches
  people to ignore warnings.
- **Accept `return shell { … }`.** The engine would have to derive a tag from an exit
  code. It cannot: `0` says the command succeeded, never what it accomplished.

## Consequences

- **Parser** (`internal/lang/def_parser.go`): refuse an `apply` whose last statement is
  not a `return`; accept a body-level call and refuse `shell` inside its arguments.
- **Evaluator** (`internal/lang/eval.go`): a body-level call evaluates the callee in the
  caller's mode and yields its `engine.Result`, in all three modes.
- **Stdlib**: `file.template` sheds the `observe` it grew in #334 and becomes the
  delegation above. No other migration — every `apply` already returns.
- A def whose effect is several calls (`sudo.write` writes then chmods) keeps its
  `apply`, and keeps the coarse `--dry-run` preview that comes with it: its callees'
  `observe` phases do not run, so it announces `would.<tag>` even on a converged host.
  That false `would` is a defect in its own right, exposed by this record rather than
  created by it, and is fixed separately — by giving such a def its own `observe`, or by
  whatever §2 cannot reach.
- ADR-0007 keeps its body and is marked superseded by this record.
