# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **BREAKING** — `~` now marks a primitive and `%` marks a control-host path
  (ADR-0036); `%file.read(…)` becomes `~file.read(…)`. One marker could not express a
  primitive that writes on the target, which is what `~file.write` does. The old
  spelling is refused, naming the new one (#332).

### Added

- `~file.render` now runs on the control host, over the target's own variable set, so a
  template substitutes per host as before (ADR-0024). `file.template` is therefore an
  ordinary def over `~file.read` + `~file.render` + `~file.write`, and the Go
  transformation that rewrote it is gone — one of the two remaining special cases in the
  engine (#334).
- A `~file.render` ask carries the variables in scope at the call site, so a template
  substitutes over the host's variables *and* the caller's — a `with { }` override
  (ADR-0022) or a def parameter. The control host layers the call site on top: the most
  local binding wins (#334).
- `shellf status` opens the control channel too. An `observe` may call a primitive —
  `file.template` renders there to decide whether the destination is in sync — and
  without the channel every template reported `err.agent` (#334).
- `sudo.write(name, content)` and `sshd.config(name, content)` deliver a validated
  drop-in: `visudo -cf` and `sshd -t -f` run in the `check` phase, on the def's own
  temporary, so a refused content never reaches the write — and since `check` runs in
  `--dry-run`, a broken config is caught before any real run. Both set the mode the
  daemon requires (0440, 0600): sudo *ignores* a drop-in with any other mode, so a rule
  written 644 looks installed and does nothing. `sudo.write` also rejects a drop-in name
  containing a dot, which sudo silently skips (#328).
- `~file.write(path, bytes)` writes on the target, and `~file.read` now reads **either
  side**: the control host when its argument is marked `%"…"`, the target otherwise.
  Together they close a gap — until now no plan could deliver a single binary file, only
  a whole directory through `dir.copy`. `file.copy` is now an ordinary def over the two
  (#332).

- **BREAKING** — phase and mode vocabulary (ADR-0035). `pre-check` is folded into
  `check`, which now also runs under `status` — a def refusing its arguments refuses
  them there too. `post` is removed: nothing declared it and its meaning was never
  settled. The `--check` flag becomes `--dry-run`, because `check` the phase and
  `--check` the mode named different things: the mode ran four phases, while the phase
  also ran during a real apply. Both removed phases and the removed flag are refused
  naming their replacement. `docs/language.md` gains the mode/phase table whose absence
  let the confusion last (#326).
- Content validation is no longer a parameter of `file.write` / `file.template`. It
  belongs in the `check` phase of an instruction that knows the format — a `sudo.write`
  writing its own temporary and running `visudo` there — which then calls `file.write`
  in its `apply` (ADR-0030). `check` runs before any `apply` and its outcome wins, so a
  refused content is never written; and since `check` also runs in check mode, a bad
  config is caught before any real run. `file.write` stays within its own scope:
  whether the bytes are valid sudoers is not its business (#323).

- **BREAKING** — every stdlib instruction now belongs to a package: `file-write` is
  `file.write`, `wait-for` is `http.wait-for`, `template` is `file.template`, and so
  on for 25 names (ADR-0032). The dot separates the package, the dash separates words
  inside the action; exactly one dot per name. There is no alias and no transition
  period: a plan using an old name fails, but the error names its replacement —
  `unknown instruction "file-write" — renamed to "file.write" (ADR-0032)`. Three
  renames are not mechanical: `service` became `service.ensure`, `wait-for` became
  `http.wait-for`, `template` became `file.template` (#305).

### Added

- Control-host primitives (ADR-0034): `~file.read`, `~file.render` and `~dir.list` read
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

- `sudo.write` and `sshd.config` tell the truth in `--dry-run`. Both compose in `apply`
  and declared no `observe`, and `apply` never runs in check mode — so on a host whose
  drop-in was already correct they announced `would.written` for a write that would not
  happen. Each now observes the two things its apply sets: the content, and the mode the
  daemon insists on. The mode half matters on its own — sudo silently ignores a drop-in
  that is not 0440, so perfect content at 644 looked converged (#340).
- `shellf status` no longer acts on the target. It is documented as reporting state
  without acting (ADR-0013), and its usage line says so, but the engine handled check
  mode and then fell through to apply: every remaining Go instruction ran for real, so
  `status` wrote files and executed shells on a host the operator only meant to look at.
  It now reports `would.<tag>` for a resource that has drifted, and the guard still runs
  so a converged one is still recognised (#338).
- An ask survives a bridge left over from a previous session. A resident agent outlives
  the command that created it (ADR-0005), so `shellf status` and the `shellf run` that
  follows attach different bridges; the agent held the dead one, which looks alive until
  it is used and then answers EOF. Seen as an intermittent `err.agent` — about one run in
  two against a real target, and invisible to every unit test (#334).
- `file.template` is idempotent again. As a def it had no `observe`, so it rewrote the
  destination on every run and reported `written`; it now renders in `observe`, compares
  the result with the destination, and the binding it makes there is reused by `apply` —
  one round trip to the control host per run, not two (#334).
- `ResolveRefs` no longer drops a step's control-host marking on the way to the agent.
  It rebuilt each step field by field and omitted the one saying which arguments the plan
  wrote `%"…"`, so `file.template(%"conf.j2", …)` read `conf.j2` on the *target* — with
  no error to show for it when a file of that name happened to exist there. Local runs
  were unaffected, which is why the unit suite stayed green (#334).
- A def whose decision phase runs a shell no longer reports `changed` on a converged
  run. `check` and `observe` are reads; counting their shells as "acted" fired every
  `if x.changed { … }` on every re-run — the exact false positive idempotence exists to
  prevent (#328).

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
