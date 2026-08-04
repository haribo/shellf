<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/wordmark-dark.svg">
    <img alt="shellf" src="docs/assets/wordmark-light.svg" width="440">
  </picture>
</p>

<p align="center"><em>Raw shell, but idempotent, previewable, and fast.</em></p>

<p align="center">
  <a href="https://github.com/haribo/shellf/actions/workflows/ci.yaml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/haribo/shellf/ci.yaml?branch=develop&label=CI"></a>
  <img alt="Go" src="https://img.shields.io/github/go-mod/go-version/haribo/shellf">
  <img alt="status" src="https://img.shields.io/badge/status-experimental-e0a000">
</p>

shellf runs configuration as plain shell — but structured so every action is
**idempotent** (skip when already in the desired state), **previewable** (a
dry-run shows what *would* change, without touching anything), and **fast**. It
is agentless: a single static binary pushes an **ephemeral agent** over SSH that
evaluates the plan **on the target**, then vanishes. Nothing stays installed.

> Status: experimental (0.1.0). Ships a **stdlib of instructions** (`apt.install`,
> `service`, `dir-ensure`, `file-*`, `ufw.*`, `docker.*`, …) written as shellf
> `def`s, plus `file-copy` and a raw **`shell`** form; you write the plan and
> inventory. User-supplied instruction libraries (imports), cross-distro, and
> cross-arch agents are not there yet. Debian/systemd targets, `linux/amd64`
> control host.

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
  file-copy("/tmp/nginx.conf", "/etc/nginx/nginx.conf")
  service("nginx", "true", "true")       # running now, enabled at boot
}
```

Preview first — this touches nothing, and reports what would change:

```
shellf run --inventory hosts.shellf --check plan.shellf
```

Then apply:

```
shellf run --inventory hosts.shellf plan.shellf
```

Hosts run in parallel; steps run in order per host. A re-run is idempotent
(`ok.alreadyInstalled`, `ok.alreadyConverged`, …).

## Inventory

| Construct | Form |
|---|---|
| Defaults | `defaults = { user: "…", port: "…" }` |
| Host | `host <alias> = { address: "…", user: "…", port: "…" }` |
| Group | `group <name> = [<alias>, <alias>]` |

Omitted host fields fall back to `defaults`, then to `22` for the port. Only
`address` is required. A host may belong to several groups. `key: "…"` is an
optional field (a pinned ssh key); without it, authentication uses the ssh-agent.

## Plan

| Construct | Form |
|---|---|
| Block | `on <group-or-host> { <steps> }` — blocks run sequentially |
| Parallel | `parallel { <steps> }` — branches run concurrently on one host |

Blocks target a group (or a single host). A host that errors is dropped from
later blocks.

## Writing shellf

A worked tour. The full example is in [`examples/webserver/`](examples/webserver/).

**Variables** come from the inventory (per-host) or `--set`. Use them as bare
identifiers, or `${name}` inside strings:

```
host web = { address: "10.0.0.1", pkg: "nginx", webroot: "/var/www/app" }
```
```
on web {
  apt.install(pkg)                 # `pkg` resolves per host
  dir-ensure(webroot)
}
```

**Secrets** (ADR-0018) come from a file or an env var — never the command line
(so not in `ps`/history) and never the plan (so not committed):

```
shellf run plan.shellf --secret-file rclone_pass=./secret --secret-env db=DB_PW
```

A secret is a variable like any other (`${rclone_pass}`), but shellf **redacts
its value** (`***`) from every report, `--check`, and `status`. Honest limit: the
secret still reaches the target (in the request file, `0600`, and the process
env) — root there can read it; at-rest secrecy is not yet solved.

**Control flow.** `if` takes an instruction (or a captured result); the branch is
taken on its outcome. `dir-exists` is a read-only *question*, so it stays honest
in `--check`:

```
if dir-exists("/opt/app") { service("app", "true", "true") }
if !dir-exists("/opt/app") { dir-ensure("/opt/app") }   # act only when absent
```

**Capture and match outcomes.** An instruction returns a `Result`: a tagged
outcome (`ok`/`err` + a tag), plus a `changed` flag.

```
x = file-write("/etc/app.conf", cfg)
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
block (via sudo/doas); many stdlib defs (`apt.install`, `service`, …) declare
`as root` themselves and escalate on their own:

