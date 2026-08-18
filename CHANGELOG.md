# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- `main` is the repository's default branch, so a visitor lands on what ships rather than on integration (ADR-0047). CI now refuses a PR into `main` from anything but `develop`, since `gh pr create` without `--base` targets the default (#436).

### Fixed

- A host whose login shell is not POSIX is reachable again. v0.5.0 escaped the transport's commands with a POSIX-sh quoting idiom, read by the login shell before `sh` exists, so nushell hosts died at the first probe (#439).

## [0.5.0] - 2026-08-17

### Added

- shellf is MIT-licensed (ADR-0046). The repository carried no license at all, so the default was all rights reserved — nobody could legally fork, package or redistribute it (#427).

- Control-host primitives: `~file.read`, `~file.render` and `~dir.list` read from the machine running shellf, and `%"path"` marks a path as living there (ADR-0034). A def can reach a template or a tree on the operator's disk without a Go transformation (#317).
- `bytes`, an opaque value type for content read from the control host: it can be handed to an instruction and nothing else, so binary is never mangled into text (#317).
- A def may call another instruction, so the stdlib composes instead of every def being an island (ADR-0030). Call cycles are refused with their chain (#296).
- `${…}` is interpolated in a def body against the def's own scope, as a plan does against globals (#296).
- A subdirectory of a package is a sub-package, one level deep, and its defs are qualified `<dir>.<def>` (ADR-0033). This is how a stdlib instruction is overridden (#306).
- `~file.write(path, bytes)` writes on the target, and `~file.read` reads either side — the control host when marked `%"…"`, the target otherwise. A plan can now deliver a single binary file (#332).
- `sudo.write(name, content)` and `sshd.config(name, content)` deliver a drop-in validated by `visudo -cf` / `sshd -t -f` in the `check` phase, at the mode each daemon requires (#328).
- `~file.render` runs on the control host over each host's variables, so `file.template` becomes an ordinary def instead of a Go transformation (#334).
- A `~file.render` ask carries the variables in scope at the call site, so a `with { }` override or a def parameter reaches the template (#334).
- `shellf status` opens the control channel, which an `observe` calling a primitive needs (#334).
- A def can **delegate**: one call outside every phase, and the def *is* that def with rebound arguments (ADR-0037 §2). The callee's phases then run in every mode (#339).
- A `preview` phase can describe a primitive, not only a shell — what previewing a destructive one requires (#373).
- `dir.sync(%"src", dst)` makes the target match the source: it delivers, and it removes what the source does not have. `--dry-run` names every file it would delete (#373).
- The shipped examples exercise 24 of the 25 constructs a reader can meet, up from 8, and the e2e harness runs each one against a real target (#357).

### Changed

- **BREAKING** — every stdlib instruction belongs to a package: `file-write` is `file.write`, `template` is `file.template`, and so on for 25 names (ADR-0032). An old name fails naming its replacement (#305).
- **BREAKING** — content validation is no longer a parameter of `file.write` / `file.template`: it belongs in the `check` phase of an instruction that knows the format (ADR-0030), where it also runs in `--dry-run` (#323).
- **BREAKING** — phase and mode vocabulary (ADR-0035): `pre-check` folds into `check`, `post` is removed, and `--check` becomes `--dry-run`. Each removed name is refused naming its replacement (#326).
- **BREAKING** — `~` marks a primitive and `%` marks a control-host path (ADR-0036); `%file.read(…)` becomes `~file.read(…)`. The old spelling is refused, naming the new one (#332).
- **BREAKING** — an `apply` must end with a `return` naming what it did (ADR-0037 §1). The implicit `ok` made a forgotten `return` read as a deliberate success (#339).
- **BREAKING** — a project is laid out by type: `plans/`, `defs/<package>/`, `assets/`, `inventories/` (ADR-0038). Paths anchor at the project root, and `shellf.lock` moves there (#355).
- **BREAKING** — `dir.copy` is a def over the new `~dir.sync` primitive and its source must be marked `%"…"` (ADR-0039). The 32 MB ceiling is gone, files stream, and only what differs is sent (#335).
- **BREAKING** — a `shell { }` whose command has an exact def equivalent no longer parses: `mkdir` names `dir.ensure`, `cp` names `file.copy` / `dir.copy` (ADR-0040). `unsafe shell { … }` keeps the shell and marks where shellf's guarantees stop (#382).
- **BREAKING** — `~file.render` renders a declared file instead of content handed to it (ADR-0042): `~file.render(~file.read(src))` becomes `~file.render(src)`. An imported def could otherwise send `"@{db_password}"` and be answered with the secret (#392).
- **BREAKING** — a `%"…"` inside a def no longer parses (ADR-0043): only a plan names a file on your machine, and a def receives that path as a parameter. A def could add itself to the allow-list meant to bound it (#403).
- **BREAKING** — a parameter's declared type is checked, by value (ADR-0045). `service.ensure("cron", "yes", "true")` reported `ok.converged` while stopping the service; `"yes"` is now refused when the plan is read, while `true`, `"true"` and a variable holding one all pass (#418).
- A tree transfer inside `as root { … }` honours the escalation (ADR-0044). It used to write from the agent's own process, so the tree landed owned by the connecting user under `ok.copied` (#390, #409).

### Fixed

- A `dir.sync` that only deletes no longer fails after a stale-bridge retry. Clearing the staging area removed the directory itself, which a transfer with a file to deliver recreated by accident and a delete-only one did not (#431).

- `file-write` stages beside the destination and renames over it, so a reader can no longer catch the file partial mid-write (#298).
- A `template` used as an `if` condition is rendered on the control host instead of reaching the agent verbatim and failing `err.agent` (#293).
- A def whose `check` or `observe` runs a shell no longer reports `changed` on a converged run — the false positive idempotence exists to prevent (#328).
- `ResolveRefs` no longer drops a step's control-host marking on the way to the agent, which made `file.template(%"conf.j2", …)` read the path on the target (#334).
- `file.template` is idempotent again: it renders in `observe` and compares with the destination instead of rewriting it every run (#334).
- An ask survives a bridge left over from a previous session, seen as an intermittent `err.agent` about one run in two against a real target (#334).
- `shellf status` no longer acts on the target. The engine handled check mode and then fell through to apply, so `status` wrote files on a host the operator only meant to look at (#338).
- `sudo.write` and `sshd.config` tell the truth in `--dry-run`: each now observes the content **and** the mode its daemon insists on (#340).
- A run survives its control-channel bridge being dropped. ADR-0031 §2 promised a reconnection; the control host dialled once (#347).
- A call cycle is refused when the defs are loaded, not mid-run on a partially applied host (ADR-0030 §6). The evaluator's guard stays as a backstop (#311).
- `dir.owner` converges. Its `observe` compared `user:group` to an argument naming a user, so every run reported `changed` — invisible to unit tests, found by running it twice against a container (#367).
- An error caught with `?` no longer fails the run, so `shellf run … && …` works for a plan using the language's own error handling (#356).
- `file.copy` no longer reports `ok.already` over a stale file. Its `observe` asked whether the destination existed, which is true forever after the first run (#378).
- On a converged host, `file.copy`, `dir.copy` and `dir.sync` no longer preview `would` for a write that would not happen. An `apply` that cannot act is now evaluated in check mode (ADR-0041), and a `shell { }` in it keeps the conservative verdict (#380).
- `dir.sync` no longer reports `ok.already` after deleting files: the transfer counted what it wrote and discarded the removal list, so a destructive action was announced as a no-op. The check-mode half was fixed in #384, so the dry-run told the truth while the real run did not (#387).
- Three `observe` phases asked a weaker question than their `apply` answers, so each reported converged over a host that was not: `git.sync` compared its ref to itself, `file.line` matched a substring, `ufw.open` matched a port anywhere in `ufw status` (#388).
- A delivered file is replaced, not written through: `~file.write` ran `cp` into the destination's own inode, so a daemon reloading its config could read it truncated (#389).
- The agent binary and its workdir are verified before use. A cache hit was `test -x` at a public path, so any local user could plant the file shellf executes — often under `as root` (#391).
- A symlink in `assets/` no longer reaches outside the project: the allow-list's containment check was lexical, so a link at `assets/leak.txt` was declared, served and landed on the target under `ok.copied` (#393).
- The docs stopped describing a shellf that does not exist: the site's hero ran a renamed flag on a plan outside its project, and `README.md` and `docs/language.md` documented renamed instructions and a `when err` form the parser never accepted (#394).
- Passing the bytes of `~file.read` where a string is expected is refused instead of silently becoming `""`. `file.write(dst, ~file.read(src))` delivered a 0-byte file under `ok.done`, and a shipped example had been doing it since #375 (#411).
- A symlink inside a transfer's destination no longer carries the write out of it, which since ADR-0044 happened under `sudo`. `dir.sync` can now remove such a link, named in `--dry-run`; `dir.copy` refuses and points at it (#412).
- The workdir on the target is created exclusively, in the command that checks it. A `mkdir -p` after a probe accepted a directory another user had created in the window, into which the agent runs every request it finds (#413).
- `dir.copy(%"src", dst, "sha256")` parses. A stale builtin entry shadowed the def's real signature, so an argument documented in `language.md` and the README was refused (#414).
- **BREAKING** — `unless { … }` in a def no longer parses. It was stored and read by nothing, so `shell { … } unless { true }` ran the command with the guard holding (#415).

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

[Unreleased]: https://github.com/haribo/shellf/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/haribo/shellf/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/haribo/shellf/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/haribo/shellf/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/haribo/shellf/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/haribo/shellf/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/haribo/shellf/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/haribo/shellf/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/haribo/shellf/releases/tag/v0.1.0
