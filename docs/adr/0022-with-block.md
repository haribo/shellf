# ADR 0022 — `with { … }`: a per-call variable override

## Status

Active. A first-class capability under [ADR-0020](0020-design-for-the-user-base.md)
(per-call override is attested across config-management tools).

## Context

Templating a file, running a def, or a raw `shell` all resolve `${var}`/`@{var}`
from the ambient variable scope (globals, per-host). Ambient-only is an
anti-pattern: a call's inputs are invisible at the call site — to know which
variables a `template("nginx.conf", …)` reads, you must open the file. And the
same instruction cannot be called twice with different values in one scope
without abusing a loop or a per-host var.

Established tools give a call explicit, local inputs (Ansible's per-task `vars:`,
a role's parameters). shellf should too.

## Decision

### 1. `<call> with { k = <value>, … }`

Any instruction call — a def, `shell`, `template`, or a builtin — may be followed
by a `with { … }` block of `k = value` bindings that add or override variables
**for that call only**:

```
template("nginx.conf", "/etc/nginx/a.conf") with { port = "8080", root = "/srv/a" }
template("nginx.conf", "/etc/nginx/b.conf") with { port = "8081", root = "/srv/b" }

apt.install("nginx") with { version = "1.24" }

shell { echo "@{msg}" } with { msg = "hi" }
```

The bindings do not leak beyond the call.

### 2. Values are interpolated at parse (global vars)

A `with` value is a string, interpolated with the global variables (`${var}`) at
parse — like a plan binding. It is concrete when the call is built. (Per-host
values in a `with` are not in this ADR; per-host scope arrives with the per-host
template ADR.)

### 3. Precedence: `with` wins for that call

Within a call, a `with` binding overrides a same-named per-host var and global
var. It is the most specific, most local scope — so it wins.

### 4. Mechanism

The bindings ride in the step (`With`) and are injected where the call resolves
variables: a def's shell environment, a raw `shell`'s environment, and a
template's render scope. No new value type, no wire change beyond the extra map.

## Rejected alternatives

- **Ambient-only (status quo).** A call's inputs stay implicit and un-reusable —
  the very problem this fixes.
- **A general list/map value type.** `with { … }` is a call-local binding block,
  not a first-class map — it reuses the `k = value` binding syntax. A real map
  type, if ever needed, is a separate decision.
- **Per-host values inside `with` in v1.** Kept parse-resolved (global) here;
  per-host variable scope is the per-host template ADR's concern. They compose.

## Consequences

- A call's inputs can be **explicit and local**, and an instruction is reusable
  with different values in one scope — `template(…) with { … }` twice, no loop.
- `with` is general (def / `shell` / `template` / builtin), one mechanism.
- The step gains a `With` map; the evaluator merges it over the call's params
  (for a def) or into the environment (for a `shell`), and the CLI merges it over
  a template's render scope. Precedence: `with` > per-host > global.
