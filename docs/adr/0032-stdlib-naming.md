# ADR 0032 — Every stdlib instruction belongs to a package

## Status

Active. Fixes a convention the code applied inconsistently and no record ever stated.
Consumed by the primitives ADR, which names `%<package>.<action>`.

## Context

The stdlib carries two naming shapes with no written rule:

    file-write, file-mode, dir-ensure, service-restart, wait-for      (22 defs, no package)
    docker.install, ufw.open, apt.install                             (9 defs, packaged)

`internal/std/std.go` documents the *mechanism* — root `*.shellf` files form the
unqualified `std` package, each subdirectory becomes a package prefixed `<pkg>.` — but
never says whether a name like `file-write` ought to be `file.write`. Searching the 31
records, `docs/language.md`, `docs/design.md` and `docs/CONVERSATION.md` returns nothing:
the convention was decided in discussion and never written down.

It became blocking when naming the control-host primitives: `%file.read` sitting next
to a def called `file-write` reproduces the very inconsistency it should settle.

## Decision

### 1. Every stdlib instruction belongs to a package

No stdlib def keeps a bare name. The dot means **package membership**, and membership
becomes mandatory rather than incidental to living in a subdirectory.

### 2. The dot separates the package; the dash separates words inside the action

`docker.compose-up` already reads this way and stays valid. So do
`archive.extract-member`, `systemd.daemon-reload` and `http.wait-for`. One rule, no
exception: **exactly one dot per name**.

### 3. The mapping

| Package | Today | Becomes |
|---|---|---|
| `file` | `file-write`, `file-mode`, `file-line`, `file-delete`, `file-replace`, `file-exists`, `file-download` | `file.write`, `file.mode`, `file.line`, `file.delete`, `file.replace`, `file.exists`, `file.download` |
| `file` | `file-copy`, `template` *(builtins)* | `file.copy`, `file.template` |
| `dir` | `dir-ensure`, `dir-exists`, `dir-owner`, `dir-copy` *(builtin)* | `dir.ensure`, `dir.exists`, `dir.owner`, `dir.copy` |
| `archive` | `archive-extract`, `archive-extract-member` | `archive.extract`, `archive.extract-member` |
| `git` | `git-clone`, `git-sync` | `git.clone`, `git.sync` |
| `service` | `service`, `service-restart`, `service-reload` | `service.ensure`, `service.restart`, `service.reload` |
| `systemd` | `systemd-daemon-reload` | `systemd.daemon-reload` |
| `user` | `user-ensure`, `user-group` | `user.ensure`, `user.group` |
| `http` | `http-check`, `wait-for` | `http.check`, `http.wait-for` |
| unchanged | `docker.*`, `ufw.*`, `apt.*` | already conform |

Three entries deserve their reason:

- **`service` → `service.ensure`.** A bare `service` cannot survive the rule, and the
  name never said what it did. `ensure` is already the project's verb for "bring to
  the desired state" (`user-ensure`), so the rename makes an existing convention
  explicit rather than inventing one.
- **`wait-for` → `http.wait-for`.** Its signature is `(url, timeout)`: it waits on an
  HTTP endpoint, and belongs with `http.check`. A `net` package was considered and
  rejected — it would hold this single member and blur the boundary with `http`.
- **`template` → `file.template`.** It delivers a file; the rule admits no exception
  for a name because it is well established.

### 4. No aliases, no transition period

The old names are removed in the same change that introduces the new ones. Accepting
both would mean carrying two names in the stdlib, in the docs and in every example,
for a benefit that only exists for plans written before the rename.

The break is real and must be stated: the stdlib ships **inside the binary**, so
upgrading shellf breaks a plan nobody touched. Mitigation is a good error, not a
grace period — an unknown instruction whose name matches a renamed one reports the
new name:

    unknown instruction "file-write" — renamed to "file.write" (ADR-0032)

This is help at the point of failure, not a second accepted spelling: the plan still
fails, and the fix stays a one-line edit.

### 5. The rename lands before the primitives

Doing it first means `%file.read` is born beside `file.write` instead of beside
`file-write`. Doing it after would ship the primitives with a naming already out of
step with the library they sit next to, and force a second pass.

## Rejected alternatives

- **Keep both shapes, package only what already lives in a subdirectory.** Today's
  situation: the dot then means "happens to be in a subdirectory", which is an
  implementation detail leaking into the language.
- **Dot as a word separator** (`file.write`, `daemon.reload`). Collides with package
  membership, which the dot already means for imports (ADR-0015) and remote modules
  (ADR-0016) — `web.deploy` is an alias, not a word boundary.
- **A transition period with both names.** Rejected in §4. The user base is one
  repository, and the cost of carrying duplicates outweighs a mechanical migration.
- **A `net` package for `wait-for`.** One member, and an unclear boundary with `http`.

## Consequences

- 22 defs and 3 builtins are renamed; `docker.*`, `ufw.*` and `apt.*` are untouched.
- Root `*.shellf` files move into per-package subdirectories, which is what makes the
  prefix real rather than declared — `std.go` derives the prefix from the directory.
- Every plan calling a renamed instruction breaks until edited. Measured on the first
  consumer, the `hosting` repository: **60 call sites across 8 plans** — `template`
  (25), `file-mode` (11), `dir-ensure` (9), `wait-for` (5), `dir-copy` (2), `service`
  (2), `systemd-daemon-reload` (2), `file-replace`, `service-restart`,
  `file-download`, `file-exists` (1 each). They must be migrated in step with the
  shellf release carrying this change, which is the practical argument for a good
  error message: sixty failures, each naming its replacement, is a mechanical edit;
  sixty `unknown instruction` is a treasure hunt.
- The engine gains a rename table used **only** to produce the error of §4.
- Docs, `README.md` and `examples/` are rewritten to the new names in the same change;
  a half-migrated example is worse than none.
