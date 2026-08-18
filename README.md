<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/wordmark-dark.svg">
    <img alt="shellf" src="docs/assets/wordmark-light.svg" width="440">
  </picture>
</p>

<p align="center"><em>Raw shell, but idempotent, previewable, and fast.</em></p>

<p align="center">
  <a href="https://github.com/haribo/shellf/actions/workflows/ci.yaml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/haribo/shellf/ci.yaml?branch=main&label=CI"></a>
  <img alt="Go" src="https://img.shields.io/github/go-mod/go-version/haribo/shellf">
  <img alt="status" src="https://img.shields.io/badge/status-experimental-e0a000">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-blue"></a>
</p>

shellf runs configuration as plain shell — but structured so every action is
**idempotent** (skip when already in the desired state), **previewable** (a
dry-run shows what *would* change, without touching anything), and **fast**. It
needs nothing installed on the target: a single static binary pushes itself over SSH and
evaluates the plan **on the target**. The pushed agent stays **resident** between jobs, so
a second run costs no round trip; after an inactivity TTL (default 2h) it removes its
workdir and its own binary and exits, leaving nothing behind.

> Status: experimental (0.4.0). Ships a **stdlib of instructions** (`apt.install`,
> `service.ensure`, `dir.ensure`, `file.*`, `ufw.*`, `docker.*`, …) written as shellf
> `def`s, plus a raw **`shell`** form; you write the plan and inventory. Instruction
> libraries can be shared: a local package by path, or a remote module pinned by tag in
> `shellf.lock`. Cross-distro and cross-arch agents are not there yet. Debian/systemd
> targets, `linux/amd64` control host.

## Install

```
CGO_ENABLED=0 go build -o shellf ./cmd/shellf
```

A static binary. The same binary is what gets pushed to targets as the agent.

## Quickstart

Describe your hosts in an **inventory** file (`hosts.shellf`):

```
defaults = { user: "root", port: "22" }

host web1 = { address: "10.0.0.1" }
host web2 = { address: "10.0.0.2" }

group web = [web1, web2]
```

Authentication uses your **ssh-agent** (`SSH_AUTH_SOCK`) by default, so an encrypted
key never leaves the agent. To pin a specific key instead, add `key: "~/.ssh/id_…"`
to `defaults` or a host (it is an optional override).

Describe what to do in a **plan** file (`plan.shellf`):

```
on web {
  apt.install("nginx")
  file.copy("/tmp/nginx.conf", "/etc/nginx/nginx.conf")
  service.ensure("nginx", "true", "true")       # running now, enabled at boot
}
```

Preview first — this touches nothing, and reports what would change:

```
shellf run --inventory hosts.shellf --dry-run plan.shellf
```

Then apply:

```
shellf run --inventory hosts.shellf plan.shellf
```

Hosts run in parallel; steps run in order per host. A re-run is idempotent: an
instruction that finds the state it wants reports `ok.already` and does nothing.

## Inventory

| Construct | Form |
|---|---|
| Defaults | `defaults = { user: "…", port: "…" }` |
| Host | `host <alias> = { address: "…", user: "…", port: "…" }` |
| Group | `group <name> = [<alias>, <alias>]` |

Omitted host fields fall back to `defaults`, then to `22` for the port. Only
`address` is required. A host may belong to several groups. `key: "…"` is an
optional field (a pinned ssh key); without it, authentication uses the ssh-agent.

A host with `local: "true"` is provisioned on the **control host itself**, with no
SSH — `host self = { local: "true" }` (no `address` needed). Same agent, plan, and
reports as a remote target; `as root` still goes through `sudo`.

## Plan

| Construct | Form |
|---|---|
| Block | `on <group-or-host> { <steps> }` — blocks run sequentially |
| Parallel | `parallel { <steps> }` — branches run concurrently on one host |

Blocks target a group (or a single host). A host that errors is dropped from
later blocks.

## Writing shellf

A worked tour. Runnable examples live under [`examples/`](examples/) — one project with
two plans, start with [`webserver.shellf`](examples/plans/webserver.shellf), then the
containerized [`blog.shellf`](examples/plans/blog.shellf) for user defs, secrets,
templates, and docker/ufw; see [`examples/README.md`](examples/README.md).

A project is laid out by type (ADR-0038): `plans/` holds what a run invokes, `defs/<pkg>/`
the reusable instructions (called `pkg.name`, no import), `assets/` the content a plan
delivers, `inventories/` the hosts.

**Variables** come from the inventory (per-host) or `--set`. Use them as bare
identifiers, or `${name}` inside strings:

```
host web = { address: "10.0.0.1", pkg: "nginx", webroot: "/var/www/app" }
```
```
on web {
  apt.install(pkg)                 # `pkg` resolves per host
  dir.ensure(webroot)
}
```

