# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **BREAKING** — a `shell { }` block whose command has an exact def equivalent no longer
  parses (ADR-0040). Two rules: `mkdir` names `dir.ensure`, `cp` names `file.copy` for a
  file and `dir.copy` for a tree — and says that neither carries mode or ownership, since
  `cp -p` does. `unsafe shell { … }` keeps the shell, runs identically, and composes with
  the interpreter override and with `if !`. The point is not style: a def tolerates being
  re-run, and a step that does not is what wedges a host after a partial failure —
  measured in #377, where the same def written with `mkdir` blamed the wrong step forever
  and the one written with `dir.ensure` converged as soon as the cause was fixed.
  `unsafe` means **"no def covers this"**, not "dangerous": an atomic `mkdir` lock is
  irreproachable and is marked, which is what keeps `grep -r 'unsafe shell'` — the list of
  every place shellf's guarantees stop, imported modules included — worth reading. The
  detector is a heuristic and says so: `$CMD`, `eval`, `xargs` and `find -exec` go through
  it, and each of the two rules carries its written justification because both were found
  inexact on first reading. The standard library is exempt — it is the layer that reaches
  the system (#382).
- **BREAKING** — `dir.copy` is a def over the new `~dir.sync` primitive (ADR-0039), and
  its source must be marked: `dir.copy("tree", …)` becomes `dir.copy(%"tree", …)`. The
  tree is read on the control host, and since #332 that is what `%` says — the marker was
  absent only because the copy used to be expanded before the plan was sent. The 32 MB
  ceiling is gone, files stream in chunks, and the transfer sends **only what differs**:
  a converged tree transfers zero bytes and reports `already`, where the old expansion
  inlined the whole tree into the request on every run. A third argument, `compare`,
  chooses size+mtime (default) or `"sha256"`; the default cannot see a change that
  preserves both, which is stated in `language.md` rather than left to be discovered.
  With it goes the last control-side transformation — `file.template` lost its own
  in #334 (#335).
- **BREAKING** — a project is laid out by type (ADR-0038): `plans/` holds what a run
  invokes, `defs/<package>/` the reusable instructions, `assets/` the content a plan
  delivers, `inventories/` the hosts. The anchor moves from the invoked plan's directory
  to the project root, so a def is addressed by name (`defs/toto/` → `toto.write`) and a
  content path is relative to `assets/` — `%"toto/tutu/titi.txt"`, with no `../` from
  wherever the plan happens to sit. A plan's siblings are no longer defs, and a control-
  host path resolving outside `assets/` is refused. Running a plan from outside a project
  fails naming the layout. `shellf.lock` moves to the project root, where it belongs: it
  pins what the project depends on (#355).
- **BREAKING** — an `apply` must end with a `return` naming what it did (ADR-0037 §1,
  reversing ADR-0007 §4). The implicit tag-less `ok` made a forgotten `return` and a
  deliberate "nothing to declare" report identically, so an omission read as a success.
  No stdlib def relied on it — all 31 `apply` blocks already returned — and the error
  names the fix.
- **BREAKING** — `~` now marks a primitive and `%` marks a control-host path
  (ADR-0036); `%file.read(…)` becomes `~file.read(…)`. One marker could not express a
  primitive that writes on the target, which is what `~file.write` does. The old
  spelling is refused, naming the new one (#332).

### Added

- The shipped examples now exercise the language rather than a corner of it: 24 of the 25
  constructs a reader can meet appear in `examples/`, up from 8. `parallel { }`, `else`,
  `.changed`, `with { }`, a triple-quoted raw string, a local `import`, a delegating def
  and a def with `check`/`preview`/`using bash`/`shell(sh)`/a defaulted parameter — each
  where it belongs, a def-authoring feature in a def and never in a plan to tick a box.
  The e2e harness runs every example against the real target, so a construct that stops
  working fails the build instead of misleading a reader. The 25th, `import` of a remote
  module, is proven by the harness — which builds a bare repository, tags it, imports it
  by URL and checks the resulting `shellf.lock` — and left out of the examples until a
  published module exists to point at (#357).
- `dir.sync(%"src", dst)` makes the target *match* the source: it delivers, and it
  **removes** what the source does not have. One word apart from `dir.copy` — the
  primitive already took the flag — which is why ADR-0039 refused to add it as a side
  effect and why it lands with a `preview` phase: `--dry-run` names every file it would
  delete, one per line, before deleting any. A destructive instruction whose dry-run says
  nothing tells the operator what they lost only afterwards. The primitive is inert in
  check mode by construction, so previewing costs a manifest exchange and touches nothing
  (#373).
- A `preview` phase can describe a **primitive**, not only a shell. It collected shell
  stdout alone, so a primitive had no way to say what it would do — which is exactly what
  previewing a destructive one requires (#373).
- A def can **delegate**: one call outside every phase, and the def *is* that def with
  rebound arguments (ADR-0037 §2). The callee's own phases then run in every mode, which
  an `apply` cannot do — `apply` is skipped in `--dry-run`, so a wrapper calling from
  there lost the callee's `check` and `observe` and previewed a write on a host it would
  not touch. `file.template` is the form's first user: three lines, and it sheds the
  `observe` it had to grow in #334 to re-decide what `file.write` already knew. Exactly
  one call, only `check` beside it, and no `shell` in its arguments — each a parse error
  naming the rule (#339).
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

- Three `observe` phases asked a weaker question than their `apply` answers, so each
  reported converged over a host that was not — the shape #378 and #387 already cost.
  `git.sync` resolved its ref **inside the checkout**, comparing it to itself: right after
  a checkout the local branch equals HEAD, so `ref = "main"` was converged forever and the
  fetch in `apply` never ran again. It now asks the remote (`ls-remote`, which reads
  without writing to `.git` as a fetch would) and asks for the peel too — `ls-remote <url>
  v1.0` answers with the tag *object*, which is never what a checkout's HEAD holds.
  `file.line` used `grep -qF` without `-x`, so adding `foo` to a file holding `foobar`
  reported `present` and never appended — `file.replace`, four lines above it, has always
  had the `-x`. `ufw.open` matched its rule anywhere in `ufw status`, so
  `ufw.open("80", "tcp")` reported `allowed` on a host where only `8080/tcp` was open; it
  now matches the port column exactly. Fixing `git.sync`'s observe exposed a fourth defect
  in its `apply` — `checkout --force main` re-checks-out the **local** branch, which a
  fetch does not fast-forward, so the def never delivered the update it exists for. It now
  checks out the remote-tracking ref, detached, which is what a deploy checkout wants
  anyway (#388).
- The agent binary and its workdir are verified before use. A cache hit was `test -x`
  at `/tmp/shellf-agent-<digest of the binary>-<user>` — both halves public, so any local
  user could create that file first and have shellf execute it, often to run work under
  `as root`. The probe now checks that the file is **ours**, that nobody else can write
  it, and that its bytes are the ones we would have sent; a foreign file is refused by
  name rather than overwritten, because replacing a file we do not own is a race we cannot
  win. The workdir is checked the same way before a request is deposited: the resident
  agent runs **any** `req-*.json` it finds without asking who wrote it, and `umask 077`
  only sets the mode of a directory it *creates* — `/tmp` and `/dev/shm` are both
  world-writable, so the path can be pre-created. A probe that cannot answer refuses
  rather than proceeds. The pushed binary is now `chmod 700` and not `chmod +x`: under a
  target umask of 0002 the latter yields 775, so any member of the SSH user's group could
  rewrite the binary about to run — found by running the guard against a container, where
  it refused shellf's own binary while every unit test passed (#391). The site's hero ran
  `shellf run --check … hosts.shellf site.shellf`, which fails twice over: `--check` was
  renamed `--dry-run` (ADR-0035), and a plan must sit in `plans/` beside `inventories/`
  (ADR-0038). It also used `service(…)`, `service-reload(…)` and `template(…)`, all
  renamed in ADR-0032, and advertised v0.3.1. `README.md` claimed the agent "vanishes,
  nothing stays installed" while its own *How it works* section said it stays resident,
  put the version at 0.1.0, said imports were "not there yet" three releases after they
  shipped, and called `file.copy` a Go builtin — it has been a def since #332.
  `docs/language.md`, which calls itself *current by definition*, documented
  `apt-install(…)` and a `-> … when err` form that the parser has never accepted (`->` is
  lexed and never parsed; `when` does not exist), and promised `set -o pipefail` in
  **every** shell block when it is a bashism deliberately kept out of POSIX blocks — a
  documented safety net that is not there under the default `sh` (#394).
- `dir.sync` no longer reports `ok.already` after deleting files. The transfer counted the
  files it **wrote**, and the agent discarded the removal list on the way back
  (`Sync` returned `n, _, err`), so a run over a converged tree holding one intruder
  removed it and announced that nothing had changed — a destructive action reported as a
  no-op, which is worse than an error since an error at least stops the plan. `~dir.sync`
  now returns the work it did, written **plus** removed. The check-mode half of this same
  defect was fixed in #384 and this one was left behind, so the dry-run told the truth
  while the real run did not — the inversion of what an operator expects. `dir.copy` is
  unaffected: it passes `delete = "false"` and removes nothing. Found by an external audit;
  the e2e coverage plan cannot produce the case, because it creates its extra file and
  delivers a tree in the same call, so the harness grew the one that can (#387). On a converged host
  `file.copy`, `dir.copy` and `dir.sync` reported `would.copied` / `would.synced` — and
  `dir.sync` contradicted its own verdict in the preview line below it (*0 file(s) would
  be transferred*). An operator who learns that `would` means "maybe" stops reading it,
  which is the whole claim of a tool whose thesis is that a run is readable before it
  happens. The information was never missing: `engine.Run` runs an instruction's guard
  *before* deciding, so a converged `~file.write` already answers `already` and
  `~dir.sync` counts without writing a byte — it was produced inside `apply`, which check
  mode never ran. ADR-0041 runs it there when it cannot act: no `observe`, and an `apply`
  holding only primitives, control flow and `return`. The alternative — give those defs an
  `observe` — is the #378 bug, deliberately reintroduced. A `shell { }` in the apply
  disqualifies it silently and keeps the conservative verdict (#380). Its `observe` asked whether the destination
  *existed*, which is true forever after the first run, so an edited source was never
  re-delivered — and the run reported `ok.already` over a stale file. Worse than a
  failure, which at least stops the plan. The `observe` is gone rather than repaired:
  `~file.write` is already idempotent by content sha256, so the def was asking a second,
  weaker version of a question the primitive answers exactly — the same duplication
  `file.template` removed in #334, and the same drift. The verdict now comes from the
  work itself, as in `dir.copy`, which is why `~file.write` returns the number of files
  it wrote (`"1"` or `"0"`) the way `~dir.sync` already did for a tree. Binary safety is
  untouched: the bytes still travel through the primitive, never through a shell
  variable. The e2e harness grew the case that catches this class — a source edited
  *between* two runs, which no fixed asset can exercise (#378).
- An error caught with `?` no longer fails the run. The plan handles it and carries on —
  that is what `?` is for (ADR-0009) — yet shellf exited 1, so `shellf run … && …` never
  succeeded for any plan using the language's own error handling, and a CI job saw a
  failure where there was none. The report now carries the caught flag the agent already
  had, and only an *uncaught* error fails the run (#356).
- `dir.owner` converges. Its `observe` read `stat -c '%U:%G'` — `covuser:deploy` — and
  compared it to the argument, which names a user: the two can only be equal when the
  caller writes `"user:group"`, so every run reported `changed` and every
  `if x.changed { … }` gated on it fired for nothing. The comparison now happens in the
  shell and accepts both forms `chown` accepts. Invisible to unit tests — a fake executor
  answers whatever the test asks — and found by running the def twice against a real
  container (#367).
- A call cycle is refused when the defs are loaded, not when they run (ADR-0030 §6, which
  asked for exactly this). The evaluator's guard fired on the target, after earlier steps
  of the plan had already acted — a partially applied host, for an error that is a writing
  mistake readable from the files. The guard stays as a backstop, and both report the same
  chain (`a -> b -> a`) so the two cannot drift. The walk follows delegations too, an edge
  a phase-only traversal would miss (#311).
- A run survives its control-channel bridge being dropped. ADR-0031 §2 promised the
  control host "reconnects, relaunches the bridge, and the dialogue resumes" — it dialled
  once, so a flaky link, an idle timeout or a killed `sshd` child was fatal to every
  remaining `~file.read` in the job. It now relaunches, bounded, and tells its own
  shutdown apart from a drop so no session is left behind. This is the property the
  socket in the agent's workdir was chosen for, and it had never held (#347).
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
