# Inventory

The inventory declares **which hosts exist** and **how to reach them**. It is part
of the orchestration plane (targets), kept separate from execution logic (`def`).

Expressed in the shellf language itself — no separate YAML/TOML. A host is a
record, a group is a list.

## Host

A `host` binds a logical alias to connection coordinates. The alias is distinct
from the network address.

| Field | Required | Meaning |
|---|---|---|
| `address` | yes | network endpoint (IP or DNS) |
| `user` | no | ssh user; falls back to `defaults.user` |
| `port` | no | ssh port; falls back to `defaults.port`, then `22` |
| `key` | no | ssh identity file; falls back to `defaults.key` |

```
host web1 = { address: "10.0.0.1" }
host db1  = { address: "10.0.0.9", user: "root" }
```

Connection coordinates only — no business values (see Out of scope).

## Defaults

An optional `defaults` record supplies field values for hosts that omit them.
Precedence is exactly two levels: a host field overrides the matching
`defaults` field. No group-level defaults, no CLI merge.

```
defaults = { user: "deploy", port: 22 }

host web1 = { address: "10.0.0.1" }               // user/port from defaults
host db1  = { address: "10.0.0.9", user: "root" } // user overridden, port from defaults
```

Authentication is agent-first (ADR-0026): shellf uses the ssh-agent
(`SSH_AUTH_SOCK`) unless a `key:` is given. `key: "~/.ssh/id_…"` is an optional
override — a pinned key file, tried before the agent — usable in `defaults` or on a
host.

## Group

A `group` is a named list of hosts. A host may belong to several groups.

```
group web = [web1, web2]
group all = [web1, web2, db1]
```

Groups are the unit of assignment (which hosts run a `def`) — covered separately.

## Out of scope

**Per-host business variables** (Ansible `host_vars` / `group_vars`). Excluded by
design: multi-source variable precedence is a debugging pit. Business values are
passed as **explicit `def` arguments**, never resolved implicitly from the host.

> **Superseded.** A host does carry free-form variables today, and a plan reads them —
> through a prefix that names where the value comes from, which is what this exclusion was
> protecting against. [ADR-0052](../adr/0052-per-host-interpolation.md) added
> `${inventory.<field>}` for the host a step runs on;
> [ADR-0054](../adr/0054-cross-host-inventory-reads.md) added
> `${inventory.<host>.<field>}` for another host's, because an address that exists once in
> reality was being written twice. [ADR-0053](../adr/0053-one-name-one-source.md) is what
> makes it safe: one name resolves against one source, so the pit this paragraph describes
> — the same name meaning two things depending on how it is written — is closed by rule
> rather than by abstinence. `key` stays refused: it is the path to a private key.
