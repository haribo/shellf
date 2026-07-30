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
- **Precedence**: `--vars` `<` plan binding `<` inventory (per-host) `<` CLI `--set`. A **bare-identifier** argument resolves **per host at orchestration time** (so a per-host inventory var can override a global); an undefined one errors at orchestration. `${name}` **interpolation is global** (resolved at parse) — a per-host var cannot be interpolated. Per-host vars are free-form fields in the inventory: `host web1 = { address: "…", owner: "alice" }`.

See [ADR-0003](adr/0003-variable-scoping.md).

## Control flow — `if` / `else`

```
if dir-create("/opt/app") {     // condition = an instruction; branch on its Result .ok
  apt-install("nginx")
} else {
  apt-install("apache")
}
```

- The **condition is an instruction** (or a `shell` block); the branch is taken on its Result `.ok`.
- The condition runs **on the target** — the agent interprets the flow.
- A failing condition takes `else` (or is skipped): the `if` **captures** the result, so it does **not** halt (halting rule).
- **Preview** (`--check`): a `would` condition (an effect not applied) makes the branch **`undetermined`** — honest, never guessed. An `ok`/`err` condition is deterministic. See [ADR-0004](adr/0004-control-flow-preview.md).

Put the effect **inside** the `if` (not a separate action followed by a `test`) so the preview stays honest.

### Capturing a result

```
x = dir-ensure("/opt/app")   // capture the instruction's Result under `x`
if x.changed {               // acted this run (apply ran, not a guard-skip)
  service("nginx", true, true)
}
if x { … }                   // `if x` is sugar for `if x.ok`
```

- `.ok` = the instruction succeeded; `.changed` = it actually acted (apply ran, not skipped by its guard).
- In `--check`, a captured `would` result makes `if x.ok` / `if x.changed` **`undetermined`** — same never-lie rule.
- A capture is block-scoped; capturing an `if`/`parallel` is rejected.
