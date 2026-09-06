# ADR 0056 — A pure `check` decides while the plan is read

## Status

Active. Applies the timing rule of [ADR-0034](0034-control-host-primitives.md) §5 — a
plan's error is reported when the plan is read, not mid-deploy — to a def's own argument
guards, which [ADR-0045](0045-parameter-types-are-checked-by-value.md) established for
types and left for shapes.

Depends on [ADR-0055](0055-text-primitives.md): before `~text.matches`, an argument guard
had to be a `shell`, and nothing about it could be decided anywhere but on the target.

## Context

A def's `check` refuses arguments it cannot honour. Since ADR-0055 it can do so without a
shell — `file.replace` refuses a key holding a `=`, `sudo.write` refuses a name that cannot
be a filename. But a `check` is a phase, and a phase runs where the def runs: **on the
target**. So the refusal needs a reachable machine to happen at all.

Measured on the built binary, two hosts, both unreachable, `--dry-run`:

| the plan says | what the operator gets | when |
|---|---|---|
| `service.ensure("nginx", "yes", true)` — a wrong **type** | `3:1: running expects a boolean, got "yes"` | **5 ms**, no network |
| `file.replace("/etc/app.env", "a=b", "v")` — a wrong **shape** | `a: unreachable` / `b: unreachable`, and not one word about `a=b` | **10 020 ms** |

The second plan is as broken as the first and shellf cannot say so. A plan cannot be
reviewed away from the fleet; a typo in an argument is learned by deploying. On fifty
hosts, it is learned fifty times, after fifty connections.

### What is already true

- The guard exists and is already written, in the def, once (ADR-0055).
- It is already **pure**: `==` and `~text.matches`, no shell, no host.
- The load path already runs a whole-plan check that needs both the plan and the resolved
  defs — `CheckCycles`, called from `cmd/shellf/main.go:354`, with its reason recorded
  there: the graph spans user defs and the stdlib, which no single package sees.

So the thing to decide is not a new way to express a guard. It is *when the guard that
exists is allowed to answer*.

## Decision

### 1. A `check` that cannot touch anything is evaluated while the plan is read

When every statement of a def's `check` is pure, the check is evaluated on the control
host, against the arguments the plan wrote, before any host is contacted. An `err` verdict
refuses the plan, naming the instruction, its position, and the verdict — the shape a type
error already has.

Naming the position is part of the decision, not a detail of the message: a plan calls
`file.replace` ten times, and a refusal that names only the instruction sends the reader
looking. A step therefore carries the line it was written on, the way a `shell` has carried
one since #470. It stays on the control host and never reaches the wire — the agent has the
source of nothing.

No new syntax. The def below is today's `file.replace`, unchanged:

```
def replace(path: str, key: str, value: str) {
    check {
        if key == "" { return err.keyMustNotBeEmpty }
        if ~text.matches(key, "=") { return err.keyMustNotContainEquals }
    }
    …
}
```

```
plans/p.shellf: 3:1: file.replace: err.keyMustNotContainEquals
```

An annotation in the signature (`key: str matching "^[^=]+$"`) was the shape the issue
proposed, and is rejected in §6: it is a second place to write a guard that is already
written, and a grammar to design, for the same answer.

### 2. Pure is narrower than inert, and is its own test

`inertExpr` (`internal/lang/inert.go`) answers a different question — "can this act in
check mode" — and every primitive passes it, including `~file.read`, which reaches a host.
This decision needs "reaches nothing at all":

- no `shell { }`, marked or not;
- no call to another def — resolving it needs the def table, and a callee with a shell
  three levels down would make the answer wrong. Refusing to answer is the same trade
  ADR-0041 already took for `inertApply`;
- of the primitives, only `~text.matches` and `~text.replace`, which are pure by
  construction (ADR-0055 §1).

A `check` holding anything else is not evaluated here. It runs on the target, exactly as
it does today: this decision adds an earlier answer, it never removes the later one.

### 3. Only an `err` decides

A pure check reaching `err` refuses the plan. A pure check returning `ok`, or returning
nothing, concludes nothing and the run proceeds unchanged.

The asymmetry is deliberate. A def whose whole body is a `check` is a question, and
[ADR-0051](0051-a-failing-question-is-would-in-check.md) decided a question is not resolved in check mode — its `ok` describes state, not
the arguments. Refusing on `err` costs a question nothing: a question about state cannot be
pure, since state lives on the target.

### 4. It answers where the value exists, and nowhere else

An argument reaches a step in one of three conditions, and only the first is known while
the plan is read (`internal/proto/proto.go:172-177`):

| the plan wrote | held as | known |
|---|---|---|
| `"a=b"`, or `"${global}"` | `Args` | while the plan is read |
| `"${inventory.flag}"` | `Templates` | at per-host expansion (ADR-0052) |
| a bare `flag` | `Refs` | at per-host resolution (ADR-0003 §5) |

A step carrying a `Ref` or a `Template` for a parameter its check reads is **not** decided
early: the value is not there yet, and checking the text `${inventory.flag}` against a
pattern would refuse a plan that is correct. That is not hypothetical — it is #582, where
the type check reads exactly that text and refuses a legitimate plan.

Re-checking at per-host expansion is deliberately not decided here. It is #582's subject:
the type check has to move there first, and the two must land in the same place rather than
grow two conventions.

### 5. It lives where `CheckCycles` lives

Same pass, same reason, same file (`cmd/shellf/main.go`): it needs the plan and the
resolved defs — the stdlib included — and no package under `internal/lang` can see both.
`lang` exposes the walk; the caller supplies the resolver, as it already does for cycles.

The evaluator keeps the check on the target. An earlier answer is an addition, never a
replacement: a def reached by a path this pass cannot enumerate — through a delegation, or
a def whose arguments were per-host — must still be refused, late rather than never.

## Rejected alternatives

- **An annotation beside the type** (`key: str matching "…"`). A grammar to design, a
  vocabulary to document, and a guard written twice — or moved out of the `check`, where a
  reader already looks for it. The gain is a signature that documents itself; the cost is a
  second mechanism answering a question the first already answers.
- **Evaluating the whole `check`, shells included, on the control host.** Shell on the
  operator's machine is the one thing the primitive set is closed to prevent (ADR-0036 §2).
- **Evaluating a check's pure *prefix* and stopping at the first impure statement.** It
  would cover `sudo.write`, whose name guard precedes a `visudo` shell. Rejected: what a
  reader can predict must not depend on statement order inside a phase, and a guard that
  answers early only when it happens to be written first is a rule nobody can hold. A def
  that wants the early answer can put its argument guards in a pure `check` — which is a
  refactor, not a grammar.
- **Deciding on `ok` as well as `err`.** §3.

## Consequences

- `file.replace` refuses `a=b` while the plan is read, with no network, and `sudo.write`
  would too once its name guard stops sharing a phase with `visudo`.
- A plan can be reviewed away from the fleet, which is what "previewable" has meant for
  everything except arguments.
- A def author gains a reason to keep argument guards free of shell, and ADR-0055's
  primitives are what makes that possible.
- The rule is one sentence — *a check that touches nothing answers when the plan is read* —
  and it holds for user defs and imported defs without a word of extra vocabulary.
