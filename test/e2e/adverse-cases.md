# Adverse-state plans (#489)

The plans themselves are `plans/adverse-<def>.shellf`.

One plan per case. Each one puts the target in a state that is **wrong on purpose**,
calls the def, and asserts the **machine** — never the verdict.

## Why this exists next to `coverage.shellf`

`coverage.shellf` asks one question: *"run it twice, does the second run say `already`?"*
That is idempotence, and it proves it well. It does not prove correctness: a def that is
wrong in a **stable** way passes it. #486 is the proof — `apt.install` observed a package
in state `rc` as installed, both runs reported `already`, the harness stayed green, and the
package was not on the machine. Re-running finds nothing, because the wrong answer
converges.

The other half is the input. `coverage.shellf` runs on a fresh container, from an empty
starting state, with benign arguments — `file.replace(…, "KEY", "value")` on an empty file,
which is how the `&` corruption of #487 shipped.

|                    | `coverage.shellf`          | `adverse/`                       |
| ------------------ | -------------------------- | -------------------------------- |
| starting state     | fresh                      | pre-broken on purpose            |
| asserts            | the verdict                | the machine                      |
| catches            | non-idempotent defs        | defs that are confidently wrong  |

## The rules a case follows

- **One file per case**, named after the def it exercises (`plans/adverse-file.mode.shellf`).
  Flat in `plans/`, not in a sub-directory: a plan lives beside `defs/`, `assets/` and
  `inventories/`, and `plans/adverse/` is not a project (ADR-0038). `run.sh`
  runs every file in this directory and **collects** the failures instead of stopping at
  the first one — a plan halts on its first error by design, so putting every case in one
  file means the first defect hides all the others.
- **Run once.** These plans are not expected to converge: each rebuilds its hostile state,
  so a second run legitimately acts again. Convergence is `coverage.shellf`'s question.
- **The assertion is a `shell` that exits non-zero** when the machine is not in the desired
  state. A verdict is never the evidence — the whole point is that a def can report
  `ok.converged` over a machine that is wrong (#495).
- **`unsafe shell` for the setups** is the right hatch, not a workaround: these blocks
  exist to produce a state no def would ever produce (ADR-0040 §3).
- **Own your paths, with no shared parent.** A case works under `/tmp/adv-<case>`, not
  `/tmp/adv/<case>`. Measured: the first version shared `/tmp/adv`, the alphabetically
  first case created it under `as root`, and every later case failed on `mkdir: Permission
  denied`. A shared parent is a dependency between cases, which is the thing one file per
  case exists to remove.

## Three hostile things, not one

A case makes the **starting state**, the **argument**, or the **shape of the state** wrong —
and they find different defects.

| | wrong starting state | wrong argument | right-shaped but wrong |
|---|---|---|---|
| asks | "the machine is not what you assume" | "the caller passed something you did not expect" | "the state looks converged and is not" |
| finds | a def that misreads state, or half-converges | a path or a value parsed as syntax | an `observe` asking less than its `apply` guarantees |
| the defect behind it | #486, #480 | #487 — `URL=https://a&b` written as `URL=https://aURL=oldb` | #594 — four defs reporting `already` over a machine they would have changed |

The third is the one `coverage.shellf` can never produce, because it only ever builds state
from an empty target. Its cases are built by hand: a `.env` holding the wanted line **and**
a stale duplicate, an archive's destination emptied with its sentinel left behind, a
database that exists under the wrong owner, two logins where one is a regex match of the
other, an archive member emptied in place. Each was verified to fail before the fix and pass after — a case of this kind that
was never seen red proves nothing at all, since a weak observe passes it by construction.

An argument case passes a path holding a space, a single quote and a `&`, or a name at a
boundary (empty, very long, starting with a dash). It asserts the machine like any other
case: the directory that exists is the one that was asked for, *whole*, and no sibling was
created by an argument that split.

Nothing exotic is required to find these — "Application Support" and "Bob's data" are
ordinary names, which is the point.

## Coverage, and the gate that is not here yet

8 cases against 38 defs today. A CI gate requiring an adverse case per def would be 30
named exemptions, which is not a gate — it is a file nobody re-reads, and it turns the
exemption from a signal into the norm. `def-coverage.sh` works because it is at 38/38.

The gate lands when this directory covers the defs that declare an `observe` (~20) — those
are the ones for which "hostile starting state" means anything; an action-shaped def
(ADR-0029) has no state to get wrong. Until then the protection is #489 staying open.
