# ADR 0034 — `%` marks the control host: primitives and paths

## Status

Superseded by [ADR-0036](0036-primitive-and-location-markers.md) — which keeps the
decision (a closed set of primitives reaching control-host data) and splits the marker:
`~` for a primitive, `%` for a control-host path. The single marker could not express a
primitive that writes on the target.

Built on [ADR-0031](0031-control-channel-and-detachment.md) (a job may pull
data from the control host) and [ADR-0030](0030-def-composition.md) (a def may call
another instruction). Uses the naming of [ADR-0032](0032-stdlib-naming.md).

## Context

Two instructions are not defs. `file.template` and `dir.copy` are Go transformations
that rewrite the plan before it is sent: the first reads a template, substitutes
`@{var}` and emits a `file.write`; the second walks a directory and emits one
`file.put` per file.

They are invisible to the language. They cannot be read, composed, or overridden, and
their behaviour lives in Go visitors — which is where #293 came from: two of those
visitors had drifted apart and stopped handling a construct the language allows.

The reason they are not defs is structural, not an oversight: a def is evaluated **on
the target**, and their input file is **on the control host**. No instruction can reach
it, because the language has no way to say "this part happens on my machine".

That is the gap this ADR closes. Not by moving the work to the target — the source file
is not there, and shipping every variable to every host would undo
[ADR-0018](0018-secrets.md)/[ADR-0025](0025-secrets-at-rest-tmpfs.md) — but by giving
the language a way to name the control host.

## Decision

### 1. `%` means "on the control host", for a call and for a path alike

```
contents = %file.read("conf.j2")        # primitive: runs on the control host
file.template(%"conf.j2", "/etc/a.conf") # path: this file is on the control host
```

One rule covers both positions: **a `%` marks what belongs to the operator's machine**.
It is a prefix, never a character inside a string — `"%/etc/x"` is an ordinary path
whose first character is a percent sign. That keeps the marker visible when the value
comes from a variable, and lets a missing file be reported before any connection.

### 2. `%` is valid only before a primitive from a closed set

The set is `file.read`, `file.render` and `dir.list` — no other name is accepted, and
**`%` before a def is a parse error**, naming the offending call.

This is the rule that keeps the door shut: if `%` could prefix a def, a def could run
shell, and shell would run on the operator's machine — the one holding every SSH key
and every secret. shellf runs shell on targets. That is the product.

### 3. The primitives read; they never act

| Primitive | Returns |
|---|---|
| `%file.read(path)` | the contents of a control-host file, binary included |
| `%file.render(contents)` | those contents with `@{var}` substituted |
| `%dir.list(path)` | the entries of a control-host directory, with their kind |

Splitting read from render is deliberate: it makes them composable, and it covers the
case neither of today's Go transformations can — rendering a template whose source
lives on the *target* (`%file.render(shell { cat … })`).

Writing is not among them. It happens on the target, through `file.put`, the existing
engine primitive that today only `dir.copy` can emit; it becomes callable. A
control-host write primitive would mean shellf modifying the operator's machine, which
is not what it is for.

### 4. `bytes` is a value type, opaque

`%file.read` on an image cannot return a string. The language gains `bytes`: it can be
passed from a primitive to an instruction, and nothing else. It cannot be interpolated
into `"${…}"`, compared, or printed in a report — a report showing raw bytes is noise,
and a comparison invites treating them as text.

base64 remains a transport detail, in the JSON request only. It never appears in a plan
or a value.

### 5. What `%` needs is declared before the run, served during it

ADR-0031 lets a job ask the control host for data mid-run, and requires the control
host to serve **only what the plan declared**. The two fit because `%` occurrences are
syntactic: the control host extracts them from the plan before sending, and that set is
the allow-list it will honour.

A consequence worth stating: a `%` path built from a value the target produces cannot be
in that set, and is refused when the plan is read — not mid-deploy.

### 6. `file.template` becomes a def; `dir.copy` does not, yet

```
def template(src, dst: str, validate: str = "") {
    apply { file.write(dst, %file.render(%file.read(src)), validate) }
}
```

`dir.copy` stays a Go transformation for now: it emits one step per file, and writing it
in shellf needs a `for` that runs inside a def body and iterates a computed list —
neither of which [ADR-0017](0017-for-loops.md) provides.

## Rejected alternatives

- **Render on the target.** Would require shipping the template *and every variable* to
  every host, so every secret in the plan would reach machines that use none of them.
  Today a target receives only substituted values (`proto.Request` carries no variable
  table). Rejected on ADR-0018/0025 grounds. It would also make `--check` unable to show
  what will be written.
- **A local `shell` primitive** (`%shell { sed … }`). It is the one thing that makes
  every other guarantee void: an imported package ([ADR-0016](0016-remote-modules.md))
  could read `~/.ssh/id_ed25519`. A closed set of readers is bounded; arbitrary
  execution is not.
- **A `control { }` phase** instead of a prefix. Adds a seventh phase to the def
  grammar, and duplicates what the prefix already says at the exact point it applies.
- **`%` inside the string** (`"%/etc/x"`). Invisible when the value comes from a
  variable, ambiguous for a path legitimately starting with `%`, and impossible to
  check before the run.
- **Keeping the Go transformations.** They work, but they are unreadable, unoverridable,
  and they are where #293 came from.

## Consequences

- The lexer gains `%` as a prefix; the parser accepts it before a closed set of names
  and before a string literal, and rejects it elsewhere by name.
- The language gains a second value type. Every place that assumes "a value is a
  string" — interpolation, comparison, the shell environment, reports — must reject
  `bytes` explicitly rather than stringify it.
- `file.put` becomes callable, so a def can deliver binary content.
- `file.template` becomes an ordinary def: readable, composable, overridable by
  placement ([ADR-0033](0033-sub-packages.md)). One of the two Go transformations disappears.
- The control host extracts `%` occurrences statically to build ADR-0031's allow-list.
