# ADR 0052 — `${inventory.name}` interpolates a host's own values

## Status

Active, **amended by [ADR-0053](0053-one-name-one-source.md)**: the prefix below is no
longer one way among two to reach the inventory, it is the only one. The decision itself is
unchanged and ADR-0053 depends on it.

**Extends [ADR-0003](0003-variable-scoping.md) §4**, which decided `${…}` is
resolved at parse and therefore global-only. That decision is unchanged: this ADR adds a
second, explicitly named form rather than altering the first. ADR-0003 keeps its body and
carries a link forward.

## Context

A per-host inventory variable cannot go inside a string:

```
http.check("https://${domain}/healthz", "200")
→ deploy.shellf: 90:19: undefined variable "domain" in interpolation
```

A **bare** identifier does resolve per host — `http.check(healthz, "200")` works — but only
as a whole argument, and the language has no concatenation. A value derived from a host's
own attributes therefore cannot be expressed.

### What it costs, measured

The #490 dogfood hit it on its first health check, and the workaround is in the shipped
example:

```
host edge = {
    domain:  "app.example.test",
    healthz: "https://app.example.test/healthz",   # the domain, written twice
}
```

Every derived value then needs its own field — the health URL, a backup path, a certificate
name — and the copies drift the first time a domain changes. On one host it is untidy; on
twenty it is a maintenance defect. Composing a URL from a host's domain is what a per-host
variable is *for*.

## Decision

### 1. `${inventory.<field>}` reads this host's value, resolved per host

```
http.check("https://${inventory.domain}/healthz", "200")
file.write("/etc/hostname", "${inventory.name}")
```

The text is fixed at parse; the substitution happens per host at orchestration, like a bare
reference. `${plain}` is untouched — global, resolved at parse.

The form is **additive**: nothing that exists today changes meaning.

| form | resolved | scope |
| --- | --- | --- |
| `domain` (bare argument) | per host, at orchestration | full ADR-0003 §3 precedence |
| `"${domain}"` | at parse | globals only |
| `"${inventory.domain}"` | per host, at orchestration | **this host's inventory entry, and nothing else** |

### 2. The prefix names the source, so nothing overrides it

`--set domain=x` does **not** override `${inventory.domain}`. The whole point of writing the
source is that the value comes from there; a flag that silently redirected it would put the
ambiguity back. `--set` keeps overriding `${domain}` and bare references, where the
precedence chain is the feature.

To deploy with different values, point at a different inventory — which is what an inventory
is.

### 3. What a host exposes

| field | exposed | why |
| --- | --- | --- |
| `name` | yes | the alias as written in the inventory (`host web = …` → `web`). Not a field of the block but its identity, and the one most often wanted: hostname, a Traefik label, a per-machine backup path |
| `address` | yes | the case with no DNS: `http://${inventory.address}:8080/` |
| free-form fields | yes | `domain`, `acme_email`, `repo` — the case this ADR exists for |
| `user`, `port` | yes | consistent, though no use case is known. Allowing them costs nothing; refusing them would need a reason |
| `key` | **no** | the path to a private key. No legitimate file content needs it, and writing it into a rendered template is a mistake worth refusing at parse, with a message that says so |

`interpreter` and `local` are transport mechanics (ADR-0012, ADR-0027), not host data, and
stay out.

### 4. A collision stops being possible

`domain` declared in a plan and `${inventory.domain}` are different names. Today
`mergeVars` overwrites silently — a plan binding loses to the inventory with no report —
and this form removes the case rather than warning about it.

That silence is a defect in its own right: it still applies to bare references and
`${plain}`. This ADR does not close it — [ADR-0053](0053-one-name-one-source.md) does, by
taking the inventory out of a bare reference's sources altogether.

*(This paragraph originally claimed the defect was "tracked separately". It was not: no issue
existed until #540. Corrected rather than removed — a doc that points at tracking which does
not exist is worse than one that says nothing.)*

## Consequences

- **An unknown field errors at orchestration, naming the host and the field.** The prefix
  itself is checked at parse, so `${inventroy.domain}` is caught before any connection —
  only the field name is late, which is already true of bare references (ADR-0003 §4).
- **`proto.Step` must carry a residual template**, not only a variable name: `Refs` is
  `map[argName]varName` today. And `proto` must not grow a dependency on `lang` to expand
  it.
- **Triple-quoted strings stay raw** (ADR-0003 §4). Their `${VAR}` belongs to compose, YAML
  or a shell here-doc. Per-host values reach file content through `file.template` and `~{…}`,
  which is what that form is for.
- **`docs/language.md:233` is wrong after this** — it states the limitation as final — and
  the shipped example loses its duplicated domain.

## Deliberately out of scope

**Reading another host's values**, `${inventory.web.address}`, so a front end can learn its
backend's address. It is genuinely useful in a multi-host deployment and it opens a set of
questions this ADR does not answer: resolution order between hosts, hosts outside the `on`
block, and what a plan means when it depends on a machine it is not deploying. Recorded as
excluded rather than forgotten; it comes back with its own ADR if a real deployment needs it.

## Rejected alternatives

### Make `${…}` resolve per host when the name is unknown at parse

No new syntax: an interpolation the control host cannot resolve becomes a per-host
reference. Rejected — it makes the resolution schedule **implicit**, so reading a line of a
plan requires knowing what the inventory contains. It also leaves the silent-collision case
untouched, where the prefix removes it. The explicit form costs eleven characters and
answers both.

### A new sigil, e.g. `%{domain}`

`%` is the control-host marker, `~` is template placeholders, `@` was retired by
[ADR-0049](0049-template-delimiter-tilde.md) for being confusing. A third interpolation
sigil in a language whose thesis is that it is small, to say something a prefix already
says.

### Derived fields in the inventory

Let a host field reference another. It removes the duplication without touching the plan
language — and puts an expression language in the inventory, where there is none. It also
does not help a plan composing a value it has no reason to store.
