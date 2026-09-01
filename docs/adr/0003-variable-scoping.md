# ADR 0003 — Variable scoping

## Status

Active, except §3 and §5.

- **§3 (precedence) and §5 (resolution split by kind) are superseded by
  [ADR-0053](0053-one-name-one-source.md)**: a bare reference and `${name}` resolved against
  different sources, so one name held two values. The inventory leaves the chain and is
  reached through the `inventory.` prefix instead.
- **§4 is extended by [ADR-0052](0052-per-host-interpolation.md)**, which adds
  `${inventory.<field>}` beside the two reference forms.

§1, §2 and §4 stand as written.

## Context

Plans repeat literals (e.g. `haribo` in both `user-group` and `dir-owner`). Variables are needed for DRY, readable plans. This ADR fixes the binding syntax, scope model, precedence and interpolation rules so they are not re-litigated. See issue #44.

## Decisions

### 1. Binding syntax — no keyword

```
owner = "haribo"
```

Immutable binding, **no `let` keyword**. The same syntax applies **everywhere**: plan top-level, vars file, and `def` bodies (the previous `let r = shell { … }` becomes `r = shell { … }`). Uniformity is mandatory — two binding syntaxes would be incoherent.

### 2. Immutability, lexical scope

- Bindings are **immutable** (no reassignment). This is why no `let`/`const` keyword is needed — its only purpose elsewhere is to separate declaration from reassignment.
- **Lexical scope** with **lexical shadowing**: a file may shadow a global; the override is confined to that file and is **never propagated to callees**. Explicitly **no dynamic scoping**.

### 3. Precedence — 3 levels, no more

```
--vars   <   plan binding   <   inventory (per-host)   <   CLI --set
```

Increasing specificity: shared vars file `<` the plan `<` the target `<` the CLI. Deliberately not Ansible's 22-level precedence.

### 4. Reference and interpolation

- A **bare identifier** in argument position is a variable reference: `user-group(owner, "docker")`.
- **Interpolation `${name}` in simple strings only**: `dir-owner("/opt", "${owner}:${owner}")`.
- **Triple-quoted strings `"""…"""` are RAW** — no interpolation. Their `${VAR}` are shell/compose variables resolved on the target; interpolating them would corrupt the content.

### 5. Resolution — split by kind

Variables are resolved **on the control host**, but *when* depends on the reference (amended: per-host vars are incompatible with pure parse-time resolution, since the host is unknown at parse):

- **Interpolation `${name}`** and a **plan binding's value** resolve **at parse time**, against the globals known then (`--vars`, `--set`, earlier bindings). Consequence: a `${name}` cannot reference a per-host var — interpolation is **global-only**.
- **Bare-identifier arguments** (`dir-owner("/opt", owner)`) are **not** resolved at parse; they are carried as unresolved refs (`proto.Step.Refs`) and resolved **per host at orchestration time**, with the full §3 precedence. This is what lets a per-host inventory var override a global. An undefined bare ref is therefore reported at **orchestration**, not parse.

The agent still receives fully-resolved argument values — the control↔agent wire protocol is unchanged (refs are resolved into `Args` before the Request is sent).

## Rejected alternatives

- **`let` keyword** — redundant under immutability, and incoherent with the inventory grammar, which already binds with `name = …` (no keyword).
- **Dynamic scoping** (a file's override propagating to callees) — makes `check` non-deterministic (a value would depend on the caller), poisoning previewability.
- **Interpolation inside triple-quotes** — collides with the `${SHELL_VAR}` the content legitimately carries (compose, YAML, shell here-docs).
- **Ansible-style 22-level precedence** — the complexity shellf exists to avoid.

## Consequences

- The `let` keyword is removed from the grammar; the stdlib defs are migrated (`let r = …` → `r = …`).
- Delivered in increments: bare-identifier references (PR1), `${name}` interpolation in simple strings (PR2), `--vars` file + inventory/`--set` precedence (PR3, needs free-form per-host vars in the inventory).
- `docs/language.md` documents the variable syntax and the raw-triple-quote rule.
