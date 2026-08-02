# ADR 0014 — User-defined defs: the directory is the package

## Status

Active

## Context

Instructions are self-hosted (issue #24): the stdlib is written as `def`s in
shellf and embedded in the binary. But **only** stdlib defs exist — a user
cannot write their own instruction (`my-backup(dir)`) and call it (issue #169).
The plan parser rejects `def`, there is no way to load a user def, and
resolution is `std.Lookup` only.

We want users to write and use their own instructions, and to reuse a def
written in one file from another file — **without** an import/module/registry
mechanism, which is deliberately deferred (ADR-0002, phase 3).

Two forces shaped the design:

- Cross-file reuse cannot be free: for file B to use a def from file A, shellf
  must know A exists. The only options are a CLI flag (`--defs A`), an import
  statement in B (that *is* the deferred imports feature), or a **package**
  convention that makes a set of files share a namespace.
- Overriding a stdlib def is legitimate user autonomy, but a **silent** override
  is a footgun (an accidental name clash shadows the stdlib and breaks behavior
  mysteriously).

**Mental model — Ansible, minus the sprawl.** shellf already fits Ansible's
shape: the invoked plan file is the **playbook** (ordered `on` blocks), the
`--inventory` file is the **inventory**, and a `def` is a **role** (a reusable
unit invoked by name). Ansible's lesson on the ordering question is decisive:
play order is **explicit** (top-to-bottom in the playbook, or explicit
`import_playbook`), **never** inferred from filenames; roles are auto-discovered
by name and carry no execution order of their own. We adopt those *principles*
but keep shellf's own vocabulary (`plan`/`def`/`inventory`) and reject Ansible's
directory sprawl (`roles/x/tasks/main.yml`, `group_vars/`, `import` vs `include`,
`ansible.cfg`) — a flat directory of `.shellf` files is the whole convention.
Cramming Ansible's ecosystem in would forfeit the one thing shellf sells: less
ceremony.

## Decision

### 1. A package is a directory — implicit, no declaration

The directory of the invoked plan file **is** its package. `shellf run
deploy/plan.shellf` loads every `*.shellf` file in `deploy/` as one package;
there is no `package <name>` header (zero ceremony — the directory is the unit,
Go's model minus the declaration).

All defs in the package share one namespace: a `def` written in one file is
usable from any other file in the same directory, with **no flag and no
import**. Order is irrelevant (names resolve after the whole package is loaded).

### 2. Cross-directory reuse is out of scope (it is imports)

A package references only itself. Using a def from *another* directory requires
naming that dependency — which is the imports/modules feature, deferred to
phase 3. v1 introduces **no** `import`/`use` syntax, no paths, no versions. One
directory, no more.

### 3. Only the invoked plan file targets hosts; siblings are defs-only

The invoked file provides the `on` blocks that execute, in explicit
top-to-bottom order (it is the playbook). Sibling `*.shellf` files in the package
contribute **defs only** — an `on` block in a sibling is an error, because
merging on-blocks across files would need an execution order, and inferring it
from filenames is exactly the implicit-ordering trap Ansible avoids. Splitting
ordered `on` blocks across files is the imports feature (Ansible's
`import_playbook`), deferred to phase 3. The file passed to `--inventory` is
parsed as the inventory and excluded from the package's def load.

### 3b. Vocabulary stays shellf's own

The concepts map to Ansible (playbook/inventory/role) but the **names** do not:
shellf keeps `plan`, `def`, and `inventory`. `plan` is more accurate than
`playbook` (it is what `--check` previews), `def` is universal and carries no
role-layout baggage. The model is the compass; the vocabulary is the identity —
"successor", not "clone".

### 4. Resolution: package defs ∪ embedded stdlib; duplicates error

An instruction name resolves against the package's user defs and the embedded
stdlib. A name defined twice **within the package** is an error (a real typo,
not an override). A user def whose name collides with a **stdlib** def is an
error too — unless it is an explicit override (Decision 5).

### 5. Overriding a stdlib def is explicit: `override def`

To shadow a stdlib name, a def must be declared `override def <name>(...)`. A
plain `def` colliding with a stdlib name is an error. `override` on a name that
shadows nothing is also an error (it catches a typo in the overridden name).
This keeps override intentional and **quiet** — no warning noise for a declared
override, a hard error for an accidental clash.

### 6. User defs travel to the agent as text; the agent re-parses

Defs are evaluated on the target. The stdlib rides in the binary via
`//go:embed`; user defs do not, so the per-host `proto.Request` carries the
package's user def **source** (raw text). The agent runs `lang.ParseDefs` on it
and registers the defs alongside the stdlib, resolving with the same
package-∪-stdlib and override rules.

The source travels as text, not as parsed `lang.Def`, because `proto` must not
import `lang` (a cycle: `lang` already produces `proto.Step`). Re-parsing on the
agent is cheap.

### 7. Signatures combine before the plan parses

The CLI parses the package defs first and builds the combined signatures
(stdlib ∪ package) that the plan parser needs to resolve `my-x(...)` arguments.
The duplicate/override checks (Decisions 4–5) happen here, before any host is
touched — a bad package fails fast, locally.

## Rejected alternatives

- **A `--defs <file>` flag.** Heavier to use (every run names the files); the
  directory-package needs no flag. Rejected in discussion.
- **An explicit `package <name>` header.** Ceremony for a config tool; the
  directory already is the boundary. Implicit chosen for ease of writing.
- **Cross-directory refs / imports / registry in v1.** The deferred phase-3
  feature; pulling it in now reopens the module-design hole ADR-0002 closed.
- **Silent override, or warn-on-override.** Silent shadows accidental clashes;
  a warning is noise on an intentional override. The `override` keyword is both
  intent-declaring and quiet.
- **Merging `on` blocks across package files.** Undefined execution order;
  on-blocks come from the single invoked file.
- **Sending parsed `lang.Def` over the wire.** Creates a `proto` → `lang`
  import cycle; the source travels as text instead.

## Consequences

- Users write instructions in `*.shellf` files next to their plan and call them
  with no flag, import, or declaration — the lightest possible UX.
- The plan parser learns to load a directory as a package and to accept `def`
  (and `override def`) declarations; the grammar gains the `override` keyword.
- `proto.Request` gains a `Defs` text field; the agent registers user defs
  before evaluating. The stdlib stays embedded and unchanged.
- Cross-file reuse works **within a directory**; reuse across directories waits
  for the imports phase.
- Implementation follows in slices (parser package-loading + `override`;
  resolution + collision/override checks; transport + agent registration; CLI).
