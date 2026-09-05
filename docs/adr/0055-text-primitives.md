# ADR 0055 — Text primitives: `~text.matches` and `~text.replace`

## Status

Active. **Extends the closed set of [ADR-0036](0036-primitive-and-location-markers.md) §5**
with two primitives that read and rewrite a *value* rather than a file. ADR-0036 is not
reversed: the marker rules, the closed set, and "a primitive is marked because its contract
differs" all stand.

Uses the naming of [ADR-0032](0032-stdlib-naming.md), and the timing rule of
[ADR-0034](0034-control-host-primitives.md) §5 carried forward by
[ADR-0045](0045-parameter-types-are-checked-by-value.md).

## Context

The language can ask exactly one question of a value: *is it equal to that one?*
`Binary.Op` is `"=="` or `"!="` and nothing else (`internal/lang/ast.go:108-111`,
evaluated at `eval.go:637-643`). Any other question about an argument — does it hold a
`=`, a newline, does it look like a name — has no spelling.

Two measured consequences:

- **`file.replace` cannot refuse an argument it cannot honour.** It writes a `key=value`
  line, so a key holding a `=` cannot be represented: `file.replace(f, "a=b", "v")` finds
  no line starting with `a=b=`, appends its own, and leaves the file with two lines
  starting with `a=`. A reader of that `.env` sees the key `a` defined twice and generally
  keeps the last — so `a` silently changes value, and the file never converges. The def
  carries the missing guard as a comment (`internal/std/file/file.shellf:54-59`); its
  `check` only tests `key == ""` (`:61-63`). Tracked as #492, from #487.
- **`sudo.shellf:27` asks the question anyway, in shell**: `printf '%s' "$name" |
  grep -qE '^[A-Za-z0-9_-]+$'`. That is a round trip to the *target* to inspect a string
  the control host passed in, paid in every mode, `--dry-run` included.

A shell in `check` has a second cost, structural rather than slow: the unit fakes answer
every non-apply shell identically (`internal/std/std_test.go:16-38`), so a def whose
`check` shells cannot be unit-tested at all.

### Doing it in `sed` would import a portability problem

The obvious repair — reach for `sed -E` on the target — makes the answer depend on the
target's tooling. GNU, BusyBox and BSD disagree on the dialect, and `\d` works on none of
them in POSIX. A plan validated against Debian would behave differently on Alpine, which
is the class of failure this project keeps finding.

### The agent already runs Go on the target

The marker `~` says "primitive", not "control host" — where one runs depends on the
primitive (ADR-0036 §1, §5), and the code confirms it:

| primitive | runs | reference |
|---|---|---|
| `~file.write` | on the **target**, in Go, idempotent by sha256 | `eval.go:791-793`, `:808-810` |
| `~file.read("/etc/x")` | on the target, base64 so binaries survive | `eval.go:847-849`, `:968-986` |
| `~file.read(%"x")` | control host, through the channel and its allow-list | `eval.go:838-845` |
| `~file.render` | control host only — host variables and secrets never travel | `eval.go:856-878` |

So a regex primitive needs no new machinery and no `sed`: the engine is the binary already
sitting on the target.

## Decision

### 1. Two primitives, over values

```
~text.matches(s, pattern)        → true / false
~text.replace(s, pattern, repl)  → the rewritten text
```

They are pure: they read no file, write no file, and reach no host. They join the closed
set in `internal/lang/def_parser.go` (`ControlPrimitives`, ADR-0036 §5).

`text` and not `str`, which is a **type** name since ADR-0045 §1 — a word meaning two
things in one grammar is what [ADR-0053](0053-one-name-one-source.md) exists to prevent. Not `re` or `regex` either:
those name the tool, and would read wrong the day a non-regex text operation joins them.
One dot, per ADR-0032 §2. `matches` is plural because it is read as a predicate:
`if ~text.matches(key, "=")`.

### 2. The dialect is Go's RE2, named and closed

One engine, compiled into the agent, therefore identical on Debian, Alpine and BSD. RE2
has no backtracking and runs in time linear in the input, so a pattern cannot be the
reason a deployment hangs.

Stating the dialect *is* the decision: `\d` is not RE2 (`[0-9]` and `\p{Nd}` are), and a
pattern is refused, named, rather than silently reinterpreted. A pattern that does not
compile is an error naming the pattern and the position in it.

### 3. The replacement is literal — this is #487 in another syntax

