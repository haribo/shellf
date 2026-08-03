# ADR 0019 — `template`: render a control-host file, ship it as a file-write

## Status

Active.

## Context

Real deployments render config files: an ACME email into `traefik.yml`, a version
into a `.env`, a domain into a label. Today the only way to place file content on
a target is `file-write(path, "literal")` — the content must be inlined in the
plan (and multi-line strings are raw, so `${var}` does not interpolate). Inlining
a 40-line `traefik.yml` into the plan is the wrong shape; configs are files.

Note: `file-copy(src, dst)` does **not** help — it runs `cp "$src" "$dst"` on the
**target**, so both paths are on the target. shellf has no way to bring a
control-host file's content to a target except a `file-write` argument.

We want Ansible's `template`: a control-host template file, rendered with plan
variables, delivered to the target.

## Decision

### 1. `template(src, dst)` renders a control file onto the target

`src` is a path on the **control host** (relative to the plan file); `dst` is the
target path. shellf reads `src`, interpolates `${var}` with the plan's variables,
and writes the rendered content to `dst`.

```
# traefik.yml (a real file next to the plan) contains `${acme_email}`
on host {
    template("traefik.yml", "/etc/traefik/traefik.yml")
}
```

### 2. Resolved on the control host into a `file-write`

`template` is **not** a new agent instruction or transport mechanism. On the
control host, after the plan is parsed, each `template(src, dst)` is replaced by
`file-write(dst, <rendered content>)`: the rendered text becomes the file-write
argument and ships to the target exactly as any file-write content does
(ADR-0002 §4 — control resolves, agent executes). Idempotence, the `--check`
content diff, and `status` all come for free from `file-write`'s content observe.

The agent never sees a `template` instruction.

### 3. Rendering: `${var}` over the plan's global variables

The same `${var}` interpolation as strings, over the global variable tiers
(`--vars`, plan bindings, `--set`, secrets). An undefined `${x}` is an error,
raised on the control host before any target is touched. Per-host inventory vars
are **not** available in v1 (the render is global, once) — per-host templating is
a later concern.

### 4. Secrets in templates

A secret rendered into a template flows through `file-write`'s content like any
value: it is **redacted by value** in all output (ADR-0018) and lands on the
target the same way. No new exposure.

## Rejected alternatives

- **Interpolated inline strings** (`file-write(dst, t"""…${x}…""")`). Smaller, but
  it inlines the config into the plan — the wrong shape for real config files.
  The `template` builtin keeps configs as separate files (chosen).
- **A target-side render** (the agent reads the template). The template lives on
  the control host, not the target; and control-side resolution keeps the agent
  dumb (ADR-0002 §4).
- **`{{ }}` template markers** (Ansible/Jinja). Reuse shellf's existing `${var}`
  — one interpolation syntax. Cost: a literal `${…}` in a template that is *not*
  a shellf var cannot be kept verbatim in v1 (documented).
- **Per-host rendering in v1.** Rendering once with global vars covers the
  concrete need (version/email/domain are deploy-wide). Per-host vars later.

## Consequences

- Config files stay files: `template("traefik.yml", "/etc/…")` renders and
  delivers, with idempotence and `--check` diffs inherited from `file-write`.
- The render is control-side and global-var only; undefined vars fail fast,
  locally. Per-host templating is a documented follow-on.
- `template` is a builtin **name** the parser accepts (signature `src, dst`), but
  it is resolved away before orchestration — the agent and the wire protocol are
  unchanged.
- Implementation: a control-side resolution pass (in the CLI, where the
  filesystem and the global vars are) walks the plan, reads+renders each
  `template`, and rewrites it to `file-write`. Reuses the string interpolation.
