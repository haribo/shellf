# ADR 0024 — Per-host template rendering

## Status

Active. Supersedes [ADR-0023](0023-template-scope-loopvar-and-per-host.md) §2 (which
deferred per-host rendering) and amends [ADR-0019](0019-templates.md) §3 (render was
global, once). The `@{var}` delimiter (ADR-0021) and `with { }` (ADR-0022) stand.

## Context

ADR-0019 rendered a template once on the control host over the **global** variables,
and ADR-0023 deferred per-host rendering for want of a multi-host test surface. That
deferral is now lifted: templates must be able to vary by host — a config that carries
the host's address, role, or any inventory var is the ordinary case in a fleet
(Ansible renders templates against host vars + facts). Rendering once, globally, cannot
express it.

The obstacle was layering: the render runs in the CLI (`loadPlanPackage`), before the
orchestrator resolves anything per host. A host's variables (`host.Vars`) exist only
later, per host, in the orchestrator's `reqFor`, where `mergeEnv(baseVars, host.Vars,
setVars)` is built. But the orchestrator (and `proto`) deliberately import neither
`lang` nor the filesystem, so the render cannot simply move there.

## Decision

### 1. Rendering moves to per-host resolution

A `template(src, dst)` step is **no longer** resolved in `loadPlanPackage`. It survives
into the orchestrator and is rendered **per host**, over that host's full variable scope
`mergeEnv(baseVars, host.Vars, setVars)` (plus the call's `with { }`), then rewritten to
`file-write(dst, rendered)` before the request is sent. Each host gets its own rendered
content; `--check` and `status` show the per-host diff.

### 2. A renderer is injected into the orchestrator

The orchestrator stays free of `lang`/filesystem: the CLI passes a renderer

```
func(src string, vars map[string]string) (string, error)
```

that reads `src` (relative to the plan dir, which the CLI closes over) and interpolates
`@{var}` (`lang.Template`). The orchestrator calls it per template step per host, walking
into `if`/`block`/`parallel`, and rewrites the step to a `file-write`. `proto` is
untouched.

### 3. `dst` may be a per-host ref; `src` stays a literal control path

`dst` may now be a bare per-host variable (`template("nginx.conf", conf_path)`), resolved
from the host env like any ref. `src` remains a **literal** control-host path — it names a
file on the control host, not a per-host value — so a ref `src` is still an error.

### 4. Undefined variables fail before the target is touched

An undefined `@{var}` (or an undefined `dst` ref) fails during that host's resolution, in
`reqFor`, **before** anything is sent to the host — the same fail-fast as before, now per
host instead of once. Secrets rendered into content stay redacted by value (ADR-0018).

## Rejected alternatives

- **Keep the global-once render (ADR-0019/0023).** Cannot express host-varying config —
  the whole point.
- **Render on the target (agent reads the template).** The template lives on the control
  host; and it would make the agent read control state, breaking the dumb-agent boundary
  (ADR-0002 §4). Control-side-per-host keeps the agent unchanged.
- **Push `lang`/filesystem into the orchestrator.** Breaks the layering that keeps
  `orchestrator`/`proto` transport-only. A one-function injected renderer is enough.
- **Interpolate `dst`'s `${var}` per host.** `${}` is parse-time/global by construction;
  per-host values reach steps as **refs** (ADR-0002), so `dst` uses a bare ref, consistent
  with every other instruction.

## Consequences

- Templates can carry host-specific values; each host renders its own file-write, with
  per-host `--check`/`status` diffs.
- The render is control-side and per-host; undefined vars fail fast, per host, before the
  target is touched.
- `orchestrator.Run` gains a renderer parameter; `loadPlanPackage` no longer rewrites
  templates. `proto` and the agent are unchanged.
- The `template` render scope is now: `--vars`, plan bindings, **per-host inventory vars**,
  `--set`, secrets, and the call's `with { }`.
