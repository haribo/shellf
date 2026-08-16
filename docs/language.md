# shellf — language spec

> Living doc. Only **stable** primitives are documented here. Instruction syntax is still moving → not yet included.

## `Result` — an instruction's outcome

What every instruction returns (implicit — `-> Result` is omitted). A tagged union, **not** a `ShellResult`.

```
Result = ok.<tag>(payload?) | err.<tag>(payload?) | would.<tag>(payload?)
```

- `ok` — desired state reached. `err` — failure. `would` — check mode: the engine *would* have acted (derived, never authored).
- Comparison at two levels: `== ok` matches the category (idempotence); `== ok.installed` matches the exact tag.
- Open set: `err.runtime(shellResult)` is mandatory (the shell fails in unbounded ways).
- Optional payload carries data — often a `ShellResult` for diagnostics (`err.runtime(r)`).

### `Result` vs `ShellResult` — do not conflate

| | `ShellResult` | `Result` |
|---|---|---|
| Level | raw shell mechanics | instruction's semantic decision |
| Shape | `{ exit, stdout, stderr, ok }` | `ok`/`err`/`would` + tag + optional payload |
| Produced by | a `shell { }` block | a `def` instruction |

An instruction **reads** the `ShellResult` and **translates** it into a `Result` (via `when`/tags). A `Result` may *carry* a `ShellResult` in its payload; it is not one. Flattening `Result` to exit/stdout/stderr = branching on exit codes = plain bash — the exact regression shellf exists to avoid.

## Phases and modes

A `def` declares phases; a run picks a mode. Which phases a mode runs (ADR-0035):

| Mode | `check` | `observe` | `preview` | `apply` |
|---|---|---|---|---|
| `shellf run` | yes | yes | no | yes |
| `shellf run --dry-run` | yes | yes | yes | **no**, except below |
| `shellf status` | yes | yes | no | no |

`check` decides before acting — its outcome wins and halts. `observe` reports current
state; equal to the desired one means the apply is skipped. `preview` describes what the
apply would do and runs only in `--dry-run`. `apply` acts.

**The exception (ADR-0041).** A def with no `observe` whose `apply` contains only
primitives, control flow and `return` *is* evaluated in `--dry-run` — every primitive is
inert there, so nothing can happen. The verdict then comes from what the primitives found:
`ok.already` when nothing would change, `would.<tag>` when something would. This is how
`file.copy`, `dir.copy` and `dir.sync` report honestly on a converged host instead of
announcing writes that would not happen.

Put a `shell { }` in that apply and the exception lapses — a shell can do anything, so the
def falls back to `would.<tag>` on every dry-run. That is the cost of reaching for the
shell where a primitive would do, and it is the only place the choice shows.

An `apply` **must end with a `return`** naming what it did (ADR-0037 §1):

```
apply {
    r = shell { apt-get install -y "$pkg" }
    if !r { return err.runtime(r) }
    return ok.installed          # required: the verdict is never implicit
}
```

## Delegation — a def that *is* another def (ADR-0037 §2)

A def may hold **one** call outside every phase. It then *is* that def with rebound
arguments: the callee's own phases run, in every mode.

```
def template(src: str, dst: str) {
    file.write(dst, ~file.render(src))
}
```

Why not put the call in `apply`: the table above says `apply` does not run in
`--dry-run`. A call placed there loses the callee's `check` and `observe`, so a wrapper
previews a write on a host it would not touch. A delegation keeps them.

Three rules, each a parse error when broken:

- **Exactly one call.** Two calls are a sequence of effects, which is what `apply` is for
  — and they have no single verdict.
- **Only `check` beside it.** `observe`, `preview` and `apply` each answer a question the
  callee already answers. You delegate the decision or you make it.
- **No `shell` in the arguments.** They are evaluated in `--dry-run` too, because the
  callee's `observe` needs their value; a shell there would run for real.

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

