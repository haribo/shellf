# ADR 0043 — Only the plan declares a control-host path

## Status

Active. **Makes [ADR-0031](0031-control-channel-and-detachment.md) §3 true as written**
("the control host derives *from the plan* the set of resources a job may request") and
narrows [ADR-0034](0034-control-host-primitives.md) §5, which made the extraction
syntactic without saying which text it reads. Companion to
[ADR-0042](0042-render-a-declared-file.md): that one closed the variable half of the same
question, this one the file half.

## Context

The allow-list is built by `ControlResources`, which scans the plan's steps **and the
defs** (`internal/lang/control_scan.go:15-29`); `scanExpr` records a key for any literal
`%"…"` in a def body (`:78-90`). A def therefore adds entries to the list that is supposed
to bound it.

Defs can come from third parties ([ADR-0016](0016-remote-modules.md)). An imported def
that writes `~file.read(%"blog/compose.env.tmpl")` in its own body declares that access
itself. The remaining bound is containment: a path resolving outside `assets/` is dropped
(`internal/orchestrator/serve.go:54-83`) and a symlink out is refused at read time (#393).
`~/.ssh/id_ed25519` is safe; everything the operator keeps under `assets/` is not,
including a template whose rendered form carries a secret.

The code never argued for scanning def bodies. The comment at `control_scan.go:31-38`
justifies scanning **steps** — a plan writes the path at the call site while the def sees
only a parameter — and says nothing about the other half.

Measured before deciding: **no def carries a literal control-host path today**. Not one in
the embedded stdlib, not one in `examples/defs/`, not one in the e2e plans' defs — every
real occurrence sits in a plan (`test/e2e/plans/plan.shellf:19-23`,
`test/e2e/plans/coverage.shellf:21-46`) or in a comment.

## Decision

### 1. A `%"…"` inside a def body is a parse error

The refusal names the replacement: the def takes the path as a **parameter**, and the plan
passes it marked. That is how every def in this repository is already written.

```
# refused
def deliver() { apply { file.copy(%"conf.j2", "/etc/x") return ok.done } }

# how it is written
def deliver(src: str) { apply { file.copy(src, "/etc/x") return ok.done } }
on target { deliver(%"conf.j2") }
```

It applies to every position a `%"…"` can occupy in a def — an `apply`, an `observe`, a
condition, an argument to another def, a delegation — and to a def written inline in a
plan file, which is a def like any other. An `on` block is not affected: that is the plan.

At parse time, not at run time. ADR-0034 §5 already holds that a path problem is reported
when the plan is read rather than mid-deploy, and a refusal arriving as a channel error
halfway through a deployment is the failure mode that rule exists to avoid.

### 2. No exemption for the standard library

The trusted/untrusted axis of [ADR-0040](0040-rerunnable-steps-and-unsafe-shell.md) §6 is
not extended here. It exists because the stdlib is the layer that reaches the system and
`service.ensure` is *implemented* with the `systemctl` a rule would forbid — a real
conflict. There is no equivalent conflict: the stdlib holds zero literal control paths, so
an exemption would buy nothing and would have to be carried, explained and trusted
forever.

One rule, no exception, checkable with a `grep`.

### 3. The rule does not depend on where a def lives

Rejected in §4 below, but the reason belongs to the decision: a def that is local today is
an imported def tomorrow — extracting reusable defs into a module is the path #376
describes. A rule keyed on the file's location would make the same code mean two different
things depending on which repository hosts it, and the day it moves is the day nobody
re-reads it.

### 4. What this does not claim

This bounds **which** files a def can obtain, not **what it does with them**. A def that
legitimately receives `%"compose.env.tmpl"` can still push the rendered content into an
`unsafe shell { curl … }`. That is the standing contract — a def runs shell on the target —
and no allow-list addresses it. Anyone reading this record as "imported defs can no longer
leak our files" is reading it wrong.

## Rejected alternatives

- **Let only imported defs be refused, keep the local ones' literals.** Preserves a
  convenience nobody uses, and it is buildable — the alias is known where the list is
  assembled (`cmd/shellf/main.go:213-214`, and imported defs carry their alias as a prefix,
  `internal/lang/parser.go:119-135`), contrary to what #403 first claimed. It is rejected
  on §3: the rule would follow the file rather than the code.
- **Keep scanning def bodies, but refuse a path the plan did not also declare.** Same
  outcome by a longer route: the def's literal becomes decoration, and two lists have to
  agree. A rule that makes an expression meaningless is better written as a refusal.
- **Refuse at run time instead of at parse time.** Smaller change, and it turns a writing
  error into a mid-deployment channel error naming a resource the author believed
  declared.
- **Do nothing, treat containment to `assets/` as sufficient.** It is the current state:
  the imported def only has to guess a filename, which a public example hands it.

## Consequences

- Breaking: a def containing `%"…"` no longer parses. No def in this repository is
  affected — the rule was measured before being written.
- `ControlResources` loses its `defs` parameter and its def-scanning half: the plan's steps
  are the only source. `scanStmts`/`scanExpr` go with it, and `cmd/shellf` stops re-parsing
  def sources to extract `%` occurrences.
- A def wanting a control-host file gains a parameter, which also makes the file visible in
  the plan — the run stays readable before it happens, which is the thesis.
- A module cannot ship its own assets. It could not before either (a module is a flat set
  of `.shellf` files, and paths resolve under the calling project's `assets/`). If that
  need appears, it comes back as an ADR about module assets, not as an exemption here.