**Secrets** (ADR-0018) come from a file or an env var — never the command line
(so not in `ps`/history) and never the plan (so not committed):

```
shellf run plan.shellf --secret-file rclone_pass=./secret --secret-env db=DB_PW
```

A secret is a variable like any other (`${rclone_pass}`), but shellf **redacts
its value** (`***`) from every report, `--dry-run`, and `status`. Honest limit: the
secret still reaches the target (in the request file, `0600`, and the process
env) — root there can read it; at-rest secrecy is not yet solved.

**Control flow.** `if` takes an instruction (or a captured result); the branch is
taken on its outcome. `dir.exists` is a read-only *question*, so it stays honest
in `--dry-run`:

```
if dir.exists("/opt/app") { service.ensure("app", "true", "true") }
if !dir.exists("/opt/app") { dir.ensure("/opt/app") }   # act only when absent
```

**Capture and match outcomes.** An instruction returns a `Result`: a tagged
outcome (`ok`/`err` + a tag), plus a `changed` flag.

```
x = file.write("/etc/app.conf", cfg)
if x { restart() }               # `if x` = it succeeded; `if !x` = it failed
if x == ok.written { reload() }  # match a specific tag
if x.changed { … }               # did it actually act (not a converged skip)?
```

**Error handling.** By default the first `err` halts the host. To handle a
*specific* error, mark the instruction `?` and test it — testing `== err` without
a `?` is a compile error (the branch would be unreachable):

```
x = apt.install("nginx")?
if x == err.dbLocked { retry() } else { report() }
```

**Privilege escalation.** shellf runs as the SSH user. `as <user>` escalates a
block (via sudo/doas); many stdlib defs (`apt.install`, `service.ensure`, …) declare
`as root` themselves and escalate on their own:

```
on web {
  apt.install("nginx")           # escalates itself (intrinsic `as root`)
  as root {                      # escalate a block of generic instructions
    dir.ensure("/opt/app")
    file.write("/etc/app.conf", cfg)
  }
}
```

**Custom instructions** are `def`s written in shellf. A def has phases (`observe`
read-only, `apply` effectful) and returns an outcome. `observe` reports the
current state as a `state(...)` record; shellf compares each field to the
arguments and runs `apply` only on a mismatch — no hand-written skip. Inside a
def, a `shell {}` is a struct — read `.exit`/`.stdout`, and `if r` / `if !r` test
its success:

```
def install(pkg: str) as root {
  observe {
    return state(installed: shell { dpkg -s "$pkg" >/dev/null 2>&1 }.exit == 0)
  }
  apply {
    r = shell { apt-get install -y "$pkg" }
    if !r { return err.runtime(r) }
    return ok.installed
  }
}
```

A field with no same-named argument (like `installed`) must simply hold;
fields that match a parameter (`service.ensure` → `running`, `git.clone` → `url`) are
compared to it. See [ADR-0013](docs/adr/0013-observe-state-contract.md).

**Preview, then apply.** `--dry-run` runs only the read-only phases, never mutates,
and prints `would.<tag>` for what would change. A second real run is idempotent
(everything reads `ok.already`). `shellf status` reports the same observed state
as `current → desired`, without acting.

## Instructions

Most instructions are `def`s written in shellf and embedded in the binary; only
`shell` and five **primitives** — `~file.read`, `~file.write`, `~file.render`,
`~dir.list`, `~dir.sync` — are built in. Most are idempotent: a def that declares
`observe` skips its `apply` when the desired state already holds. Some are
**action-shaped** and always act — `service.restart`, `docker.compose-up` — because
restarting a service has no "already restarted" to observe; `--dry-run` says `would`
for those rather than pretending otherwise.

