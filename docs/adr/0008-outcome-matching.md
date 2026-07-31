# ADR 0008 — Outcome matching: `s == cat.tag`, one mechanism

## Status

Active

## Context

An instruction returns a **Result**: a `category.tag` outcome (`ok.installed`,
`err.dbLocked`, …) plus an orthogonal `changed` flag. Callers — plans, and
captured results from ADR-0004 (`s1 = …`) — need to test it: *did it succeed?
is it this specific error?*

ADR-0003/0004 exposed `s1.ok` / `s1.changed` as boolean fields. Testing a
specific **tag** has no clean form: `s.err == dbLocked` is ugly (a bareword tag
with no category, and `.err` is ambiguous — a bool? a tag?). Left unspecified,
we would drift into two mechanisms — `.ok`/`.err` for the category and something
else for the tag — the exact `when`/`if` duplication ADR-0006 just removed.

## Decision

### 1. Test an outcome by equality to an outcome pattern

```
if s == ok            { … }   # succeeded (any ok.*)
if s == err           { … }   # failed (any err.*)
if s == ok.installed  { … }   # succeeded with this tag
if s == err.dbLocked  { … }   # failed with this tag
```

The right-hand side is an **outcome literal**: a category, optionally `.tag`.

### 2. The tag is optional; absent = category wildcard

`s == err` matches any `err.*`. `s == err.dbLocked` matches only that tag.
`Result == pattern` ⟺ `category matches AND (pattern has no tag OR tags match)`.
`!=` is the negation. One rule answers both *"is it an error?"* and *"which
error?"*.

### 3. This is THE mechanism for category and tag

`.ok` / `.err` on Results are **deprecated** — use `== ok` / `== err`. One way to
test the category, one way to test the tag, same operator. No second form.

### 4. `changed` stays; ShellResult fields stay

- `s.changed` remains — `changed` is **orthogonal** to the category (an `ok` can
  be *changed* or a *skip*); it is not an outcome pattern.
- Inside defs, `r.exit` / `r.ok` / `r.stdout` on a `shell { }` **ShellResult**
  are unchanged. A ShellResult is not an outcome — no `==` pattern applies to it.
  (`r.ok` there means exit == 0, a different type from a Result's `ok` category.)

## Rejected alternatives

- **Field form for tags** (`s.err == dbLocked`) — bareword tag without a
  category; `.err` ambiguous (bool vs tag). Reads worse than `== err.dbLocked`.
- **Allow both `.ok`/`.err` and `== ok`/`== err`** — two ways to test the
  category. The precise duplication ADR-0006 removed for conditionals; re-adding
  it here would contradict the same principle.

## Consequences

- **Grammar**: the RHS of `==`/`!=` may be an outcome literal (`cat` or
  `cat.tag`). Reuses the existing outcome syntax.
- **Evaluator**: add `Result` vs outcome-pattern comparison (category match +
  optional tag match).
- **Amends ADR-0004**: `s1.ok` → `s1 == ok`; `s1.changed` kept. Update its
  examples. ADR-0004 stays Active (the capture/if model is unchanged; only the
  category test spelling moves).
- Implemented in a follow-up `type: feature` issue (this ADR is docs-only).
