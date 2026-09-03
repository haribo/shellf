# ADR 0054 — `${inventory.<host>.<field>}` reads another host's declared values

## Status

Active. **Supersedes the exclusion in [ADR-0052](0052-per-host-interpolation.md) § Deliberately
out of scope**, which recorded this form as excluded "pending a real deployment that needs
it". The rest of ADR-0052 stands: the prefix, what a host exposes, and `key` being refused.

## Context

A plan cannot read another host's inventory entry, so an address that exists once in reality
has to be written twice:

```
host db  = { address: "10.0.0.40" }
host svc = { db_address: "10.0.0.40" }   # the same address, copied
```

The two-host dogfood (#542) needed exactly this: a service reaching a database on another
machine. `examples/plans/fleet.shellf` carries the duplication today.

### The measurement, and the correction to it

The issue that opened this (#547) argued the cost grows with the number of consumers — ten
services reading one database writing the address eleven times. **That is wrong**, and it was
found by testing rather than reading: an inventory's `defaults` block already carries
free-form variables, merged with each host's own (`internal/inventory/inventory.go`). So:

```
defaults = { db_address: "10.0.0.40" }
host db   = { address: "10.0.0.40" }
host svc1 = { }   # ten of these, none repeating the address
```

Two writes, not eleven, whatever the number of consumers. Verified against the built binary.

**The case for this ADR is therefore not magnitude, it is arity.** The right number is one.
Two copies of a fact, with nothing keeping them equal, is the defect
[ADR-0053](0053-one-name-one-source.md) was written to remove for variable names — the same
shape, one level up. A `defaults` entry does not fix it; it makes the second copy tidier.

### Why ADR-0052's objections do not survive

That ADR named three open questions. All three rest on an assumption that does not hold:

| question it raised | why it dissolves |
| --- | --- |
| resolution order between hosts | **An inventory holds no expressions.** ADR-0052 itself rejected derived fields for that reason, so a field can never reference another. Reading `db.address` evaluates nothing — it is a lookup in a table already parsed. |
| hosts outside the `on` block | The whole inventory is loaded before any host is contacted. Reading the entry of a machine this run does not deploy is as defined as reading one it does. |
| depending on a machine not deployed | An operator's concern, not a resolution one. The declared address of an undeployed host is still its declared address; whether anything answers there is what a health check is for. |

The objections were sound against a *dynamic* reading. Against static data they have no
content.

## Decision

### 1. `${inventory.<host>.<field>}` reads that host's declared value

```
file.template(%"app.env.tmpl", "/etc/app.env")   # PGHOST=~{inventory.db.address}
http.check("http://${inventory.db.address}:5432/", "000")
```

Resolved per host at orchestration, like `${inventory.<field>}`. The one-segment form keeps
its meaning — **this** host — and is unchanged.

### 2. It reads the *resolved* host, defaults included

`${inventory.db.address}` sees what `db` resolves to after `defaults` are merged, not what
its literal block spells. Anything else would make the same field mean two things depending
on who asks.

### 3. Exposure is ADR-0052's table, unchanged

`name`, `address`, `user`, `port` and every free-form field. **`key` stays refused** — the
path to a private key does not become readable because another host asks for it. Refusing it
here matters more, not less: a plan reading `${inventory.db.key}` is asking for a credential
belonging to a machine it is not even deploying.

### 4. An unknown host or field is an error at orchestration, naming both

```
box: undefined host "bd" in ${inventory.bd.address}
box: host "db" declares no field "adress"
```

The prefix and the shape are checked at parse, as ADR-0052 already does; only the names are
late, which is already true of every per-host reference (ADR-0003 §4).

Errors, never an empty string. A plan pointing at a machine that does not exist must halt
before it applies anything — the property that made ADR-0053's breaking change safe.

## Consequences

- **`examples/plans/fleet.shellf` loses its duplicated address**, and its inventory stops
  carrying `db_address` at all.
- **`proto` must carry a two-segment reference**, and must still not depend on `lang` to
  expand it — the constraint ADR-0052 already met for one segment.
- **A group name is not a host.** `${inventory.web.address}` where `web` is a group has no
  single answer; it is an error naming the group, not a silent first-member pick.
- **This does not make the inventory an addressing table for the world.** It reads what the
  inventory declares. A machine absent from it stays unreachable by name, which is the point
  of having one.

## Rejected alternatives

### Keep the exclusion, use `defaults`

What this ADR was nearly closed as. It removes the repetition across consumers and leaves
**two** copies of one address — the second in `defaults`, further from the host it describes
than the first. Tidier duplication is still duplication, and the failure it permits is
silent: nothing compares the two.

### Let an inventory field reference another

`host svc = { db_address: "${db.address}" }`. Solves it inside the inventory and puts an
expression language where there is none — rejected by ADR-0052 for that reason, and this ADR
depends on that rejection holding, since it is what makes cross-host reads acyclic.

### A `link` or `depends_on` declaration between hosts

Model the relationship, then read through it. It buys ordering guarantees this ADR does not
provide — and nothing has asked for those. A deployment that needs `db` provisioned before
`svc` writes the `on` blocks in that order today, which is what `fleet.shellf` does.