- **Packages & services** — `apt.install(pkg)` · `apt.update()` · `service.ensure(name, running, enabled)` (running/enabled are `"true"`/`"false"`; a `.timer` unit works as the name) · `service.restart(name)` · `service.reload(name)` · `systemd.daemon-reload()` · `user.group(user, group)` · `user.ensure(name, shell)`
- **Files & directories** — `file.copy(%"src", dst)` (deliver a file from the control host, binary-safe) · `file.template(%"src", dst)` (render a control-host file's `@{var}` and deliver it — `src` must be marked `%"…"`, an unmarked path is refused) · `dir.copy(%"src", dst, compare)` (deliver a control-host tree verbatim, binary-safe; sends only what differs, so a converged tree transfers nothing — `compare` defaults to size+mtime, pass `"sha256"` when a change may preserve both) · `file.write(path, content)` · `file.mode(path, mode)` · `file.replace(path, key, value)` (a `key=value` line) · `file.line(path, line)` · `file.delete(path)` · `file.download(url, dst, sha256)` · `dir.sync(%"src", dst, compare)` (same transfer, and it **removes** what the source does not have — `--dry-run` names every file it would delete) · `dir.ensure(path)` · `dir.owner(path, owner)` · `archive.extract(src, dst)` · `archive.extract-member(src, dst, member)` (one file out of a tarball) · `git.clone(url, dst)` · `git.sync(url, dst, ref)` (update to a pinned ref)
- **Questions** (read-only, deterministic in `--dry-run`) — `dir.exists(path)` · `file.exists(path)` · `http.check(url, status)` · `http.wait-for(url, timeout)` (retries until ready)
- **Validated configs** — `sudo.write(name, content)` (checked with `visudo -cf`, set 0440) · `sshd.config(name, content)` (checked with `sshd -t -f`). The check runs before anything is written, and in `--dry-run` too, so an invalid file is caught before it can lock you out
- **Firewall** — `ufw.enable()` · `ufw.default(incoming, outgoing)` · `ufw.open(port, proto)`
- **Docker** — `docker.install()` · `docker.network(name)` · `docker.compose-up(dir, build)` (`build` `"true"` rebuilds local images; always re-applies — `up -d` is idempotent) · `docker.compose-restart(dir, service)` (handler — omit `service.ensure` for the whole stack; gate it on `.changed`, e.g. after a mounted config is edited)

**Primitives** (ADR-0036) — `~` marks an engine primitive (no phases, no override), and
`%` marks a path on your machine: `~file.read(path)` reads — on your machine if the path is marked, on the target
otherwise — `~file.write(path, bytes)` writes on the target, `~file.render(%"path")`
reads a template on your machine and substitutes its `@{var}` there, and
`~dir.list(path)` lists a directory, and `~dir.sync(src, dst, delete, compare)` transfers a
tree. Only those five names may carry a `~`; anything else is a parse error, and the control
host serves only the paths the plan marked — a render included, which is why it names a file
rather than carrying one.

```
def deliver(src: str, dst: str) {
  apply { file.write(dst, ~file.render(src)) }
}
```
```
on web { deliver(%"app.conf.j2", "/etc/app.conf") }
```

Write your own — see [Writing shellf](#writing-shellf).

## Raw shell

The first-class citizen: run anything the builtins don't cover. `shell` is a
special form, not a `name(args)` call.

```
on server {
  shell docker compose up -d              # one-line: ends at the newline

  shell {                                 # block: raw, verbatim (heredocs work)
    curl -fsSL https://get.docker.com | sh
  }

  # idempotent + previewable: gate the effect on a read-only test
  if !shell { docker network inspect web } {
    shell { docker network create web }
  }
}
```

- A bare `shell` always runs (raw, like bash; not previewable).
- To make it idempotent and previewable, gate it: `if !shell { <test> } { shell { <cmd> } }`
  — the test is read-only, so `--dry-run` runs only the test, never the command.
- Escalate with `shell as root { … }` (see [Writing shellf](#writing-shellf)).
- A block ends at its balanced `}`. A lone unbalanced `}` in a string ends it
  early — use a heredoc or the one-line form.

## CLI

```
shellf run    --inventory <hosts.shellf> [flags] <plan.shellf>
shellf status --inventory <hosts.shellf> [flags] <plan.shellf>
shellf clean  --inventory <hosts.shellf> [flags] [target...]
shellf version
```

`run` applies, `status` reports observed state without acting, `clean` kills the resident
agents and wipes shellf's files from the targets.

| Flag | Meaning |
|---|---|
| `--inventory <file>` | inventory file (required) |
| `--dry-run` | decide and preview, never mutate |
| `--vars <file>` | global `name = value` bindings |
| `--set k=v` | override one variable (repeatable); wins over `--vars` |
| `--secret-file n=path` | secret read from a file (repeatable); redacted in all output |
| `--secret-env n=VAR` | secret read from an environment variable (repeatable) |
| `--known-hosts <file>` | host-key file (default `~/.ssh/known_hosts`) |
| `--insecure` | skip host-key verification (dev only) |
| `--agent-ttl <dur>` | resident agent inactivity TTL before it self-erases (default 2h) |

## How it works

![Isometric view: a control terminal pushes one static binary over SSH arcs to three servers, where it runs as an agent executing the plan locally; the finished agent dissolves into pixels, leaving the machine untouched.](docs/assets/how-it-works.png)

*One static binary. The control host orchestrates; pushed over SSH, it re-runs as a resident agent that executes the plan locally on each target, and exits after its inactivity TTL.*

Two planes, kept separate. The **orchestration** plane (control host) decides
which hosts run what, in what order. The **execution** plane is the agent: the
binary is pushed over SSH (cached by hash, skipped on re-runs), evaluates the
plan **on the target**, and stays resident between jobs — then self-erases after
an idle TTL, leaving nothing after a reboot. Guards are read-only, which is what
makes `--dry-run` honest across a whole fleet.

Shell values are passed to the target via the environment, never concatenated
into commands — so a value like `nginx; rm -rf /` cannot inject a command.
