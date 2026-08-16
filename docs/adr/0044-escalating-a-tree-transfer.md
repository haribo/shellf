# ADR 0044 — A tree transfer escalates by re-invoking the agent, with bounded verbs

## Status

Active. **Extends [ADR-0011](0011-privilege-escalation.md)**, whose `as <user>` reached
only what runs through the executor, and [ADR-0039](0039-tree-transfer-protocol.md), which
defined the transfer without saying who owns what it writes. Neither is reversed.

## Context

Measured against the e2e container, both lines inside one `as root` block, delivering into
a directory the connecting user *can* write (#390):

```
as root                                                ok
  file.write(path=/opt/writable/by-file-write)  ok.written
  dir.copy(dst=/opt/writable/delivered)         ok.copied

-rw-r--r-- 1    0    0  by-file-write         ← root, as asked
drwxr-xr-x 2 1000 1000  delivered/            ← deploy
-rw-r--r-- 1 1000 1000  delivered/file.txt    ← deploy
```

Exit 0. The escalation was ignored for one of the two, and the report says `ok.copied`
either way. Into a directory the connecting user cannot write, the same plan fails outright
— `dir.sync: mkdir /opt/root-owned/delivered: permission denied` — which is the same defect
being loud instead of silent.

The cause is structural, not a missing call. `as <user>` is carried by the executor, which
prefixes `sudo`/`doas` around a shell (`internal/engine/exec_shell.go:99-129`). An
instruction that reaches the filesystem **through a shell** therefore escalates;
`~file.write` does, by staging bytes in a temp file and placing them with
`ex.Shell(placeCmd, …)` (`internal/engine/fileput.go:81-95`). A transfer does not: the
agent writes with `os.MkdirAll`, `os.CreateTemp`, `os.Rename`, `os.Remove`
(`internal/agent/sync.go:231-302`), and never receives an executor at all — `TreeSyncer`
does not take one (`internal/lang/eval.go:241`) and the agent wires `ch.Sync` bare
(`internal/agent/agent.go:341-345`).

Two consequences that shape the decision:

- The write is not the only escalated operation needed. The delta is computed by reading
  the destination first (`scanTarget`, `internal/agent/sync.go:69`), so a destination the
  connecting user cannot *read* — a `0700` directory — fails before anything is written.
  A fix that escalates only the placement leaves that case broken.
- Reproducing the placement in shell means writing staging, atomic rename, mode and mtime
  preservation, and deletion a second time, in a second language. This project has a name
  for that: #378 removed an `observe` that re-decided what the primitive already knew,
  after it drifted and lied for months.

## Decision

### 1. A transfer escalates by re-invoking the agent binary through the executor

The agent runs its own binary as a child process, launched with `ex.Shell`. The executor
supplies whatever `as <user>` means for this run — `sudo -n`, `doas`, a non-root user, or
nothing at all — so escalation is not re-implemented here and stays defined in one place
(ADR-0011).

The Go code that places files is therefore the same code, running as the right user. There
is no shell reimplementation of staging, renaming, modes, mtimes or deletion.

### 2. The escalated child gets bounded verbs, never the channel

Two sub-commands, and nothing else:

| Verb | Takes | Returns |
|---|---|---|
| `__sync-scan <dst> <compare>` | a destination path | the manifest, on stdout |
| `__sync-commit <staging> <dst> [--delete]` | a staging directory and a destination | what it wrote and removed |

The escalated child **holds no socket, speaks to no control host, parses no plan and runs
no def**. The unprivileged agent keeps the dialogue: it asks for the delta, receives the
bytes, and writes them into a staging directory it owns. Root is used to read the
destination and to put files in place, nothing more.

The rejected shape is worth naming because it is the obvious one: running the whole
transfer under the escalated child. That puts a root process in conversation with the
network for the length of a deployment, to save an intermediate directory.

### 3. The staging directory belongs to the unprivileged agent

It sits in the agent's own workdir, created and written by the agent as itself. The
escalated child only reads it. A staging area writable by the escalated side would be a
way to hand root a file chosen by someone else.

The final placement still happens beside the destination, as `~file.write` already does
(`internal/engine/fileput.go:62-64`): a rename across filesystems is not atomic, and #389
exists because a non-atomic replacement is observable by a daemon reloading its config.

### 4. Before escalating, the binary is re-verified

#391 already refuses an agent binary that is not ours and a workdir another user can write
to — checks written when the agent ran
unprivileged (`internal/transport/ssh.go:281-308`). Under `sudo` the same weakness stops
being a foothold and becomes the machine: whoever can replace that file chooses what root
executes.

So the path being launched is checked immediately before the escalation — owner, and not
writable by anyone else — and a failure refuses the transfer rather than downgrading it.
A refusal here is not a fallback to the unprivileged path: that is what produces the
wrong-owner success this ADR exists to remove.

### 5. One path, escalated or not

The child is used whether or not `as <user>` is set; with no escalation the executor adds
no prefix and the child runs as the agent. Keeping a second, in-process path for the
common case would mean the escalated one is the one nobody exercises — and it is the one
that touches root.

### 6. Until it ships, an escalated transfer is refused

Deciding is not delivering, and the behaviour in between is a decision of its own. A
`~dir.sync` reached with an escalation in force **fails**, naming the escalation it cannot
honour and what to do instead (deliver unescalated, then `dir.owner`). In `--dry-run` too:
a preview that announced a transfer the real run would refuse is the same lie one step
earlier.

This breaks plans that pair `as <user>` with a delivery today and live with the resulting
ownership. That is the point — those plans are already not doing what they say, and a
refusal is how they find out before the next deployment rather than after.

### 7. What this does not claim

Nothing here makes a transfer safer to *aim*: `as root { dir.sync(%"x", "/etc") }` still
deletes what the source does not have, as root. ADR-0039 §5 decided that, and `--dry-run`
naming every deletion is the guard.

## Rejected alternatives

- **Place each staged file with `FilePut.Apply`.** Reuses shipped, tested code, and costs
  one escalation and one shell **per file** — `dir.sync` exists for trees where that is
  hundreds. Rejected on cost, not on correctness.
- **One escalated shell script looping over the staged files.** One `sudo`, but it
  re-implements staging, atomic rename, mode, mtime and deletion in shell, beside the Go
  that already does it. Two implementations of one rule, free to drift; and it still
  cannot read a destination the agent cannot read.
- **Run the whole transfer, channel included, in the escalated child.** Simpler to write
  than the split, and it hands a root process the network conversation for the duration of
  a job. The split costs one staging directory.
- **Refuse a transfer inside `as <user>`, and stop there.** Honest, and it removes a
  capability people need — delivering a tree into a root-owned directory is ordinary. It
  is adopted as the interim behaviour in §6, and is not the destination.
- **Do nothing, document the limitation.** The failure mode is a run that reports success
  having disobeyed. A documented lie is still a lie.

## Consequences

- The agent binary gains two hidden verbs, alongside `__agent`, `__agent-resident` and
  `__bridge`. They take paths and flags only.
- A transfer costs one extra process, and two when it also scans — measured against a
  per-file escalation, which is what the alternative would have cost.
- A destination readable and writable only by root now works under `as root`, which is the
  case #390 opened on and the one an operator hits first.
- `TreeSyncer` gains an executor, so `~dir.sync` learns what every other instruction
  already knows: who it is running as.
- The e2e harness gains the reproduction as a step: both primitives in one `as root`
  block, asserting ownership rather than the exit code — the exit code was already 0 while
  the tree landed wrong.
