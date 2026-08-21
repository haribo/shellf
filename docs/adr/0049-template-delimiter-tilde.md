# ADR 0049 — Template delimiter: `~{var}`, and no sigil escape

## Status

Active. Supersedes [ADR-0021](0021-template-delimiter.md) entirely: both its
delimiter (`@{var}`) and its escape (`@@`). The problem ADR-0021 solved — not
colliding with a config file's own `${…}` — still stands and is preserved here.

## Context

ADR-0021 chose `@{var}` and, alongside it, `@@` as the escape for a literal `@`.
Both decisions have now been used in anger, and both are wrong for different
reasons.

**The escape makes an email address unwritable.** A mail signing table, a Postfix
map or an OpenDKIM key table is a file where nearly every line is
`something@domain`. Writing `admin@@{mail_domain}` renders the literal text
`admin@{mail_domain}`, because `@@` is consumed as an escape before the delimiter
is ever considered. The file looks plausible and the daemon reading it silently
never matches (#481).

**The escape should not exist at all.** It was borrowed from Razor, where `@@`
is necessary because Razor's delimiter is a *single* `@` (`@Model.Name`), so a
literal `@` is genuinely ambiguous. shellf's delimiter is two characters, so the
sigil alone is never ambiguous — and no two-character templating syntax escapes
its opening character. Jinja, Go's text/template, Handlebars and Mustache all
leave a lone `{` alone.

**And the sigil is the wrong character.** Even with the escape removed,
`admin@@{domain}` would render correctly — the first `@` literal, the second
opening the variable — but it reads as an escape when it is not. The one
character most often written immediately before a variable in the files shellf
renders is precisely `@`.

### What was checked rather than assumed

ADR-0021 rejected `{{ }}` because it collides with go-template, Helm and Traefik.
**That collision is real in principle but absent in practice here**: no file
rendered by shellf, in `examples/assets/` or `test/e2e/assets/`, contains `{{`.
The collision that *is* observable is the one ADR-0021 was right about — `${…}`,
live in `examples/assets/blog/compose.env.tmpl`, which docker-compose must
receive untouched.

`{{ }}` was still rejected, but on the honest ground: it forecloses templating a
Helm chart or a Traefik dynamic config later, and those are plausible targets for
this tool.

## Decision

### 1. The delimiter is `~{var}`

```
admin@~{mail_domain}                  → admin@example.com
VIRTUAL_HOST=${DOMAIN}                → passed through, compose reads it
rule: "Host(`{{ .Domain }}`)"         → passed through, Traefik reads it
```

`~{` is a construct in none of the ecosystems shellf renders — shell,
docker-compose, `.env`, YAML, TOML, systemd, nginx, go-template, Helm, Traefik —
and `~` is not a character written immediately before a value, which is the
defect that sank `@`.

Known overlap, accepted: `~` already marks a control-host primitive in the
*language* (`~file.render`, ADR-0034). There is no technical clash — a primitive
appears in plans and defs, a delimiter in rendered files, and the two grammars
never meet — but the character now carries two meanings in one product. That cost
was weighed against a delimiter used thousands of times, and the reading comfort
won.

### 2. There is no escape for the sigil

`~~` is not special. A lone `~` is a literal `~`, wherever it appears, exactly as
a lone `{` is literal in Jinja and Go.

### 3. A literal `~{…}` is written with a verbatim region

```
~{raw}
  This shows ~{mail_domain} as text — nothing here is substituted.
~{endraw}
```

Modelled on Jinja's `{% raw %}`, which is the only mechanism among the tools
surveyed that a reader can use without learning a second syntax. Go's approach —
`{{"{{"}}`, the template quoting itself — requires an expression language inside
the delimiters, which shellf's templates deliberately do not have and are not
getting (#481 puts conditionals and loops out of scope).

`raw` and `endraw` are therefore reserved names: a template variable cannot be
called either.

This is what lets a template document its own placeholders — the second half of
#481, and a real gap: `examples/assets/blog/compose.env.tmpl` currently spells
out "an at-sign and braces" in prose because it cannot show the marker it exists
to carry.

## Consequences

- **Breaking.** Every template using `@{var}` must be rewritten to `~{var}`. In
  this repository that is three assets and the examples; outside it, an operator's
  templates. Announced as `BREAKING` in the changelog.
- A template that used `@@` for a literal `@` now writes a single `@`.
- The renderer loses a branch rather than gaining one: no escape handling.

## Rejected alternatives

- **`{{ }}` (Jinja, Go, Helm, Ansible, Nomad).** The familiar choice, and it
  fixes the email case. Rejected because it forecloses rendering files destined
  for tools that use it — a Helm chart or a Traefik dynamic config would need
  every one of its own `{{ }}` wrapped in a verbatim region, which is worse than
  the disease.
- **`[[ ]]` (Levant, for exactly this reason — avoiding Nomad's `{{ }}`).** The
  strongest runner-up: attested, no collision in the rendered ecosystems, nothing
  to escape. Rejected on taste rather than on merit — `~{}` keeps the familiar
  `sigil + braces` shape of `${}`, which reads as "a variable" at a glance.
  Residual overlap: `[[ … ]]` is a bash test construct, harmless in a config file
  but live in a templated script.
- **Keeping `@{}` and deleting only the `@@` escape.** Free, no migration, and it
  makes the email case work. Rejected because `admin@@{domain}` still reads as an
  escape to every reader who meets it, forever.
- **`%{ }` (Terraform directives).** Collides with Apache, where `%{HTTP_HOST}`
  is the syntax for environment variables in `LogFormat` and `RewriteCond`.
- **`<< >>`.** `<<` is YAML's merge key (`<<: *anchor`) and the shell heredoc.
- **`${{ }}`.** The GitHub Actions expression syntax.
- **`#@` (ytt).** Elegant where the host format uses `#` comments, since the
  template stays valid YAML. Meaningless for the formats that do not.
- **Go-style self-quoting** instead of a verbatim region. Requires string
  literals inside the delimiters; see §3.