shellf injects `set -e` at the top of every block, so `exit` is the code of the **first**
failing command (else `0`). Without it, `exit` would be the last command's and a mid-block
failure would be masked.

**`set -o pipefail` only under `bash`.** It is a bashism — `dash`, which is `/bin/sh` on a
Debian target, does not have it. So in a default `sh` block, `cmd | grep x` reports the
exit code of `grep`, and a failure of `cmd` is invisible. Write `shell(bash) { … }` when a
pipeline's left-hand side must be able to fail the block.

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
if s { … }                       // `.ok` is exit == 0; `if s` / `if !s` reads it

// tag a failure explicitly: name the outcome the block yields when it fails
r = shell { … }
if !r { return err.runtime(r) }
```

### `unsafe shell` — shell a def already does (ADR-0040)

A `shell { }` block whose command has an **exact** def equivalent does not parse. The def
is re-runnable by construction — it observes, it reports, it converges — and a step that
tolerates being re-run is what makes a failed run recoverable instead of wedged:

```
shell { mkdir -p /opt/app }
→ mkdir here — dir.ensure(path) is idempotent and previewable.
  Write `unsafe shell { … }` to keep the shell.
```

Two rules today: `mkdir` → `dir.ensure`, and `cp` → `file.copy` for a file or `dir.copy`
for a tree (neither carries mode or ownership — that is `file.mode` / `dir.owner`).

`unsafe shell { … }` keeps the shell, and runs exactly like `shell { … }`. It composes
with the interpreter override and works in a condition:

```
unsafe shell { mkdir /var/lock/deploy || exit 1 }     // an atomic lock: no def does this
unsafe shell(bash) { … }
if !unsafe shell { … } { … }
```

**`unsafe` does not mean dangerous.** It means shellf cannot vouch for what the block
does — the lock above is irreproachable shell that no def replaces, and it is marked,
correctly. That is what keeps the word usable, and what makes `grep -r 'unsafe shell'` the
list of every place shellf's guarantees stop, imported modules included.

The detector is a heuristic and does not claim otherwise: `$CMD`, `eval`, `xargs`,
`install -d` and `find -exec` go through it. It runs on plans and on your own defs; the
standard library is exempt, since it is the layer that reaches the system.

## Variables

Immutable bindings, **no keyword** — same syntax in plans, vars files, and `def` bodies:

```
owner = "haribo"
r = shell { usermod -aG docker "$owner" }
```

- **Immutable**: no reassignment (which is why no `let`/`const` is needed).
- **Reference**: a bare identifier in argument position resolves to its value — `user.group(owner, "docker")`.
- **Interpolation** `${name}` in **simple strings only**:

```
dir.owner("/opt/hosting", "${owner}:${owner}")   // → "haribo:haribo"
```

- **Triple-quoted strings are RAW** — `${…}` is left verbatim (it is shell/compose syntax the target resolves):

```
file.write("/app/compose.yaml", """
    environment:
      - DB=${DATABASE_URL}      // stays literal: ${DATABASE_URL}
    """)
