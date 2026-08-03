# ADR 0021 — Template delimiter: `@{var}`, not `${var}`

## Status

Active. Supersedes the delimiter choice of
[ADR-0019](0019-templates.md) §3 (which reused `${var}`); the rest of ADR-0019
stands.

## Context

ADR-0019 rendered templates with the plan's `${var}` interpolation. But template
files are exactly the files that use `${…}` for their own tools — docker-compose,
`.env`, systemd `EnvironmentFile`, shell — which shellf would wrongly interpolate.
Escaping every literal `${…}` in a real compose file (dozens) is untenable.

No delimiter is universally clash-free: `${}` collides with shell/compose;
`{{ }}` (Jinja/Ansible/Go) collides with go-template, Helm, and **Traefik dynamic
config**. The delimiter must be chosen against the config ecosystems shellf
actually templates.

## Decision

### 1. Template variables are `@{var}`

A template file's shellf variables use `@{var}`; `${…}` and `{{ … }}` are left
**verbatim** for the downstream tool (compose, Traefik).

```
# traefik.yml (rendered by shellf)
certificatesResolvers.le.acme.email: "@{acme_email}"
# ${HOSTNAME} and {{ .Whatever }} below are passed through untouched
```

`@{` is not a construct in shell, docker-compose, `.env`, YAML, TOML, systemd,
go-template, or Traefik — the ecosystems shellf renders. It keeps the familiar
`${}` shape (just `@`), so it reads naturally. Residual, negligible edges:
PowerShell `@{}` (not a Debian deploy target) and YAML's `@` reserved indicator
at a scalar *start* (mid-value is fine; quote if needed).

### 2. `@@` escapes a literal `@{`

A literal `@{…}` that is not a shellf variable is written `@@{…}` (`@@` → `@`).
Rare in practice, since `@{` is uncommon downstream.

### 3. The plan's string interpolation stays `${var}`

Only *template files* switch to `@{}`. shellf plan source keeps `${var}` in its
strings — plan code is not a config file for another tool, so there is no clash,
and changing it would break every plan. Two delimiters, in two clearly distinct
contexts (plan code vs an external config file being rendered).

## Rejected alternatives

- **`${var}` in templates** (ADR-0019's original). Collides with the very files
  it renders (compose/env/shell); unusable without escaping everything.
- **`{{ var }}`** (Jinja/Ansible/Go). Collides with go-template, Helm, and
  Traefik dynamic config — squarely in shellf's target domain.
- **`[[ ]]` / `#{ }` / `%{ }`.** TOML arrays / YAML-shell comments / systemd
  specifiers respectively — each clashes with a common config format.
- **A configurable delimiter.** Over-engineered for one clear default; revisit
  only if a concrete conflict appears.

## Consequences

- `template(src, dst)` renders `@{var}`, leaving `${…}` and `{{ }}` for the
  downstream tool — real compose and Traefik files template cleanly, no escaping.
- The plan's own `${var}` interpolation is unchanged; the two delimiters live in
  distinct contexts.
- Implementation: the template renderer uses `@{}` (with the `@@` escape),
  separate from the plan-string `${}` interpolation.
