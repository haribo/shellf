# ADR 0023 — Template render scope: loop vars via `with`, per-host deferred

## Status

Active. Resolves the loop-variable thread (§1, via `with`). Its §2 — deferring
per-host rendering — is **superseded** by
[ADR-0024](0024-per-host-template-rendering.md), which builds per-host rendering.
§1 stands.

## Context

A `template(src, dst)` renders `src` on the control host, once, over the **global**
variables (ADR-0019 §3). Two variable scopes are not global, so a template's
content could not see them:

1. **A `for` loop variable.** `for svc in […] { template("unit.tmpl", …) }` — the
   loop var interpolates in the plan (`dst`, args) because the body is re-parsed
   per item with `${svc}` bound (ADR-0017), but the render of the template *file*
   runs later, over globals only, so `@{svc}` in the file did not resolve.
2. **Per-host inventory vars.** The render is control-side and once; a host's
   `host.Vars` exist only later, per host, in the orchestrator — so a template
   could not vary by host.

## Decision

### 1. Loop variable in a template: compose `with { }` (no new mechanism)

[ADR-0022](0022-with-block.md)'s per-call override already solves case 1. Passing
the loop var explicitly makes it part of the render scope for that call:

```
for svc in ["alpha", "beta"] {
    template("unit.tmpl", "/opt/${svc}/unit") with { svc = "${svc}" }
}
```

`${svc}` in the `with` value resolves to the item at parse (the body is re-parsed
per item with `svc` bound), so the template renders `@{svc}` = the item. The two
features compose; no automatic injection of the loop var into the render scope is
added — it would duplicate `with` and make a template's inputs implicit again,
the very thing `with` fixes.

### 2. Per-host template rendering: deferred

Rendering stays control-side and global-var (plus any `with`) for now. Per-host
templating (a template varying by `host.Vars`) is **not** built yet: the current
target is single-host, so it has no test surface, and it is a non-trivial layering
change (below). It remains a recognized follow-on, not a rejected capability.

## Rejected alternatives

- **Auto-inject the loop var into the render scope.** Overlaps `with { }`; makes
  the template's inputs implicit (must read the file to know it uses `@{svc}`).
  `with` keeps them explicit and local. Rejected.
- **Build per-host rendering now.** No multi-host test surface today, and it moves
  the render from a single control-side pass (`loadPlanPackage`) to per-host
  (inside the orchestrator's `reqFor`, where `host.Vars` live). That pass renders
  files and needs `lang`, which the `orchestrator`/`proto` layers deliberately do
  not import. Speculative code with no way to exercise the per-host difference —
  deferred, not done.

## Consequences

- A loop over templates works today via `template(…) with { var = "${var}" }` —
  explicit, per-call, and covered by a test.
- Per-host templating is deferred with a recorded design: when a multi-host need
  appears, move template resolution into the per-host path and inject a renderer
  (`func(src string, env map[string]string) (string, error)`) into the
  orchestrator — keeping `lang`/the filesystem out of `orchestrator`/`proto`.
- The `template` render scope is: globals (`--vars`, plan bindings, `--set`,
  secrets) plus a call's `with { }`. Per-host inventory vars are out of scope
  until that follow-on.
