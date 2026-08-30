# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `dir.mode(path, mode)` sets a directory's own permission bits. `file.mode` chmods a directory perfectly well, so this adds no capability — it adds a call site that does not lie, next to `dir.ensure` / `dir.owner` / `dir.copy` / `dir.sync`. Not recursive (#505).
- `htpasswd.entry(path, user, password)` writes a basic-auth credential once. `openssl passwd` salts randomly, so a def comparing hashes rewrites the file on every run; this one verifies the stored hash against the password using its own salt. The password reaches openssl on stdin, never argv (#503).
- `systemd.unit(name, content)` installs a unit file, refusing one `systemd-analyze verify` rejects before it reaches /etc, and reloading systemd when the content changes. A timer is a unit: its schedule lives in its `[Timer]` section (#506).
- `system.timezone(zone)` sets the host timezone by writing `/etc/localtime` and `/etc/timezone`. Not `timedatectl`, which needs a system dbus a container has no — so the def stays exercisable against a real target (#499).
- `user.ensure` takes `system` (default false): a service account with no home, no login and a UID from the system range, which the def could not express since it always ran `useradd -m` (#501).
- `file.ensure(path, mode)` creates a file with an exact mode if it is absent, and never touches the content of one that exists. `file.write` always owns the content, so it could not express it (#502).
- `sysctl.set(key, value)` sets a kernel parameter live and across reboots, observing the running kernel rather than the file — a value persisted but not applied does not take effect until a reboot (#500).
- `docker.prune(until)` reclaims disk from unused images. Action-shaped: pruning always acts. `docker system prune` is deliberately not covered — it removes volumes (#504).
- ADR-0050: a verdict is observed, not asserted. A def that declares an `observe` re-reads it after acting, so an apply whose effect never landed reports `err.unconfirmed` instead of success — the cause behind #390, #411, #418, #480, #486 and #507 (#495).
- `docs/dogfood.md` records what a real deployment could not express in shellf. The first report — Debian 13, Traefik, an app built on the host, a systemd-timer backup — needed 6 `unsafe shell` blocks and surfaced 2 bugs and a language gap (#490).
- An adverse-state e2e plan per def: each starts from a state that is wrong on purpose and asserts the machine rather than the verdict. The coverage sweep proves idempotence, which a def that is wrong *stably* passes — #486 converged on both runs while the package was absent (#489).
- `test/e2e/vm.sh` runs the e2e harness inside a throwaway VM. The harness starts a `--privileged` container sharing the host kernel, which ended a developer's graphical session four times; the VM makes that cost nothing. CI is unchanged — a runner is already disposable (#529).
- `examples/plans/hosting.shellf`: the deployment that measured the language, now shipped as an example — Traefik with Let's Encrypt, an app built on the host, a systemd-timer backup, ufw, a service account. **No `unsafe shell`**, and the e2e harness applies it twice (#490).
- The README explains the escape hatch: shell an instruction already does does not parse, `unsafe shell` keeps it, and `grep -r 'unsafe shell'` lists every place the guarantees stop. `docs/language.md` says what to do with one you have written (#497).
- Four adverse cases pass a hostile *argument* rather than only a hostile starting state: a path carrying a space and a quote, and a line full of grep metacharacters. The stdlib holds — this is the first time anyone checked rather than assumed (#527).
- ADR-0052: `${inventory.<field>}` interpolates a host's own values, resolved per host. Additive — `${plain}` stays global and parse-time. `--set` does not override it, and `key` is refused: it is the path to a private key (#536).
- `${inventory.<field>}` interpolates a host's own values inside a string, resolved per host — the domain in `hosting.shellf`'s inventory is no longer written twice. `${plain}` stays global and parse-time; `key` is refused (#509, ADR-0052).
- `engine.PhaseAware`: an executor can be told which phase is starting, so a test fake tells an observe from an apply without matching the text of a def's shell. Three def fixes in one week broke tests in unrelated packages for want of it (#516).

### Changed

- A read-only question that answers *no* in `--dry-run` reports `would` instead of an error, so a plan that deploys a service and then checks it can be previewed to the end. A *yes* still resolves (#508, ADR-0051 amending ADR-0004).

### Fixed

- `systemd.unit` judges a unit by what `systemd-analyze verify` reports, not by its exit code — which is backwards in both directions: it fails a well-formed unit whose `ExecStart` is not on disk yet, and succeeds on a malformed one. A problem in the file carries a line number; an environment one does not (#525).
- A def that declares an `observe` re-reads it after acting: an apply whose effect did not land now reports `err.unconfirmed`, naming the field that did not move, instead of the success its shell's exit code claimed. Action-shaped defs and converged runs are untouched (#495, ADR-0050).
- The `ufw` defs converge while the firewall is down. `ufw.open` and `ufw.default` observed `ufw status`, which reports nothing until ufw is enabled — and every plan opens SSH and sets its policies *before* enabling, so both acted on every run of a first deployment (#515).
- `apt.update` is action-shaped: it always refreshes and says so. Its observe read a mtime that records when the repository last published, not when this host refreshed, so it could essentially never converge and acted on every run while reporting otherwise. Gate it like a handler (#488).
- The e2e convergence sweep matches column-aligned verdicts. It required a single space before `ok.`, so every def rendering short — `apt.update()`, `ufw.enable()` — escaped the guard that fails a def observing state yet acting on a converged target (#518).
- `dir.owner` no longer reports a path that does not exist as correctly owned. It read convergence from `find` printing nothing, and a missing path — or a subdirectory it cannot read — prints nothing (#507).
- `file.replace` writes its value instead of interpreting it. It built a sed expression from its own arguments, so `&` spliced the matched line back in and `|` failed the run: an URL with a query string landed corrupted, and the def then rewrote it on every run (#487).
- `apt.install` observes whether a package is installed, not whether dpkg still holds a record of it. `dpkg -s` exits 0 for a package removed without `--purge`, so the def reported `already` on a host whose binaries were gone — stably, on every run (#486).

## [0.8.0] - 2026-08-21

### Added

- A failing step reports the shell command that failed, where it is written (`file.mode:6`) and the values it could read. `-v` reports every command a step ran, successful ones included. The text is the source, `$var` unexpanded — no substituted command line ever runs, and reconstructing one would print a secret (#470).
- `file.owner(path, owner)` changes one file's owner without touching its directory, a case that previously needed `unsafe shell { chown … }` (#480).

### Changed

- **BREAKING** — a template placeholder is `~{var}`, not `@{var}`, and the `@@` escape is gone: it made `admin@@{domain}` render as literal text, so a mail map was unwritable. `~{raw}` … `~{endraw}` now marks a verbatim region, letting a template document its own placeholders (#481, ADR-0049).
- `docs/language.md` documents `import`, which it had never covered — both the local form and the remote one, with why `shellf.lock` is committed. The remote form also appears in the blog example, commented, using the RFC 2606 documentation domain (#376).

### Fixed

- `dir.owner` observes the tree it chowns. It checked only the top directory while applying `chown -R`, so a directory already owned by the right user reported `ok.already` whatever its contents belonged to — a key left root-owned inside it stayed that way, with the run green (#480).

## [0.7.0] - 2026-08-19

### Added

- `--json` reports a run or a status sweep as machine-readable JSON on stdout, versioned so a consumer can detect a shape change. Secrets are masked in their escaped form too, since JSON encoding hides a secret containing a quote or a backslash from plain masking (#459).
- `--limit <host|group>` narrows a run or a status sweep to part of what the plan targets, repeatable. It can only narrow, never extend; a limit that selects no host errors instead of reporting a green run that touched nobody (#460).
- `--parallel <n>` sets how many hosts a run or a status sweep dials at once, instead of a constant 16 nobody could change without recompiling. `1` serialises the fan-out; a value below 1 is refused rather than read as unlimited (#462).
- `-v` traces the control host's decisions on stderr — where it connected, the target's architecture, whether the agent was pushed or reused, the workdir chosen, and how long the job took. Secrets are masked, and stdout keeps carrying the report alone (#461).
- arm64 targets. A release binary embeds the agent for the other architecture and pushes the one `uname -m` reports, so an amd64 control host can configure an arm64 host and back. A plain `go build` carries no peer and refuses a foreign target by name instead of pushing a binary it cannot run (#453, ADR-0048).
- CI runs the cross-architecture push it could not test before: an amd64 control host provisioning an emulated arm64 target, asserting that the pushed agent is an aarch64 ELF and that a second run converges (#457).

### Changed

- `askOnce` waits for a bridge through `attached` instead of open-coding the same lock/wait/relock sequence, so a timing fix cannot land in one copy and miss the other (#473).
- `docs/design.md` is in English like every other written artifact, and `docs/CONVERSATION.md` — a frozen French transcript git still holds in full — is removed from the reading order (#465).
- The README no longer names a version in its prose — it announced 0.4.0 while 0.6.0 shipped. A release badge carries it instead, so it cannot go stale (#452).
- Four duplicated comment blocks are gone, including two whose stale first half contradicted the second — `packageLibs` documented a def layout ADR-0038 replaced, and `workdirEnsureCmd` opened with the doc of a function that no longer exists (#463).

### Fixed

- A plan targeting a name the inventory does not define is refused before anything runs, instead of reporting an empty block and exiting 0 — a typo in a group name was a deployment that never happened, reported green. A group listing an undeclared alias is caught at load, and a declared-empty group reports `(no hosts)` (#451).

## [0.6.0] - 2026-08-18

### Added

- `--dry-run` shows a unified diff of what changes in a file, under the instruction line, instead of only `would.written`. A new destination reports its line count, a long diff is cut at 40 lines, and secrets stay masked by value (#440).

### Changed

- `main` is the repository's default branch, so a visitor lands on what ships rather than on integration (ADR-0047). CI now refuses a PR into `main` from anything but `develop`, since `gh pr create` without `--base` targets the default (#436).

### Removed

- Seven functions nothing called: five convenience wrappers over a fuller form, and two residues whose last caller had been deleted. `test/dead-code.sh` now fails CI on an unreachable function, with no exemption list (#447).

### Fixed

- A def calling another keeps the error it propagates: `err.validation` stays `err.validation` instead of becoming `err.agent`, which means the agent could not run. Branching on a specific error, which `language.md` documents, now works one call deep (#441).
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

[Unreleased]: https://github.com/haribo/shellf/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/haribo/shellf/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/haribo/shellf/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/haribo/shellf/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/haribo/shellf/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/haribo/shellf/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/haribo/shellf/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/haribo/shellf/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/haribo/shellf/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/haribo/shellf/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/haribo/shellf/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/haribo/shellf/releases/tag/v0.1.0
