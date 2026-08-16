# ADR 0040 — A step must tolerate being re-run; `unsafe shell` marks what no def covers

## Status

Active. **Extends [ADR-0037](0037-explicit-verdict.md)**, which made a *delegation*
observable and left a *sequence* unanswered. Nothing in ADR-0037 is reversed.

## Context

`docs/design.md` §02 lists the non-atomic composite as an open hole. Reproduced against
the e2e container (#377), with a def whose `apply` fetches and then builds:

| Run | Result |
|---|---|
| 1 | `err.build` — the fetch already ran and left `src/` behind |
| 2 | `err.fetch` — *`mkdir: cannot create directory '…/src': File exists`* |
| 3, after fixing the cause on the target | `err.fetch` again |

Three failures, and the third is the one that matters: once the real cause is fixed, the
target still cannot converge. The error also names the wrong step from run 2 on. Only a
manual `rm -rf` unblocks it — the "go and fix it by hand" shellf exists to remove.

The same def written with `dir.ensure` instead of `mkdir` was measured immediately after:
`err.build` on all three runs, converging as soon as the cause was fixed. **The step being
re-runnable is the whole difference.** `dir.ensure` is `mkdir -p`; `mkdir` is not.

Measured over the 41 defs in `internal/std/` and `examples/defs/`: two have a multi-effect
`apply` (`sshd.config`, `sudo.write`), and both are built entirely from defs, so both
already satisfy the rule below. The invariant holds today by the authors' care, and
nothing enforces it.

## Decision

### 1. A step in an `apply` must tolerate being re-run

shellf does not roll back, and will not pretend to. It cannot undo an arbitrary
`shell { }`, and a cleanup that is itself half-successful leaves the machine in a state
nobody has described — worse than the partial state it was meant to repair.

What shellf guarantees instead is weaker and achievable: **a failed run leaves the host in
a state a later run can move forward from.** That holds exactly when every step tolerates
running again on a host where it has already run. `mkdir -p`, not `mkdir`.

Atomicity is not claimed. If step 2 fails, step 1's effect remains — and that is visible,
re-runnable, and named by the error that actually occurred.

### 2. Shell that a def already does is refused, not discouraged

A def is re-runnable by construction: it observes, it reports, it converges. The shortest
path to §1 is therefore to use the def, and the cheapest moment to say so is before the
run.

A `shell { }` block whose command has an exact def equivalent is a **parse-time error**,
in every mode. The message names the replacement:

```
mkdir here — dir.ensure(path) is idempotent and previewable.
Write `unsafe shell { … }` to keep the shell.
```

Refusing rather than warning is deliberate. A new operator does not yet trust the tool and
has no way to tell a real warning from noise; a warning that is scrolled past teaches
nothing, and the failure it predicts arrives on the second run, which is the least-tested
path there is. Blocked, the operator reads the message, and either takes the def or says
why not. This is the shape of Rust's `unsafe`: refused by default, allowed with a word.

### 3. `unsafe shell { }`, and `unsafe` means "no def covers this"

```
unsafe shell {
    mkdir /var/lock/deploy || exit 1     # an atomic lock: it must fail if it exists
}
```

`unsafe` does **not** mean dangerous. It means shellf cannot vouch for what this does —
the same claim Rust's `unsafe` makes, and for the same reason. The lock above is
irreproachable shell that no def replaces, and it is marked, correctly.

Stating this is not decoration. A marker read as an accusation gets argued with; a marker
read as "you are on your own here" gets used. It also keeps the marker's real value
intact: `grep -r 'unsafe shell'` is the audit surface of a project — every place where
shellf's guarantees stop, in one list, including inside an imported module.

`unsafe` composes with the interpreter override: `unsafe shell(sh) { … }`.

### 4. `unsafe`, not `!shell`

`!shell` was the first proposal and is **taken**: `if !shell { g } { shell { cmd } }` is
the documented replacement for `unless` (`docs/language.md`,
[ADR-0004](0004-control-flow-preview.md)) and is exercised by
`internal/agent/if_test.go`. The same three characters would mean "negate this block"
there and "force this block" here.

`unsafe` is also a better `grep` target than a sigil, and it carries its meaning without a
legend.

### 5. The detector: one rule, one written justification, exact equivalence only

A rule is admissible only when the def does **exactly** what the command does. Two
candidates were examined for this ADR and both were found to be inexact on first reading;
that rate is the reason for the constraint, not a principle stated in advance.

It ships with **two** rules:

| Command | Replacement | Why it is exact |
|---|---|---|
| `mkdir`, `mkdir -p` | `dir.ensure(path)` | `dir.ensure` *is* `mkdir -p`, with an `observe` on presence. `mkdir` without `-p` has no def — as an atomic lock it must fail when the directory exists — so it goes to `unsafe shell`, which is the right answer and not a false positive. |
| `cp` | `file.copy(src, dst)` for a file, `dir.copy(%"src", dst)` for a tree | Both deliver bytes and report `copied`/`already`. Neither carries mode or ownership: the message must say so, and name `file.mode` / `dir.owner`. `cp -p` and `cp -a` are therefore **not** equivalent, and the message must not claim they are. |

Every further rule is a separate decision, argued in the code where it is declared:
what the command does, what the def does, and why the two are the same. `systemctl`,
`curl`, `rm` and `useradd` are candidates and are **not** shipped — `rm` alone has no exact
counterpart, since `file.delete` takes a path and a shell `rm` takes a glob.

### 6. Scope: user code, including imported modules

The detector runs on plans and user defs. The stdlib is exempt: it is the layer that
reaches the system, and `service.ensure` is *implemented* with `systemctl` — a rule
forbidding that would forbid the replacement it recommends.

A remote module ([ADR-0016](0016-remote-modules.md)) is user code and is checked. Its
author marks their own `unsafe shell`, and the consumer greps for it before importing —
which is the audit surface of §3 doing the work it exists for.

## Rejected alternatives

- **Roll back on partial failure** (an `undo` / `on-fail` block). It must know how far it
  got, and a half-successful cleanup leaves an undescribed state. shellf cannot undo an
  arbitrary command, and a rollback that is right most of the time is worse than none.
- **An observation per sub-step**, making an apply resumable. Correct, and it roughly
  doubles the size of every composite def for a case that §1 already covers at no cost.
- **Refuse a multi-effect `apply` outright.** Strongest guarantee, and it rejects
  `sshd.config` and `sudo.write` as written — two defs whose steps are already
  re-runnable, i.e. exactly the shape §1 wants to allow.
- **Warn instead of refusing.** The failure a warning predicts appears on the second run
  after a first one failed. A warning that is not read is a warning that did not happen.
- **`!shell`.** Already means the opposite (§4).
- **Detect by parsing the shell properly** rather than matching commands. `$CMD`, `eval`,
  `install -d`, `tar x` and `find -exec cp` defeat any practical analysis. The detector is
  a heuristic and is documented as one; §5's constraint is what keeps a heuristic honest.

## Consequences

- The grammar gains `unsafe` before `shell`. It is a new keyword in statement position,
  where a bare identifier is currently parsed as an instruction name — so a plan using
  `unsafe` as an instruction name would break, and none does.
- Existing plans that call `mkdir` or `cp` inside a `shell { }` stop parsing. That is the
  point, and the message names the replacement; the changelog must carry it as
  **BREAKING**.
- The detector's precision becomes a maintained property: each rule carries its
  justification, and a rule found inexact is removed rather than qualified.
- `docs/design.md` §02 loses the 🔴 composite entry and links here.
