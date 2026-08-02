# ADR 0017 — `for` loops: parse-time unrolling over a literal list

## Status

Active. First iteration construct in the language.

## Context

Real plans repeat: open N firewall ports, drop N files, chmod a set of paths. A
deployment dogfood flagged hand-unrolling as the single biggest ergonomics loss:

```
ufw.open("80", "tcp")
ufw.open("443", "tcp")
```

We want a loop, without dragging in a runtime list type, a wire format for lists,
or an agent-side interpreter — none of which the concrete need (fixed lists known
when the plan is written) requires.

## Decision

### 1. `for <var> in [<str>, …] { <body> }` in `on` blocks

```
on host {
    for port in ["80", "443"] {
        ufw.open("${port}", "tcp")
    }
    for svc in ["traefik", "app"] {
        file-mode("/opt/${svc}/run", "755")
    }
}
```

The list is a **literal** of strings (each interpolated at parse time, like any
string). v1 scopes loops to `on` blocks; def bodies (observe/apply) come later.

### 2. The loop variable is referenced as `${var}` (interpolation)

`${port}` — not a bare `port`. A bare identifier argument is already a *per-host
ref* (resolved late, after the unroll, on the wrong value); `${var}` resolves at
unroll time and works **everywhere** a string does — as a whole argument and
**inside** one (`/opt/${svc}/run`), which the "N files" case needs.

### 3. Parse-time unrolling — no runtime loop, no list type

The list is known at parse, so the parser **unrolls** the loop: it captures the
body once and re-parses it once per item with `${var}` bound to that item,
appending the resulting steps. There is **no** list value, **no** proto/agent
change, **no** iteration on the target — the plan reaches orchestration already
flat, exactly as if the user had written the copies by hand.

### 4. Body contents

The body is a normal block: instructions, `if`, `shell`, nested blocks — all
unrolled recursively (they are just re-parsed per item). Nested `for` is allowed
(each unrolls in turn).

## Rejected alternatives

- **A runtime list type + agent-side loop.** A wire format for lists, an
  interpreter on the target, and a new value kind — none needed for literal
  lists known at authoring time. Parse-time unrolling is strictly simpler.
- **A bare-identifier loop var** (`ufw.open(port, …)`). Collides with per-host
  refs — the bare arg is resolved after the unroll, when the var no longer holds
  the item. `${var}` sidesteps it and is more capable (works inside strings).
- **Glob / numeric range** (`for f in *.conf`, `for i in 1..10`). Target-side
  file iteration and ranges are a separate, later concern.
- **Loops in def bodies.** Interacts with observe/apply/status semantics;
  deferred until a concrete need appears. Plans first.

## Consequences

- Repetition collapses: one `for` over a literal list instead of N hand-written
  copies — the flagged ergonomics win, at zero runtime cost.
- The loop var flows through the existing `${}` interpolation, reusing the
  parser's variable resolution; no new scoping machinery.
- The body is captured as raw balanced braces (like a `shell {…}` block), so it
  inherits the same caveat — a lone unbalanced `}` inside a string ends the block
  early. Documented.
- Because unrolling is parse-time, `--check` and `status` see the flat steps and
  report each iteration individually — honest, no special-casing.
- Follow-ons if needed: glob/range iteration, list-valued variables, loops in def
  bodies — each additive on top of this.
