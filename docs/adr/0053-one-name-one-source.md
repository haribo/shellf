# ADR 0053 — A name resolves against one source

## Status

Active. **Supersedes [ADR-0003](0003-variable-scoping.md) §3 and §5**, which put the
inventory in the precedence chain of a bare reference and split resolution by the *kind* of
reference. ADR-0003 keeps its body and its other sections (§1 binding syntax, §2
immutability, §4 the two reference forms) and carries a link forward.

**Amends [ADR-0052](0052-per-host-interpolation.md)**, which is not reversed: its
`inventory.` prefix is what makes this decision possible. What changes is the prefix's
standing — it stops being an addition beside the bare form and becomes the only way in.

## Context

The same name yields two values in one plan, decided by nothing but braces. Measured on the
built binary — plan binding `domain = "FROM-PLAN"`, inventory field `domain: "FROM-INVENTORY"`:

| written | value |
| --- | --- |
| `domain` | `FROM-INVENTORY` |
| `"${domain}"` | `FROM-PLAN` |
| `"${inventory.domain}"` | `FROM-INVENTORY` |

Nothing reports the disagreement. A reader who adds braces for legibility changes which
machine the plan deploys to.

### Each half is correct; together they are not

- `${name}` is substituted **at parse**, against globals, plan bindings, loop variables and
  `--set` (ADR-0003 §5). The plan is parsed **once for N hosts**, so a parse-time
  substitution can hold exactly one value — it cannot hold a per-host one.
- A **bare** identifier ships unresolved and is resolved **per host**, against
  `base < host.Vars < set`. `host.Vars` overwrites the plan binding, silently.

ADR-0003 §4 presents the two forms as one variable written two ways — bare in argument
position, braced inside a string. That is what a reader understands, and it is the right
model. §5 then gave them different sources, which is where the model broke.

Note what the problem is **not**. Inventory-over-plan precedence is defensible and was
decided deliberately. The defect is that a name has two values at once, not which one wins.

## Decision

### 1. Bare and braced resolve identically, against the plan

| form | resolved | source |
| --- | --- | --- |
| `domain` | at parse | `--vars`, plan bindings, loop variables, `--set` |
| `"${domain}"` | at parse | the same, exactly |
| `"${inventory.domain}"` | per host, at orchestration | this host's inventory entry, and nothing else |

`host.Vars` leaves bare-reference resolution. A bare identifier and `${name}` are the same
variable in two positions, which is what ADR-0003 §4 always said.

### 2. The inventory is reached through the prefix, and nowhere else

One door, named at the call site. Reading a plan no longer requires knowing what the
inventory contains to know where a value comes from — the line says it.

The precedence chain of ADR-0003 §3 loses its `inventory (per-host)` level and becomes
`--vars < plan binding < --set`, all plan-side. `--set` keeps overriding plan variables and
still does not override `${inventory.…}` (ADR-0052 §2): the source is written, so nothing
silently redirects it.

### 3. This breaks plans, on purpose and loudly

A plan reading a per-host variable bare stops working. It **fails**; it does not change
meaning:

```
box: undefined variable "domain"
```

An unresolved bare reference is an error, not an empty string. A plan that used to take
`domain` from the inventory halts instead of deploying with the wrong value. That property
is the reason this change is acceptable at all — a breaking change that breaks hard is safe,
and one that shifts meaning in silence is the defect being fixed.

## Consequences

- **`--set` and `--vars` become purely plan-side.** Retargeting a deployment means pointing
  at another inventory — which is what an inventory is for.
- **A per-host value can no longer be passed to a def without naming its source.**
  `apt.install(pkg)` becomes `apt.install("${inventory.pkg}")`. Longer, and it says where
  the value comes from; the shipped examples carry four such call sites.
- **`examples/inventories/inventory.shellf` documents the old rule** in its header — free-form
  fields described as variables "the plans read" — and stops being true.
- **`docs/language.md` §variables must state the table in §1**, since the split by kind is
  what a reader currently learns.
- **The `for` loop is unaffected.** It unrolls at parse and binds `${var}` into the same
  table (ADR-0017); it was never a third resolution context.

## Rejected alternatives

### Warn on collision, keep both behaviours

Report when a name exists both as a plan binding and as a host field. Cheap, no breaking
change — and it **documents the incoherence instead of removing it**. The two forms still
mean different things; the plan is still wrong; the reader is now told so at every run. A
warning is the right tool for an ambiguity that must survive, not for one that can be
deleted.

### Make `${name}` resolve per host

Align the other way: braces become per-host too. Rejected in ADR-0052 already, and the
reason stands — resolution becomes **implicit**, so reading one line of a plan requires
knowing the inventory. It also cannot work as stated: the plan is parsed once for N hosts,
so `${name}` would have to stop being a parse-time substitution entirely, which is what
makes control-host templating and `%"path"` resolvable at all.

### Keep the inventory in the chain and forbid a plan binding of the same name

Make the collision a parse error rather than a silent overwrite. It removes the surprise but
keeps two sources for one name — and it forbids a legitimate plan-side default that an
inventory may or may not override, which is a normal thing to want.
