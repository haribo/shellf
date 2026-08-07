# ADR 0028 — `dir-copy`: deliver a control-host file tree, binary-safe

## Status

Active. Fills the gap [ADR-0019](0019-templates.md) named — *"shellf has no way to
bring a control-host file's content to a target except a `file-write` argument"* —
for a whole tree, and for binary content. Reuses ADR-0019 §2 (control resolves, agent
executes).

## Context

There is no binary-safe, recursive, control-host → target delivery:

- `template(src, dst)` reads a control-host file but renders it as text and ships it
  as `file-write` content; `file-write` runs `printf '%s' "$content" > "$path"` — fine
  for YAML, corrupts a PNG or a font.
- `file-copy(src, dst)` is a target-side `cp` — both paths are on the target.

So delivering a landing page (`index.html` + `assets/`) means one `template` call per
file, and it breaks on any binary asset — the all-shellf story is partly false.

Constraints (from the field report): the mechanism must resolve **control-side** (a
def runs on the target and cannot read the control disk); idempotence must be
**content-based and honest** (per-file sha256, not "dst non-empty" — the
`archive-extract` failure); `--check` must stay inert and preview the exact
added/changed set from the same comparison that gates apply.

## Decision

### 1. `dir-copy(src, dst)` — a control-side instruction

`src` is a directory on the **control host** (relative to the plan file, like
`template`); `dst` is a target directory. It delivers the tree under `src` **verbatim**
(byte-for-byte) into `dst`.

### 2. Resolved control-side into per-file steps

Like `template` (ADR-0019 §2), `dir-copy` is **not** a new agent concept end-to-end.
The CLI walks `src` and rewrites the one `dir-copy` step into many: a `dir-ensure` per
directory and a **`file-put(dstpath, <base64>)`** per file, with the file's bytes
base64-encoded into the step. The agent never sees `dir-copy`.

### 3. `file-put` is a Go builtin, not a shell def

`file-put(path, content)` is a **built-in** instruction (like `file-copy`): the agent
base64-decodes `content` and writes the bytes with an atomic temp-then-rename. It is a
builtin, not a shellf `def`, on purpose — passing a file's base64 through a shell's
environment hits `ARG_MAX` (~2 MB total argv+env), so a def would cap file size at a
few hundred KB. A Go builtin reads the content straight from the Request and never
shells out, lifting that limit to the payload ceiling below. `file-write` stays the
text primitive; `file-put` is its binary sibling.

Idempotence: `file-put` observes `sha256(path) == sha256(decoded content)`. A changed
file drifts and is rewritten; an unchanged one is skipped. `--check` reports per file,
so the preview is exactly the added/changed set — the same comparison that gates apply.

### 4. Payload rides in the Request, under a ceiling

The tree's bytes travel base64-encoded inside the Request (which is deposited as a job
file and read by the agent — not passed on a command line, so no `ARG_MAX` limit). To
bound memory, `dir-copy` **refuses** a tree whose total exceeds a ceiling (**32 MB**
encoded) with a clear error naming the size and limit — never an OOM. Trees larger than
that are a documented follow-on: a streaming **deposit channel** (the mechanism `push`
already uses for the agent binary), out of scope here.

### 5. No prune, no owner, mode not preserved

- **No prune.** Files present in `dst` but absent from `src` are **left**. `dir-copy`
  is additive delivery, not a mirror — pruning would risk deleting unrelated files.
- **Mode not preserved.** Files land `0644`, directories `0755`; compose `file-mode`
  for an executable or a tighter mode. (Keeping mode out of the observe means a target
  `chmod` does not force a re-copy — content is the only convergence field.)
- **Owner not preserved.** The control-host owner is meaningless on the target; files
  are owned by the agent's user (root under `as root`).

### 6. Separate from `template`

`dir-copy` delivers **verbatim** bytes; `template` renders one file's `@{var}`. A tree
that needs some files rendered and some copied uses `dir-copy` for the tree and
`template` for the rendered files, as separate instructions. No mixed mode.

## Rejected alternatives

- **`file-write` for binary.** Its `printf '%s'` corrupts non-text — the gap itself.
- **A stdlib `def dir-copy`.** A def runs on the target and cannot read the control
  disk; delivery must be control-side (ADR-0019 §2).
- **`file-put` as a shell def.** Base64 through a shell env hits `ARG_MAX`; a Go builtin
  does not.
- **Base64 with no ceiling.** A large tree would OOM the agent; refuse with a clear
  error instead. The unbounded answer is the deposit channel, deferred.
- **Pruning `dst`.** A footgun (deletes unrelated files); additive-only, mirror later
  if a real need appears.
- **Preserving owner.** The control owner does not map to the target.

## Consequences

- `dir-copy("site/", "/var/www/app")` delivers HTML **and** binary assets byte-for-byte,
  idempotently, with a per-file `--check`/`status` diff.
- New builtin `file-put`; a control-side resolution pass (in the CLI, where the plan
  dir and the filesystem are) that expands `dir-copy` into `dir-ensure` + `file-put`
  steps. The wire protocol gains no new *concept* (file-put is just another step); the
  agent gains one builtin.
- Bounded by a 32 MB encoded ceiling; larger trees await the deposit-channel follow-on.
