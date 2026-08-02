# ADR 0013 — The `observe` contract: derived skip, and the `status` mode

## Status

Active

Supersedes the `guard` phase of [ADR-0006](0006-def-grammar-v2.md) (guard is
removed; the skip decision it encoded is now derived — see Decision 1).

## Context

`shellf status <machine>` (issue #93) must report, per managed resource, the
**current vs desired** state — `nginx: version 1.2.0 → 1.3.0`, `ufw: absent →
present`. Today a def cannot surface that: its `guard` phase only answers a
**boolean** ("already converged → skip"):

```
guard {
    r = shell { dpkg -s "$pkg" }
    if r { return ok.already }   # the author hand-writes the skip decision
}
```

A boolean cannot show `1.2.0 → 1.3.0` — the value is lost. `status` needs the
observed **value** and the desired **value**, side by side.

Two further facts shaped the design:

- The skip decision (`guard`) and the `--check` preview both already read state.
  A separate `status` read would be a third path over the same ground. They
  should fall out of **one** source of truth.
- `guard` was the odd phase: a boolean predicate the author writes by hand,
  duplicating "== desired" logic that the engine could compute. It is
  semantically the same act as observing state — just crippled to yes/no.

## Decision

### 1. A read-only `observe` phase returns the current state; `guard` is removed

A resource-shaped def declares an **`observe`** phase that reads the target and
returns a `state(...)` record of the current observed values. The old `guard`
phase is gone: the "already converged → skip" decision is **derived** by the
engine (Decision 4), not authored.

```
def apt-install(pkg: str, ensure: str = "present", version: str = "") {
    observe {
        i = shell { dpkg -s "$pkg" }.ok
        return state(
            ensure:  i ? "present" : "absent",
            version: shell { dpkg-query -W -f='${Version}' "$pkg" }.stdout,
        )
    }
    apply {
        r = shell { apt-get install -y "$pkg${version:+=$version}" }
        if !r { return err.runtime(r) }
        return ok.installed
    }
}
```

`observe` is read-only by convention (like `check`/`guard` before it) and runs
in **every** mode — apply, check, and status — because reading never mutates.

### 2. `state(...)` is a return-only form, not a general type

`state(field: value, …)` collects existing scalar values (`shell{…}.ok` → bool,
`.stdout` → str, ints) into named slots. It is **only** the return of `observe`:
it cannot be bound to a variable or field-accessed (`.field`) in shellf. The
engine consumes it for the diff and the report. The value model of
[ADR-0010](0010-result-and-shellresult-model.md) — `Result` (sum),
`ShellResult` (product) — is unchanged; `state` adds no third value users
manipulate. A general record type, if ever needed, is a separate ADR.

### 3. Intent lives in defaulted parameters — there is no `desired` phase

The desired state is **already declared**: it is the call's arguments. Restating
them in a phase is boilerplate. Following Ansible's `state=` and Puppet's
`ensure =>`, intent that is not a plain value (present/absent, and later latest)
is a **parameter with a default**, not magic:

```
apt.install("nginx")                  # ensure defaults to "present"
apt.install("nginx", version: "1.3.0")
apt.install("nginx", ensure: "absent")
```

So the desired is **always** carried by an argument (defaulted or explicit).
There is no separate `desired` block — an earlier draft added one and it only
ever re-stated params.

### 4. The engine auto-diffs `observe` against the arguments; that diff drives apply

For each field of the `observe` record, the engine compares it to the
**same-named argument**:

- an argument that is **empty/unset** excludes its field from the diff ("don't
  care" — e.g. `apt.install("nginx")` ignores `version`);
- otherwise the field is converged when `observed == argument`.

The comparison is typed with **string coercion**: `observe` fields are typed
(bool/str/int) while arguments arrive as strings (the env-injection channel), so
`true` (bool) equals `"true"` (arg). This is the same coercion guards did in
shell, lifted into the evaluator.

The diff then drives execution, in three modes:

| Mode | Diff empty | Diff non-empty |
| --- | --- | --- |
| **apply** | skip `apply` → `ok.already` | run `apply` |
| **check** | `ok.already` (nothing to do) | `would.<tag>` + the diff (never runs `apply`) |
| **status** | report `converged` | report `field: current → desired` (never runs `apply`) |

The author writes **no** `if converged { return ok.already }`: the skip is the
empty diff. `apply` keeps its nominal return ([ADR-0007](0007-no-return-outside-action.md)
is untouched).

### 5. Resource-shaped vs action-shaped, by structure

- **resource-shaped** = the def has an `observe` phase → it appears in `status`
  with its current→desired diff, and its `apply` is gated by that diff.
- **action-shaped** = no `observe` phase (e.g. a `restart`, an idempotent shell
  that does not decompose into observable fields) → `apply` **always** runs, and
  `status` lists it as `action (no observable state)`.

No annotation: the presence of `observe` is the classification.

### 6. Never-lie: `observe` must expose everything that determines convergence

Because the skip decision is the diff, `observe` must report **every** field
that decides convergence — otherwise the engine skips `apply` on a resource that
is actually adrift, and `status`/`--check` show a state the run does not honor.
This is stricter than `guard` (which could test anything privately), and
deliberately so: what decides the skip is exactly what `status` displays. A def
whose convergence genuinely cannot be decomposed into observed fields is
action-shaped (Decision 5), not a resource with a lying `observe`.

## Rejected alternatives

- **A `desired` phase.** Restates the parameters; pure boilerplate once intent
  is a defaulted param (Decision 3).
- **Keep `guard`, add `observe` alongside.** Two phases reading the machine for
  overlapping purposes — the redundancy this ADR removes. `status`, `--check`,
  and the skip would drift as three paths.
- **A general `state` record type** (bindable, field-accessible). Expands the
  value model (ADR-0010) for a reporting feature; deferred to its own ADR if a
  real need appears (Decision 2).
- **`latest` and relational desireds in v1.** `ensure=latest` is not an equality
  (it needs the repository's newest version), so it cannot fall out of the
  same-named auto-diff. v1 handles equality-expressible intents
  (present/absent/version-pin); `latest` waits — either a relational `observe`
  field (`uptodate: bool`) or a later ADR.

## Consequences

- Resource defs **shrink**: the hand-written `guard` predicate and its
  `ok.already` disappear, replaced by a declarative `observe`.
- `status`, `--check`, and the skip decision share **one** source (the diff), so
  they cannot disagree.
- The `~10-15` std defs with a `guard` must be rewritten to `observe`
  (mechanical). `dir-exists`/`file-exists` questions (`check` phase, no `apply`)
  are **unaffected** — they are read-only questions, not managed resources.
- `pre-check`, `check` (questions), `apply`, and `post` are unchanged; only
  `guard` is removed. The phase set becomes: `pre-check`, `observe`, `apply`,
  `post` for resources, plus `check` for questions.
- Naming: the intent parameter is `ensure` (Puppet), never `state`, to avoid
  colliding with the `state(...)` return form.
- Implementation lands in slices after this ADR: evaluator (observe + diff +
  status mode), std def migration, the `status` command (#93).
