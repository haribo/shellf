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
defaults = { user: "deploy", port: 22, key: "~/.ssh/id_ed25519" }

host web1 = { address: "10.0.0.1" }               // user/port/key from defaults
host db1  = { address: "10.0.0.9", user: "root" } // user overridden, rest from defaults
```

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
