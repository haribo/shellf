# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **BREAKING** — every stdlib instruction now belongs to a package: `file-write` is
  `file.write`, `wait-for` is `http.wait-for`, `template` is `file.template`, and so
  on for 25 names (ADR-0032). The dot separates the package, the dash separates words
  inside the action; exactly one dot per name. There is no alias and no transition
  period: a plan using an old name fails, but the error names its replacement —
  `unknown instruction "file-write" — renamed to "file.write" (ADR-0032)`. Three
  renames are not mechanical: `service` became `service.ensure`, `wait-for` became
  `http.wait-for`, `template` became `file.template` (#305).

### Added

- Control-host primitives (ADR-0034): `%file.read`, `%file.render` and `%dir.list` read
  from the machine running shellf, and `%"path"` marks a path as living there. One rule
  — `%` means my machine — for a call and for a path alike. A def can therefore reach a
  template or a tree on the operator's disk, which is what `file.template` and
  `dir.copy` did in Go, unreadably. `%` before anything but those three primitives is a
  parse error: a def can run shell, and shell prefixed by `%` would run where every SSH
  key lives. The control host serves only what the plan declared and refuses the rest by
  name, so an imported package cannot read `~/.ssh` (#317).
- `bytes`, an opaque value type for content read from the control host. It can be handed
  to an instruction and nothing else: interpolating it or putting it in a shell variable
  is refused rather than silently mangling binary into text (#317).
- `file.write` and `file.template` take an optional checker run on the staged file
  before it is installed: `file.template("sudoers.j2", "/etc/sudoers.d/x", "visudo -cf
  \"$staged\"")`. A failing checker leaves the destination untouched and returns
  `err.validation`, so the run halts before any handler gated on `.changed` can act on
  a broken file. It exists because some files lock you out when invalid — a broken
  sudoers breaks `sudo`, a broken sshd_config breaks the transport shellf itself uses —
  and their checkers are useless once the file is in place. Validation during `--check`
  is deliberately not included: it would let a plan run commands on a target in a mode
  documented as inert, which needs its own decision (#299).
- A def may call another instruction (ADR-0030), so the stdlib composes instead of
  every def being an island: a `def sudoers(...)` reuses `file.write` rather than
  reimplementing a file write in shell. The callee sees its own arguments only,
  inherits the caller's escalation unless it declares its own, halts the caller on
  `err`, and is evaluated in the caller's mode so nothing effectful runs in `--check`.
  A call cycle is refused with its chain (`a -> b -> a`). `changed` now means a shell
  ran or a callee itself reported changed, so a def whose apply only calls an
  already-converged instruction no longer claims to have acted (#296).
- `${…}` is interpolated in a def body against the def's own scope, matching what a
  plan does against globals (#296).
- A subdirectory of a package is a sub-package: its defs are qualified `<dir>.<def>`,
  one level deep (ADR-0033). This is how a stdlib instruction is overridden now that
  they all carry a package — create `dir/` and declare `override def ensure(...)`
  in it. A dot is never valid inside a def name: the directory names, the author does
  not. A directory holding no `.shellf` file is content, not code, and is ignored; one
  nesting a further directory is refused rather than silently skipped (#306).

### Fixed

- A `template` used as an `if` condition is now rendered on the control host like
  any other, instead of reaching the agent verbatim and failing `err.agent` with
  the plan halted. `dir-copy` in the same position is refused control-side with an
  explicit reason — it expands to one step per file, which a condition cannot hold
  (#293).
- `file-write` (and `template`, which becomes one) now stages next to the destination
  and renames over it, instead of redirecting at the destination and truncating it.
  A reader can no longer catch the file empty or partial mid-write — a window that
  existed on every write, not only on an interrupted run. The destination's mode and
  owner are carried onto the staging file, so a rewrite keeps them as before (#298).

## [0.4.0] - 2026-08-08

### Added

- `docker.compose-restart(dir, service)` — action-shaped handler that restarts one
  service, or the whole stack when `service` is omitted. Gate it on a real change
  (`if cfg.changed { … }`), like `service-restart`: a mounted-file edit never
  recreates a container, so `compose-up` alone leaves the stale config live.
  `--check` previews it read-only via `docker compose --dry-run restart` (#287).

## [0.3.1] - 2026-08-07

### Fixed

- Absolute `src` paths in `dir-copy` and `template` are honored (used as-is) instead
  of being silently joined onto the plan directory (#281).

## [0.3.0] - 2026-08-07

### Added

- Action-shaped defs can declare a `preview` phase — read-only, `--check` only — that
  describes what apply would do, rendered as a distinct `preview ▸` block that never
  reads as a convergence claim (ADR-0029). `docker.compose-up` uses it to show the
  per-service recreate/start plan (`docker compose up --dry-run`); best-effort, so an
  old Compose or an unreachable daemon degrades to a plain `would.up`.
- `dir-copy(src, dst)`: deliver a control-host file tree to the target **verbatim**
  and **binary-safe** — HTML and images alike, byte-for-byte, idempotent per file
  (ADR-0028). Resolved control-side into per-file `file-put` steps; refuses a tree
  over 32 MB with a clear error.
- Local transport: a host with `local: "true"` in the inventory is provisioned on
  the control host itself (agent run as a subprocess), no SSH — same agent, plan,
  and reports as a remote target (ADR-0027).
- `archive-extract-member(src, dst, member)`: extract a single file from a tarball
  to `dst` (content-hash idempotent) — compose with `file-download` + `file-mode` to
  install a binary from a release tarball without a raw `shell` escape.

### Fixed

- Report labels now show `name=value` for each argument (`file-mode(mode=755,
  path=…)`) instead of bare values in key order, which read as swapped.
- `archive-extract` re-extracts when the archive at `src` changes, instead of
  skipping because the destination is non-empty. Convergence is now decided by the
  archive's sha256, recorded in a `.shellf-archive-sha256` sentinel under `dst`.

## [0.2.2] - 2026-08-04

### Fixed

- The `examples/webserver/` example runs as documented: its inventory moved out of the
  plan's def-package directory (ADR-0014), and the invocation shows flags before the
  plan file. A CI test now parses every example so it cannot silently drift again.
- Capturing a `template` result now works: `s = template(src, dst)` then
  `if s.changed { … }` resolves instead of halting with `err.undefinedResult`.

## [0.2.1] - 2026-08-04

### Fixed

- Run transport commands under `/bin/sh` regardless of the target's login shell, so
  a non-POSIX login shell (nushell, fish) no longer breaks the agent bootstrap.

## [0.2.0] - 2026-08-04

### Added

- SSH authentication via the ssh-agent (`SSH_AUTH_SOCK`): the private key never
  leaves the agent, and an encrypted key works. The inventory `key:` is now an
  optional override, tried before the agent (ADR-0026).

### Changed

- `key:` is no longer required in the inventory; authentication falls back to the
  ssh-agent when it is absent.

### Fixed

- Expand `~` in the inventory `key:` path (and `--known-hosts`), so the documented
  `~/.ssh/…` examples connect instead of failing with `read key: no such file`.

## [0.1.0] - 2026-08-03

First tagged release. shellf provisions targets over SSH via a detached resident
agent that evaluates on the host — "raw shell, but idempotent, previewable, fast."

### Added

- Language: lexer, parser, and evaluator with the `observe`/`apply`/`status` def
  contract, `if`/`else`, error handling (`?`), parse-time `for` loops, and
  `with { }` per-call variable overrides.
- Idempotent execution with a `--check` preview and a `status` mode that reports
  current-vs-desired state per host.
- Templating: `template(src, dst)` with the `@{var}` delimiter, rendered per host
  over inventory variables.
- Secrets: `--secret-file` / `--secret-env` with value-based redaction in all
  output, and a tmpfs workdir that keeps plaintext off the target's persistent disk.
- Modules: directory-package user defs, local imports, and hash-verified remote git
  modules with `shellf.lock` and a content-addressed cache.
- Embedded, self-hosted standard library: apt, docker/compose, systemd, ufw, files,
  users, git-sync, http-check, and more.
- SSH transport with a resident agent (inactivity TTL), host-key verification, and
  per-user agent/workdir scoping.
- Commands: `run`, `status`, `clean`, and `version`.

[Unreleased]: https://github.com/haribo/shellf/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/haribo/shellf/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/haribo/shellf/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/haribo/shellf/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/haribo/shellf/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/haribo/shellf/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/haribo/shellf/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/haribo/shellf/releases/tag/v0.1.0