```

- **Scope**: lexical, with lexical shadowing (a file may shadow a global, confined to that file — no dynamic scoping).
- **Precedence**: `--vars` `<` plan binding `<` inventory (per-host) `<` CLI `--set`. A **bare-identifier** argument resolves **per host at orchestration time** (so a per-host inventory var can override a global); an undefined one errors at orchestration. `${name}` **interpolation is global** (resolved at parse) — a per-host var cannot be interpolated. Per-host vars are free-form fields in the inventory: `host web1 = { address: "…", owner: "alice" }`.

See [ADR-0003](adr/0003-variable-scoping.md).

## Control flow — `if` / `else`

```
if dir.exists("/opt/app") {     // condition = an instruction; branch on its Result being ok
  apt.install("nginx")
} else {
  apt.install("apache")
}
```

- The **condition is an instruction** (or a `shell` block); the branch is taken on its Result being `ok`.
- The condition runs **on the target** — the agent interprets the flow.
- A failing condition takes `else` (or is skipped): the `if` **captures** the result, so it does **not** halt (halting rule).
- **Negation**: `if !<cond> { … }` flips the branch. This replaces the old `unless` guard (**removed from plans**): `shell { cmd } unless { g }` becomes `if !shell { g } { shell { cmd } }`, and `if !dir.exists("/opt") { dir.ensure("/opt") }` acts only when absent.
- **Preview** (`--dry-run`): a `would` condition (an effect not applied) makes the branch **`undetermined`** — honest, never guessed. An `ok`/`err` condition is deterministic. See [ADR-0004](adr/0004-control-flow-preview.md).

Put the effect **inside** the `if` (not a separate action followed by a `test`) so the preview stays honest.

### Capturing a result

```
x = dir.ensure("/opt/app")   // capture the instruction's Result under `x`
if x.changed {               // acted this run (apply ran, not a converged skip)
  service.ensure("nginx", true, true)
}
if x { … }                   // sugar for `if x == ok`
if x != ok { … }             // `!=` negates
```

- **Outcome test** (ADR-0008): `x == ok` / `x == err` match the category; `x == ok.created` / `x == err.dbLocked` also match the tag (tag omitted = any tag of that category). `!=` negates; bare `if x` = `if x == ok`.
- `.changed` = it actually acted (apply ran, not a converged skip). It is **orthogonal** to the outcome category, so it stays a field, not a pattern.
- The old `.ok` / `.err` field tests are **removed** — use `== ok` / `== err`.
- In `--dry-run`, a captured `would` result makes the branch **`undetermined`** — same never-lie rule.
- A capture is block-scoped; capturing an `if`/`parallel` is rejected.

### Handling errors — `?`

By default a step that returns `err` **halts** the plan (and its host): nothing is
built on a broken base. To handle a *specific* error instead, mark the
instruction with `?` and test its result — captured or inline:

```
x = apt.install("nginx")?          // `?` = "I handle this; don't halt automatically"
if x == err.dbLocked { retry() }   // handled → the plan continues
                                   // any other error → covered by nothing → halt

