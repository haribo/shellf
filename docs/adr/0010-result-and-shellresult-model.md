# ADR 0010 — Result and ShellResult: product vs sum, one model per value

## Status

Active

## Context

Two kinds of value were conflated, and testing "success" had three spellings
(`r.ok`, `r.exit == 0`, `== ok`). [ADR-0008](0008-outcome-matching.md) drew the
"do not conflate `Result` and `ShellResult`" line but kept `.ok` on
`ShellResult` and left the outcome half-struct, half-union. The real distinction
is algebraic:

- a `shell { }` yields raw process output — a **product**: `exit` **and**
  `stdout` **and** `stderr`, all three always present;
- an instruction yields an **outcome** — a **sum**: **exactly one** of
  `ok` / `err` / `would`, carrying a tag.

A product is read by its **fields**; a sum is read by **matching** its variant.
Giving both the same syntax would lie about the shape. See issue #108.

## Decision

### 1. Two kinds, two models

- **`ShellResult` = a product (struct).** Read by fields only: `r.exit`,
  `r.stdout`, `r.stderr`. There is **no `.ok`**.
- **`Result` = a sum (tagged outcome).** Read by **matching**: `x == ok`,
  `x == err`, `x == ok.installed`, `x == err.diskPb`. It exposes **no outcome
  fields** (`.ok` / `.err` / `.status` / `.exit`). It carries **one** orthogonal
  flag: `x.changed` (did apply run — a different question from *what* happened).

### 2. Universal truthy sugar: `if x` / `if !x`

`if x` = success, `if !x` = failure — on **both** kinds. A ShellResult's success
is `exit == 0`; a Result's success is the `ok` category. It is sugar over the
type's success test.

### 3. `==` is uniform on Results — no carve-out

`x == ok`, `x == err`, `x == ok.<tag>`, `x == err.<tag>`: the category is
required, the tag optional. The bare-category form is **not** forbidden.
`if x` and `x == ok` are the sugar and the explicit spelling of **one**
mechanism; both are allowed. **Redundant spellings of one mechanism are fine** —
user freedom, and they all read clearly.

### 4. What is forbidden is a rival *mechanism*, not a spelling

- `.ok` / `.err` / `.status` / `.exit` on a **Result** — field access competing
  with `==`; it re-structifies a sum, and once `.ok` is back, `.status` / `.exit`
  follow and the product/sum distinction collapses. Forbidden.
- `.ok` on a **ShellResult** — redundant with `if s` / `.exit == 0`; a product's
  success is `exit == 0`. Removed.
- `s == ok` on a **ShellResult** — a product has no `ok`/`err` variant to match
  (its "ok" is `exit == 0`). `if s` is the sugar, `.exit` the field; no `==` on
  shells.

Produce and consume then use the **same** notation, which is the symmetry that
matters:

```
return err.dbLocked(r)      # a def PRODUCES the outcome
if x == err.dbLocked { … }  # a plan CONSUMES it — identical notation
```

### 5. Error tests still require `?`

Unchanged from [ADR-0009](0009-error-handling.md): testing a Result's failure
(`if !x`, `x == err`, `x == err.<tag>`) requires the instruction be `?`-caught,
else halt-on-err makes the branch dead code. Shell failure tests (`if !s`,
`s.exit != 0`) do **not** — a shell inside a def does not halt the plan.

### Vocabulary (one table)

| test | spelling | shell | def |
|---|---|---|---|
| success | `if x` | ✔ (exit 0) | ✔ (ok) |
| failure | `if !x` | ✔ | ✔ (needs `?`) |
| specific code / output | `x.exit == 2`, `x.stdout == "…"` | ✔ | — |
| category (explicit) | `x == ok`, `x == err` | — | ✔ (`== err` needs `?`) |
| specific tag | `x == ok.installed`, `x == err.diskPb` | — | ✔ (`err.*` needs `?`) |
| did it act? | `x.changed` | — | ✔ |

## Rejected alternatives

- **Struct model for outcomes** (`d.err != nil`, `d.status == err`) — models a
  sum as a product: reintroduces the `ok`/`err` "doublon" (two slots, one
  filled) and breaks produce↔consume symmetry (write `return err.dbLocked`, read
  `.err`/`.status`).
- **Banning bare `== ok` / `== err`** (keep only `if x` / `if !x`) — an
  incomprehensible carve-out: `== ok` forbidden but `== ok.installed` allowed.
  `==` must be uniform; redundant spellings of one mechanism are acceptable.
- **Keeping `.ok` on ShellResult** — a third way to test shell success alongside
  `if s` and `.exit == 0`; a product's honest success test is `exit == 0`.
- **Universal `== ok` including shells** — a product has no `ok` variant to match.

## Consequences

- **Evaluator/parser**: `ShellResult` drops `.ok` (success inside defs = `if r`
  or `r.exit == 0`). `Result`: `if x` / `if !x` truthy; `==` uniform (category
  ± tag); forbid outcome field-access on a Result; keep `.changed`. Truthy on a
  `would` result still yields `undetermined` in check (never-lie, unchanged).
- **Stdlib**: migrate every def — `if r.ok` → `if r` (or `r.exit == 0`).
- **Amends** ADR-0004 (bare `if x` is sugar over the type's success) and ADR-0008
  (removes ShellResult `.ok`; `==` uniform; forbids outcome field-access on a
  Result). **Extends** ADR-0009 (`if !x` / `== err` are the failure tests; the
  `?` rule is intact).
- Implemented in a follow-up `type: feature` issue; this ADR is docs-only.