`~text.replace` substitutes the replacement **verbatim**. `$1`, `${name}` and `&` are
ordinary characters, not references to captured groups.

This is not a limitation postponed, it is the defect that produced this ADR's context.
`file.replace` used to build a `sed` expression out of its own arguments, so `&` meant
"the whole match" and `URL=https://a&b` landed as `URL=https://aURL=oldb` (#487). Giving
the replacement its own expansion syntax reintroduces exactly that: a value the caller
passed, parsed as syntax. Group references, if a measured need appears, are a separate
decision with a separate spelling.

Every occurrence is replaced. A "first occurrence only" variant is not offered: which one
is *first* depends on facts the caller cannot see.

### 4. Convergence is verified on the real content, not derived from the pattern

A def that rewrites a file with `~text.replace` establishes idempotence by comparing, not
by reasoning about the arguments: it applies the replacement, and compares the result with
the current content. Equal → nothing to do. Different → that is exactly what would change,
and `--dry-run` can show it.

Some pairs never settle. `("a", "aa")` rewrites its own output for ever, and a def using it
would report a change on every run — the failure `dir.owner` had for months. The check is
a fixed point on the **actual result**: apply once, apply again to that result, and refuse
with an error naming the pattern if the second application changes anything.

Deriving it from the pair alone was rejected: it is both too strict and not exact.
`("PORT=[0-9]+", "PORT=8080")` has a pattern matching its own replacement and is perfectly
convergent, while `("ab", "ba")` looks stable and is not on `aab`.

### 5. The plan grammar does not change

A `~` primitive is refused in a plan today. Verified by running one:

```
if ~file.read("/etc/hostname") == "x" { … }
→ 2:8: expected condition, got "~"
```

A plan's condition is an instruction, a `shell`, or a test on a captured result
(`internal/lang/parser.go:615-640`, refused at `:631`) — the plan calls, the def computes.
Opening it to arbitrary expressions is a language decision far larger than these two
primitives, and it is not taken here.

What a plan actually needs is served without touching the grammar, because a def **is** a
form a condition accepts: a question over a file's content is a def calling
`~text.matches`, and a rewrite is a def calling `~text.replace` — the same way `file.exists`
already serves `if file.exists(…)` (`internal/std/file/file.shellf:212`).

### 6. Where the predicate is evaluated

In the def, where the def already runs: on the target, inside the agent, with no shell and
no round trip. `--dry-run` evaluates `check` phases, so a def refusing its arguments
refuses them before anything is applied.

This does not deliver what #492 asked for literally — a refusal at plan-read time, like a
type error. It cannot: a constraint expressed inside a `check` is only knowable by
evaluating the def. Making it knowable earlier means putting the constraint in the
**signature**, beside the type (ADR-0045 §3), which is a distinct decision on a distinct
grammar. #492 stays open for it; these primitives make the guard *expressible*, which it
was not.

## Rejected alternatives

- **An operator in the language** (`key contains "="`, `key matches "^[a-z]+$"`).
  It puts a regex engine in the grammar rather than in the closed set of primitives, where
  every other engine capability already lives, and it grows the expression language for
  every new question. The primitive set is the project's existing answer to "the language
  needs to do something it cannot spell".
- **`sed`/`grep` on the target.** Portability, above — and it keeps the unit fakes blind.
- **A control-host primitive that rewrites a target file.** The file is on the target: this
  would fetch it, transform it here, and send it back — two transfers per replacement,
  against the resident agent's whole point, and it would need an answer to "which files may
  a plan overwrite on my machine" that ADR-0036 §4 deliberately does not have.
- **Group references in the replacement.** §3.
- **Static analysis of the (pattern, replacement) pair.** §4.

## Consequences

- `file.replace` can refuse a key holding the separator, and `sudo.shellf` can drop the
  shell from its `check` — which also puts both back within reach of the unit fakes.
- A def rewriting a file becomes ordinary shellf: `~file.read` → `~text.replace` →
  `~file.write`, and the sha256 guard of `~file.write` (ADR-0036 §5) supplies the
  `already` verdict with no extra logic.
- The stdlib's `file.*` family can be reconsidered on top of this — a textual replacement
  under a name that means it, with a key/value def composed over it. That reshapes existing
  instructions and is a decision of its own, not a consequence of this one.
- Two names are now taken in the primitive namespace, and the set stays closed: a third
  text operation is an amendment to this record, not an addition someone makes in passing.