```
on web {
  apt.install("nginx")           # escalates itself (intrinsic `as root`)
  as root {                      # escalate a block of generic instructions
    dir-ensure("/opt/app")
    file-write("/etc/app.conf", cfg)
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
fields that match a parameter (`service` → `running`, `git-clone` → `url`) are
compared to it. See [ADR-0013](docs/adr/0013-observe-state-contract.md).

**Preview, then apply.** `--check` runs only the read-only phases, never mutates,
and prints `would.<tag>` for what would change. A second real run is idempotent
(everything reads `ok.already`). `shellf status` reports the same observed state
as `current → desired`, without acting.

## Instructions

Most instructions are `def`s written in shellf and embedded in the binary; only
`shell` and `file-copy` are Go builtins. All are idempotent (`observe` skips
`apply` when the desired state already holds).

- **Packages & services** — `apt.install(pkg)` · `apt.update()` · `service(name, running, enabled)` (running/enabled are `"true"`/`"false"`; a `.timer` unit works as the name) · `service-restart(name)` · `service-reload(name)` · `systemd-daemon-reload()` · `user-group(user, group)` · `user-ensure(name, shell)`
- **Files & directories** — `file-copy(src, dst)` (target-side copy) · `template(src, dst)` (render a control-host file's `@{var}` and deliver it) · `file-write(path, content)` · `file-mode(path, mode)` · `file-replace(path, key, value)` (a `key=value` line) · `file-line(path, line)` · `file-delete(path)` · `file-download(url, dst, sha256)` · `dir-ensure(path)` · `dir-owner(path, owner)` · `archive-extract(src, dst)` · `git-clone(url, dst)` · `git-sync(url, dst, ref)` (update to a pinned ref)
- **Questions** (read-only, deterministic in `--check`) — `dir-exists(path)` · `file-exists(path)` · `http-check(url, status)` · `wait-for(url, timeout)` (retries until ready)
- **Firewall** — `ufw.enable()` · `ufw.default(incoming, outgoing)` · `ufw.open(port, proto)`
- **Docker** — `docker.install()` · `docker.network(name)` · `docker.compose-up(dir, build)` (`build` `"true"` rebuilds local images; always re-applies — `up -d` is idempotent)

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
  — the test is read-only, so `--check` runs only the test, never the command.
- Escalate with `shell as root { … }` (see [Writing shellf](#writing-shellf)).
- A block ends at its balanced `}`. A lone unbalanced `}` in a string ends it
  early — use a heredoc or the one-line form.

## CLI

```
shellf run --inventory <hosts.shellf> [flags] <plan.shellf>
```

| Flag | Meaning |
|---|---|
| `--inventory <file>` | inventory file (required) |
| `--check` | dry-run: decide and preview, never mutate |
| `--known-hosts <file>` | host-key file (default `~/.ssh/known_hosts`) |
| `--insecure` | skip host-key verification (dev only) |

## How it works

![Isometric view: a control terminal pushes one static binary over SSH arcs to three servers, where it runs as an agent executing the plan locally; the finished agent dissolves into pixels, leaving the machine untouched.](docs/assets/how-it-works.png)

*One static binary. The control host orchestrates; pushed over SSH, it re-runs as an ephemeral agent that executes the plan locally on each target — then vanishes.*

Two planes, kept separate. The **orchestration** plane (control host) decides
which hosts run what, in what order. The **execution** plane is the agent: the
binary is pushed over SSH (cached by hash, skipped on re-runs), evaluates the
plan **on the target**, and stays resident between jobs — then self-erases after
an idle TTL, leaving nothing after a reboot. Guards are read-only, which is what
makes `--check` honest across a whole fleet.

Shell values are passed to the target via the environment, never concatenated
into commands — so a value like `nginx; rm -rf /` cannot inject a command.