if apt.install("nginx")? == err.dbLocked { retry() } else { report() }  // inline
```

- `?` **defers** the halt so a following `if` can test the error — *"let me try to
  handle it"*, not *"ignore it"*.
- An error **covered by no branch** (no matching `== err.<tag>`, no `else`)
  **halts** — no error passes silently.
- `else` is the explicit catch-all (it also runs on `ok`).
- Testing `== err[.tag]` **requires** a `?` on the source: without it, halt-on-err
  makes the branch unreachable, so it is a **compile error**. `== ok` / `!= err`
  need no `?`.

See [ADR-0009](adr/0009-error-handling.md).

### Read-only questions

`dir.exists` / `file.exists` are **questions**: read-only defs with **no `apply` phase**, so they resolve in pass 1 and are **deterministic in check** (never `undetermined`), unlike an effectful instruction.

```
if dir.exists("/opt/app") {   // present → then, absent → else — deterministic even in --dry-run
  apt.install("nginx")
}
```

The name distinguishes read from write — `-exists` questions vs `-ensure`/`-owner` instructions. No keyword: a question is simply a def whose decision lives entirely in read-only phases.

## Loops — `for` (ADR-0017)

`for <var> in [<str>, …] { … }` repeats its body over a **literal list**. The loop
variable is referenced with `${var}`.

```
on host {
  for port in ["80", "443"] { ufw.open("${port}", "tcp") }
  for svc in ["traefik", "app"] { file.mode("/opt/${svc}/run", "755") }
}
```

- **Parse-time unrolling**: the loop expands to one copy of the body per item
  before anything runs — `--dry-run` and `status` show each iteration. There is no
  runtime loop and no list value.
- `${var}` works anywhere a string does, including inside one (`/opt/${svc}/run`).
  A **bare** `var` would be a per-host ref, not the loop item — always use `${var}`.
- The body is a normal block (instructions, `if`, `shell`, nested `for`). It is
  captured as raw balanced braces, so — like a `shell {…}` block — a lone
  unbalanced `}` in a string ends it early. The loop var does **not** interpolate
  inside a raw `shell {…}` body (that stays literal shell).
- Lists are literals of strings; glob/range iteration and list variables are not
  in this version.

## Delivering a tree — `dir.copy` (ADR-0039)

```
dir.copy(%"assets/site", "/var/www/site")
dir.copy(%"assets/site", "/var/www/site", "sha256")
```

The source is read on the control host, so it carries `%"…"`; an unmarked path would name
a directory on the target, which is a different operation.

The agent sends what it already has, the control host answers only what differs. A
converged tree therefore transfers **zero bytes** and reports `ok.already` — not merely
"wrote nothing". There is no size limit: files stream in chunks, and each is written
beside its destination and renamed once complete, so a dropped connection never leaves a
half-written file.

`compare` decides what "identical" means:

| value | compares | limit |
|---|---|---|
| `"meta"` (default) | size + mtime | **misses a change that preserves both** — a restored backup with preserved timestamps is the realistic case |
| `"sha256"` | content | reads every file on both sides |

The default's limit is documented rather than discovered: when it matters, pass
`"sha256"`. What is on the target and absent from the source is left alone — a copy is a
sync that deletes nothing.

### `dir.sync` — the same transfer, and it deletes

```
dir.sync(%"assets/site", "/var/www/site")
```

One word apart from `dir.copy`, and that word removes: everything on the target and absent
from the source is deleted. Use it when the destination must *match* the source, not
merely contain it.

`--dry-run` names what it would remove, one file per line, before removing anything:

```
dir.sync(dst=/var/www/site, src=site) would.synced
    preview ▸ 2 file(s) would be transferred; 2 file(s) would be REMOVED from the target:
    preview ▸   - old-page.html
    preview ▸   - stale/asset.css
```

That preview is not a nicety. A destructive instruction whose dry-run says nothing tells
the operator what they lost only afterwards.

## Per-call override — `with { … }` (ADR-0022)

Any instruction call — a def, `shell`, `file.template`, or a builtin — may be followed
by `with { k = <value>, … }` to add or override variables **for that call only**:

```
on host {
  # explicit, local inputs — no need to read the file to know what it uses
  file.template(%"nginx.conf", "/etc/nginx/a.conf") with { port = "8080", root = "/srv/a" }
  file.template(%"nginx.conf", "/etc/nginx/b.conf") with { port = "8081", root = "/srv/b" }

  shell { echo "$msg" } with { msg = "hi" }
}
```

- The bindings do **not** leak beyond the call.
- Values are strings, interpolated with the global variables (`${var}`) at parse.
- A `with` binding wins over a same-named global (and, for a def, over the passed
  argument): it is the most local scope. Precedence: `with` > per-host > global.
- It reaches a def's / `shell`'s body as an environment variable (`$k`) and a
  template's render scope (`@{k}`).

### Template render scope (ADR-0024)

A `file.template(src, dst)` file is rendered **per host**, over that host's full
variable scope — `--vars`, plan bindings, **per-host inventory vars**, `--set`,
secrets — plus the call's `with { }`. `dst` may be a bare per-host ref
(`file.template(%"nginx.conf", conf_path)`); `src` is always a literal control-host
path. A `for` loop variable is **not** in that scope, so to use the loop item
inside a template's content, pass it with `with { }`:

```
for svc in ["traefik", "app"] {
  file.template(%"unit.tmpl", "/opt/${svc}/unit") with { svc = "${svc}" }
}
```

`${svc}` resolves to the item at parse; the template then renders `@{svc}`. (The
`dst` and other string args already interpolate `${svc}` without `with` — only
the template *file's* content needs the explicit pass.)
