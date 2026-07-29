# shellf

Raw shell, but idempotent, previewable, and fast.

shellf runs configuration as plain shell — but structured so every action is
**idempotent** (skip when already in the desired state), **previewable** (a
dry-run shows what *would* change, without touching anything), and **fast**. It
is agentless: a single static binary pushes an **ephemeral agent** over SSH that
evaluates the plan **on the target**, then vanishes. Nothing stays installed.

> Status: experimental (0.1.0 in progress). Ships **builtin instructions**
> (`apt-install`, `file-copy`, `service`); you write the plan and inventory.
> Custom instructions, cross-distro, and cross-arch agents are not there yet.
> Debian/systemd targets, `linux/amd64` control host.

## Install

```
CGO_ENABLED=0 go build -o shellf ./cmd/shellf
```

A static binary. The same binary is what gets pushed to targets as the agent.

## Quickstart

Describe your hosts in an **inventory** file (`hosts.shellf`):

```
defaults = { user: "root", port: "22", key: "~/.ssh/id_ed25519" }

host web1 = { address: "10.0.0.1" }
host web2 = { address: "10.0.0.2" }

group web = [web1, web2]
```

Describe what to do in a **plan** file (`plan.shellf`):

```
on web {
  apt-install("nginx")
  file-copy("/tmp/nginx.conf", "/etc/nginx/nginx.conf")
  service("nginx", true, true)          # running now, enabled at boot
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
| Defaults | `defaults = { user: "…", port: "…", key: "…" }` |
| Host | `host <alias> = { address: "…", user: "…", port: "…", key: "…" }` |
| Group | `group <name> = [<alias>, <alias>]` |

Omitted host fields fall back to `defaults`, then to `22` for the port. Only
`address` is required. A host may belong to several groups.

## Plan

| Construct | Form |
|---|---|
| Block | `on <group-or-host> { <steps> }` — blocks run sequentially |
| Parallel | `parallel { <steps> }` — branches run concurrently on one host |

Blocks target a group (or a single host). A host that errors is dropped from
later blocks.

## Builtin instructions

| Instruction | Call | Guard (idempotence) |
|---|---|---|
| `apt-install` | `apt-install("nginx")` | package already installed |
| `file-copy` | `file-copy("src", "dst")` | contents already match (check shows the diff) |
| `service` | `service("nginx", true, true)` | already running / enabled as desired |

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

Two planes, kept separate. The **orchestration** plane (control host) decides
which hosts run what, in what order. The **execution** plane is the ephemeral
agent: the binary is pushed over SSH, re-invoked on the target, runs the plan
there, and is removed. Guards are read-only, which is what makes `--check`
honest across a whole fleet.

Shell values are passed to the target via the environment, never concatenated
into commands — so a value like `nginx; rm -rf /` cannot inject a command.
