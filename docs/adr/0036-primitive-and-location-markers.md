# ADR 0036 — `~` marks a primitive, `%` marks the control host

## Status

Active. **Supersedes [ADR-0034](0034-control-host-primitives.md)**, whose decision it
keeps — the language reaches control-host data through a closed set of primitives — and
whose marker it splits in two.

## Context

ADR-0034 gave `%` a single meaning, "on my machine", and applied it to two different
things: a call (`%file.read(…)`) and a path (`%"conf.j2"`). It reads as one rule, which
is why it was written that way.

The two come apart as soon as a primitive writes on the **target**. `~file.write(path,
bytes)` is an engine call like `~file.read`, but it acts over there; under ADR-0034 the
`%` would claim the opposite. Either the marker lies, or the primitive cannot exist.

It also leaves an unanswerable question for the remaining Go primitives. `file.put`
writes bytes on the target — is it `%file.put`? Under one reading yes (it is a
primitive), under the other no (it does not run on the control host). The marker cannot
answer, because it is two markers wearing one symbol.

## Decision

### 1. `~` marks a primitive; `%` marks a control-host path

```
~file.read(%"conf.j2")     # primitive, reading a file on my machine
~file.read("/etc/motd")    # primitive, reading a file on the target
~file.write(dst, data)     # primitive, writing on the target
```

`~` says **what it is**: an engine call, with no phases and no `override`. `%` says
**where it is**: a path on the operator's machine. They compose, and neither implies the
other.

### 2. A primitive is marked because its contract differs, not because it is Go

A def has phases, can be observed, and can be overridden by placement
([ADR-0033](0033-sub-packages.md)). A primitive has none of that. A reader deciding
whether they can override a call needs to know which they are looking at — that is what
the marker buys, and it is why "the implementation language is a detail" is not an
argument against it.

### 3. `~` and not `&`

`&&` appears throughout shellf's shell blocks (`test -f "$path" && …`), so `&file.read`
a line below reads badly in the same file. `~` has no such neighbour: it appears mostly
in `~/.ssh`, which a plan rarely contains.

Recorded cost: on a French AZERTY keyboard `~` is a dead key (AltGr+2 then space). It is
typed, not blocked, and it is directly available on QWERTY.

### 4. `~file.write` writes on the target only

Writing on the control host was considered — it would simplify cases where an operator
wants a file produced locally — and is rejected.

The allow-list ([ADR-0031](0031-control-channel-and-detachment.md) §3) bounds what a
plan may **read** from the operator's machine: the `%` occurrences are extracted before
the run and everything else is refused. There is no equivalent for writing, and the
question "which files may a plan overwrite on my machine" is materially harder. Until it
has an answer, an imported def ([ADR-0016](0016-remote-modules.md)) must not be able to
write `~/.ssh/authorized_keys`.

### 5. The primitives

| Primitive | Does | Where |
|---|---|---|
| `~file.read(path)` | returns the contents | control host if `%"…"`, else the target |
| `~file.write(path, bytes)` | writes the contents | target |
| `~file.render(content)` | substitutes `@{var}` | control host — it needs the operator's variables |
| `~dir.list(path)` | returns the entries | control host if `%"…"`, else the target |
| `~dir.sync(src, dst, delete, compare)` | transfers a tree | control host to target |

What each primitive *returns* is recorded in
[ADR-0041](0041-inert-apply-in-check-mode.md) §4 — the table above says what they do and
where, which is this record's decision and stays unchanged.

`shell { }` stays a language block, not a call, so it carries no marker.

`~file.render` substitutes over **both** halves of a template's namespace: the host's
variables and secrets, which stay on the control host, and the variables in scope where
the call was made — a def's parameters, a `with { }` override at the call site
([ADR-0022](0022-with-block.md)) — which exist only on the target. The ask carries the
caller's scope, and the control host layers it over the host environment, so the most
local binding wins. Sending only the content renders a `with { }` binding as an
undefined variable; recorded because the split is not visible from either side alone.

The two-halves rule above is unchanged. What the row for `~file.render` takes is not:
[ADR-0042](0042-render-a-declared-file.md) narrows the argument from a content to a
`%"…"` path, so the text being substituted comes from a declared file rather than from
the target. The caller's scope still travels — that half is the caller's own.

### 6. `~dir.sync` replaces `dir.copy`; a copy is a parameterised sync

`dir.copy` reads a whole tree into memory, base64-encodes it, and sends it in one
message under a 32 MB ceiling ([ADR-0028](0028-dir-copy.md)). That fails on a large
tree, and re-sends everything on every run.

`~dir.sync` streams, and skips what is already identical. `dir.copy` becomes a def
calling it with `delete = "false"`: **a copy is a sync that deletes nothing**. Both then
gain honest idempotence — today `dir.copy` re-sends every file, every time.

Comparison defaults to size+mtime, with `sha256` available. sha256 and not md5, because
the project already compares by sha256 in `file.download` and `file.put`, and a second
digest convention would have to be justified at every reading.

The default's limit is documented rather than discovered: **size+mtime misses a change
that preserves both**. Rare, and expensive to diagnose when it happens.

### 7. What leaves Go

| Was | Becomes |
|---|---|
| `file.put` | `~file.write` |
| `file.copy` | a def: `~file.read` + `~file.write` |
| `file.template` | a def: `~file.read` + `~file.render` + `~file.write` |
| `dir.copy` | a def over `~dir.sync` |

Five primitives remain: the four above plus `shell`.

## Rejected alternatives

- **Keep one marker (ADR-0034).** Cannot express a primitive that writes on the target,
  which is the whole of `file.put`'s job.
- **No marker on calls**, letting the path carry everything. Simpler to type, but a
  reader can no longer tell a def from a primitive, hence whether it can be overridden.
- **`&` for primitives.** Collides visually with `&&`, which shellf's own shell blocks
  are full of.
- **`§`.** Absent from QWERTY keyboards.
- **`~file.write` allowed on the control host.** No allow-list exists for writes (§4).
- **Delegating tree transfer to `rsync`.** Requires it installed on the target, which
  shellf assumes nowhere — and shellf ships its own agent precisely to avoid that class
  of dependency.

## Consequences

- `%file.read`, `%file.render` and `%dir.list` shipped in #321; the marker change means
  redoing the lexer, parser, evaluator, docs and tests for them. Stated so the cost is
  weighed rather than discovered.
- The lexer gains `~` as a prefix; `%` narrows to string literals only, so `%name(…)`
  becomes a parse error naming what to write.
- `file.put` becomes reachable under its new name, closing a gap: today no plan can
  deliver a single binary file — only a whole directory through `dir.copy`.
- ADR-0034 keeps its body and is marked superseded by this record.
