# ADR 0006 — Def grammar v2: `if`/`return` instead of `when`/`->`

## Status

Active

## Context

Def bodies used `when cond -> outcome` (and `shell … -> outcome when ok`) for conditionals. That is a second conditional syntax alongside the plan's `if` (ADR-0004) — two ways to branch, extra cognitive load. Unify on one conditional. See issue #81.

## Decision

### 1. One conditional: `if` / `return`, always with braces

Inside a def, branching is `if <expr> { <stmts> }` and producing an outcome is `return <outcome>`. `when` and `->` are **removed**.

```
def install(pkg: str) {
    pre-check {
        if pkg == "" { return err.pkgMustNotBeNull }
    }
    guard {
        r = shell { dpkg -s "$pkg" }
        if r.ok { return ok.alreadyInstalled }
    }
    apply {
        r = shell { apt-get install -y "$pkg" }
        if r.exit != 0 { return err.runtime(r) }
    }
    return ok.installed
}
```

`if` is **one** concept — "condition true → body" — whether the condition is an **expression** (`pkg == ""`, `r.ok`) in a def or an **instruction** (`dir-exists`) in a plan (ADR-0004). The condition varies by context; it is not two different `if`s.

### 2. Phases stay

`pre-check` / `check` / `guard` / `apply` / `post` are **kept**. They are not decorative syntax: they carry the check/would/idempotence model (read-only pass 1 vs effectful pass 2). Removing them would break `--check` and idempotence. Phases are always blocks (`phase { … }`); the old inline `phase: <stmt>` form is dropped (one form).

### 3. Braces always

No brace-less `if cond return x` sugar (see rejected). The simple case is one line: `if cond { return outcome }`.

## Rejected alternatives

- **Keep `when`** — a second conditional syntax for the same thing; the very inconsistency this ADR removes.
- **Brace-less `if` sugar** (`if cond return x`) — reintroduces two `if` forms; and shellf has **no statement separator** (whitespace is insignificant), so without braces the body has no delimiter (`if cond return x r = shell {}` is ambiguous), plus the dangling-else trap. Go forces braces for exactly these reasons.
- **Drop the phases** (flat `if`/`return`) — the evaluator would no longer know read-only from effectful, breaking check/would/idempotence.

## Consequences

- AST: remove `GuardStmt` and `EffectStmt`'s outcome/`when`; add `IfStmt{Cond, Body}` and `ReturnStmt{Outcome}`. The final bare outcome becomes `return <outcome>`.
- Rewrite the def parser (`if`/`return`, phase-as-block only), the evaluator (`if` truthiness, `return` short-circuit), migrate every `internal/std/*.shellf`, and update def/eval tests.
- No behavior change — syntax + evaluator only.
