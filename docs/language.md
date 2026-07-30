# shellf — language spec

> Living doc. Only **stable** primitives are documented here. Instruction syntax is still moving → not yet included.

## `Result` — an instruction's outcome

What every instruction returns (implicit — `-> Result` is omitted). A tagged union, **not** a `ShellResult`.

```
Result = ok.<tag>(payload?) | err.<tag>(payload?) | would.<tag>(payload?)
```

- `ok` — desired state reached. `err` — failure. `would` — check mode: the engine *would* have acted (derived, never authored).
- Comparison at two levels: `== ok` matches the category (idempotence); `== ok.changed` matches the exact tag.
- Open set: `err.runtime(shellResult)` is mandatory (the shell fails in unbounded ways).
- Optional payload carries data — often a `ShellResult` for diagnostics (`err.runtime(r)`).

### `Result` vs `ShellResult` — do not conflate

| | `ShellResult` | `Result` |
|---|---|---|
| Level | raw shell mechanics | instruction's semantic decision |
| Shape | `{ exit, stdout, stderr, ok }` | `ok`/`err`/`would` + tag + optional payload |
| Produced by | a `shell { }` block | a `def` instruction |

An instruction **reads** the `ShellResult` and **translates** it into a `Result` (via `when`/tags). A `Result` may *carry* a `ShellResult` in its payload; it is not one. Flattening `Result` to exit/stdout/stderr = branching on exit codes = plain bash — the exact regression shellf exists to avoid.

## `shell` — run shell

A `shell { … }` block sends its body to `/bin/sh -c` (POSIX, not bash) and returns a `ShellResult`.

```
type ShellResult {
    exit:   int    // exit code
    stdout: str    // captured standard output
    stderr: str    // captured standard error
    ok:     bool   // sugar: exit == 0
}
```

- `.ok` is **not** stdout: it is `exit == 0`, derived. Use it when only success/failure matters (`is-active`).
- `stdout` / `stderr` remain available separately.

### `set -e` by default

shellf injects `set -e` + `set -o pipefail` at the top of every block. `exit` = the code of the **first** failing command (else `0`). Without it, `exit` would be the last command's and a mid-block failure would be masked.

`shell(raw) { … }` removes the net for the user who wants control.

### Variables — injection via environment

Language variables are passed through the `sh` **environment**, never concatenated into the command text. Access via `$name`. A `;` inside an expanded value does not execute → command injection is impossible on shellf's side.

User responsibility: **quote** (`"$pkg"`) to avoid word-splitting on spaces.

### Forms

```
// bare: exit != 0 halts (halting rule)
shell { apt-get install -y "$pkg" }

// captured: read the return
s = shell { systemctl is-active --quiet "$name" }
s.ok == want -> ...

// one line, explicit tag
shell { … } -> err.runtime when err
```

## Variables

Immutable bindings, **no keyword** — same syntax in plans, vars files, and `def` bodies:

```
owner = "haribo"
r = shell { usermod -aG docker "$owner" }
```

- **Immutable**: no reassignment (which is why no `let`/`const` is needed).
- **Reference**: a bare identifier in argument position resolves to its value — `user-group(owner, "docker")`.
- **Interpolation** `${name}` in **simple strings only**:

```
dir-owner("/opt/hosting", "${owner}:${owner}")   // → "haribo:haribo"
```

- **Triple-quoted strings are RAW** — `${…}` is left verbatim (it is shell/compose syntax the target resolves):

```
file-write("/app/compose.yaml", """
    environment:
      - DB=${DATABASE_URL}      // stays literal: ${DATABASE_URL}
    """)
```

- **Scope**: lexical, with lexical shadowing (a file may shadow a global, confined to that file — no dynamic scoping).
- **Precedence**: `--vars` global `<` inventory (per-host) `<` CLI `--set k=v`. Per-host inventory vars need orchestration-time resolution (planned) — `--vars`/`--set` (global, parse-time) ship first.

See [ADR-0003](adr/0003-variable-scoping.md).
