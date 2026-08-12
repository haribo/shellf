# ADR 0033 — A subdirectory is a sub-package; a dot never appears in a def name

## Status

Active. Extends [ADR-0014](0014-user-defs-directory-package.md), whose principle —
*the directory is the package* — it keeps and generalises. It relaxes that ADR's
flatness by exactly one level, for the reason recorded below. Required by
[ADR-0032](0032-stdlib-naming.md), which gives every stdlib instruction a package.

## Context

ADR-0032 renames every stdlib instruction to `<package>.<action>`. That silently
removes a capability ADR-0014 §5 grants explicitly: overriding a stdlib def.

`override def dir-ensure(...)` worked. Its successor would be
`override def dir.ensure(...)`, which the grammar rejects:

    expected (, got "."

Two existing tests fail on it. They do not break because of the rename; they break
because the feature ceases to exist the moment every stdlib name carries a dot.

Two ways out were considered. Allowing a dot inside a def name adds an exception to
the grammar and makes the dot mean two things — package membership when the engine
resolves a call, word soup when a user declares one. The other keeps the dot meaning
exactly one thing and derives the package from where the file sits, which is already
how the stdlib works: `internal/std/dir/dir.shellf` declares `def ensure(...)`, and the
prefix comes from the folder. Nothing in the grammar knows about it.

The second is the rule below. It **removes** an exception rather than adding one.

## Decision

### 1. A dot is never valid inside a def name

`def file.write(...)` is a parse error, in a plan, a library, or an imported package.
The dot separates package from action when an instruction is *called*; it is never
part of what an author writes after `def`.

### 2. A subdirectory of a package is a sub-package

Given a package directory, each immediate subdirectory is a sub-package whose defs are
qualified by the directory name:

    deploy/                     package of the invoked plan
      plan.shellf               def deploy-app(...)      -> deploy-app
      dir/
        ensure.shellf           override def ensure(...) -> dir.ensure

This is the rule `internal/std` already follows, applied to user packages. One
mechanism now covers the stdlib, local packages and imports: the container names, the
declaration does not.

### 3. One level, no deeper

ADR-0032 §2 fixes **exactly one dot per name**. A second level would produce `a.b.c`
and contradict it. Nesting beyond one level is an error naming the offending
directory, not a silently ignored folder — a silently ignored one is how an override
fails to apply while the plan reports success.

### 4. Overriding is placement plus annotation

To replace `dir.ensure`, an author creates `dir/` in the package and declares
`override def ensure(...)` inside. `override` stays mandatory (ADR-0014 §5): placement
alone must not shadow, because an accidental directory name would then change
behaviour silently.

A directory named after a stdlib package is therefore **legitimate by itself** — it is
how an override is written. Only an un-annotated def inside it is an error, and the
message must say so, naming the resulting qualified name (`dir.ensure`) rather than
the bare one the author typed.

### 5. Loading follows the same rule everywhere

The CLI reads a package's `*.shellf` files and, one level down, each subdirectory's,
registering the latter under `<dir>.<def>`. Imported packages (ADR-0015) and remote
modules (ADR-0016) keep qualifying by alias; a sub-package inside an import would
require two dots and is refused by §3.

## Rejected alternatives

- **Allow a dot inside a def name** (`override def dir.ensure(...)`). One line to
  implement, and it was the obvious fix. Rejected: the dot would mean package
  membership on a call and a naming convention on a declaration, so `def a.b(...)`
  and a file in `a/` named `b` would be two spellings of one thing — with no rule
  saying which wins on conflict. It also makes package membership a property of a
  string an author types, rather than of where the file is, which is what the stdlib
  loader already relies on.
- **Keep ADR-0014 flat and drop overriding.** Honest but a real loss: ADR-0014 §5
  calls overriding «legitimate user autonomy», and it is the mechanism a user has to
  adapt an instruction to a system shellf does not ship support for.
- **Arbitrary nesting depth.** Contradicts ADR-0032 §2, and reintroduces exactly the
  directory sprawl ADR-0014 cites as the anti-pattern to avoid.
- **Package declared by a header inside the file** (`package dir`). Two sources of
  truth for one fact, and ADR-0014 rejected declarations in favour of the directory.

## Consequences

- Overriding one instruction now costs a directory, where it used to cost one line.
  That friction is what ADR-0014's flatness bought, and it is spent here knowingly:
  the alternative was losing overriding outright. It stays proportionate because the
  need is rare — a plain def in the package covers everything else.
- `shellfSources` currently skips subdirectories (`if e.IsDir() { continue }`); it
  reads one level down and qualifies accordingly.
- A directory nested two levels inside a package is a loud error, not a silent skip.
- The def grammar is unchanged: no dot, no new token, nothing added.
- ADR-0014 keeps its body; its flatness is relaxed by one level and its §5 override
  rule now reads as placement plus annotation.
