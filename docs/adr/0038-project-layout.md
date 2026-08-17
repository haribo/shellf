# ADR 0038 — A shellf project is laid out by type: `plans/`, `defs/`, `assets/`, `inventories/`

## Status

Active. **Supersedes [ADR-0014](0014-user-defs-directory-package.md) §1**, whose decision it keeps
— a package is a directory, implicit, with no `package` header — and whose anchor it
moves: the unit is the **project**, not the directory of the invoked plan. ADR-0014's §2
and §3 (cross-directory reuse is imports; an override must be declared) stand, and
[ADR-0015](0015-local-imports.md)/[ADR-0016](0016-remote-modules.md) are untouched.

## Context

shellf imposes almost no structure. ADR-0014 made the invoked plan's directory the
package, and that is the whole convention. It was right for a first version — nothing to
learn — and it does not survive a project with more than a handful of files.

Two failures show up as soon as one does.

**Reuse forces a refactor.** An imported package may hold only defs
(`internal/lang/parser.go:158`: `an imported package may only contain defs`). So a
directory grouped by *concern* — a plan next to the defs it uses — **cannot be imported
at all**. The day a def written for `monitoring/` is wanted elsewhere, it must be moved
out. Reuse becomes a restructuring, which is the tax a layout exists to remove.

**Finding a def means knowing where its author put it.** With grouping by concern,
`toto.write` may live anywhere. Nothing in the name says where to look, and the cost
grows with every package added.

Both are avoided by grouping by **type**: a def's name maps to its location
mechanically, and defs are never in the same directory as a plan, so they are always
importable.

## Decision

### 1. The layout

```
myproject/
  plans/                      # files with `on` blocks — what a run invokes
    monitoring.shellf
  defs/                       # reusable instructions, one directory per package
    toto/
      write.shellf            # -> toto.write
  assets/                     # content to deliver: templates, files, trees
    toto/tutu/titi.txt
  inventories/                # host declarations
    production.shellf
```

Four directories, each holding one kind of thing. A reader looking for `toto.write` goes
to `defs/toto/`; a reader looking for what runs goes to `plans/`.

### 2. The project is the package; the project root is the anchor

The unit is the directory holding these four, not the directory of the invoked plan. This
is the part of ADR-0014 that moves, and everything else follows from it.

`defs/<name>/` is loaded and its defs are qualified by the directory
([ADR-0033](0033-sub-packages.md)'s rule, applied one level up): `defs/toto/write.shellf`
declares `write`, and a plan calls `toto.write(…)` — no `import`, no flag. The zero
ceremony ADR-0014 bought is preserved; only the directory it applies to changes.

`plans/` holds every file with `on` blocks. A plan is what a run invokes and is never
imported — which is what makes `defs/` importable by construction.

### 3. A content path is relative to `assets/`

```
myproject.write(%"toto/tutu/titi.txt", "/etc/titi")
```

The path names a file under `assets/`. No `../`, no root marker, no anchor syntax: the
layout is known, so the root is known.

`%` keeps exactly its meaning from [ADR-0036](0036-primitive-and-location-markers.md) —
the file is on the operator's machine, not on the target. The two notations say different
things and do not overlap: `%` says *whose* machine, the layout says *where* on it.

A control-host path outside `assets/` is refused. Stated as a decision rather than
inherited as a side effect: it bounds what a plan can read from the operator's disk,
which is what the allow-list of [ADR-0031](0031-control-channel-and-detachment.md) §3
already wanted, and it makes the answer to "where does this file come from" a single
directory instead of a search.

### 4. Prescriptive, not suggested

shellf resolves against this layout. A project not laid out this way does not work — it
is not a default one can override.

The alternative — assumed but overridable — was considered and rejected: an overridable
layout is two resolution rules, and the second exists to serve projects that the first
was written for. The point of a convention is that a reader coming from another project
recognises this one.

`--inventory <path>` stays explicit and unchanged. Whether `inventories/` should make it
optional is deliberately left open: it is a CLI question, not a layout one, and nothing
here depends on the answer.

## Rejected alternatives

- **Keep ADR-0014's flat package.** The status quo. It has no answer for two plans
  sharing defs or content, and every project reinvents one — which is what #307 was
  opened to record: a convention was invented in one project, written in comments for
  lack of anywhere else, and would have been reinvented differently in the next.
- **Group by concern** (`monitoring/` holding its plan, its defs, its content). Reads
  better for a single subject, and cannot be imported — reuse means moving files. Also
  leaves `toto.write` findable only by knowing who wrote it.
- **A `//` root anchor** (Bazel's spelling) on top of the current model. Solves the `../`
  problem without prescribing anything, and is unnecessary here: once the layout is
  fixed, the root of a content path is already known. Kept in this record because it is
  the right answer for the *other* branch — if the layout is ever made optional, this
  comes back.
- **Ansible's search paths.** Ansible resolves a bare `src` by looking in several
  directories in order (a role's `files/`, then the playbook's directory). One root here,
  not a list: a path exists under `assets/` or it does not, so there is nothing to guess
  and nothing to guess wrong. This is the one point where a layout with implicit
  resolution stays honest.
- **Ansible's `remote_src: yes`** instead of `%`. Ansible marks control-host-vs-target
  with a boolean on the task. A flag does not propagate: it must be restated at every
  module and does not survive being wrapped in an abstraction. `%` marks the path, so it
  travels through a def call — behaviour shellf had to make work explicitly (#332), and
  which would be lost by switching.

## Consequences

- **[ADR-0020](0020-design-for-the-user-base.md) is stretched, deliberately.** It designs for a solo
  operator with a handful of machines; the argument that carried this record is a project
  with many packages, where finding a def by name matters. Both are defensible; recording
  the tension so the next reader sees a choice rather than an oversight.
- **`internal/lang` and `cmd/shellf` change where they look.** `packageLibs` walks the
  plan's siblings today and must walk `defs/` from the project root instead; `srcPath`
  and `NewAllowed` anchor on the plan's directory and must anchor on `assets/`.
- **Existing content moves.** `examples/blog/compose.env.tmpl`, `examples/webserver/`,
  and `test/e2e/` all keep content beside their plan. All of them break under §3 and must
  be restructured, the e2e harness included. A real migration, stated so it is weighed
  rather than discovered.
- **A plan can no longer have sibling defs.** The single most convenient thing about
  ADR-0014 — drop a `.shellf` next to your plan and call it — is gone. What replaces it is
  a directory away (`defs/<name>/`) and qualified by name.
- This record is a decision, not a report on the tree. What implements it, and when, lives
  in the issue that carries the work.
