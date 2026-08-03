# ADR 0025 — Secrets at rest: deposit the workdir on tmpfs

## Status

Active. Closes the at-rest residual left open by
[ADR-0018](0018-secrets.md) (§ "Residual"), which deferred at-rest secrecy to a
later ADR. The redaction, `umask 077`, 0600/0700, and delete-after of ADR-0018
stand; this ADR changes only *where* the job files live.

## Context

A run's request is deposited as a **file** in the agent's workdir on the target,
and the agent's result is written back there. With `--secret-file`/`--secret-env`
(ADR-0018) those files carry secret **values** — the request inlines them into the
Steps JSON, and a result can echo them. ADR-0018 hardened this (owner-only 0600,
deleted after the job) but left a residual: the plaintext still touches the target
**disk**, so it can survive in a filesystem backup, a disk snapshot, or a
forensic undelete of the deleted file.

Two structural facts bound what is achievable:

- **The job is a file, by design.** The resident agent (ADR-0005) is detached and
  reads its job from disk precisely so a job survives an SSH drop. Streaming the
  request over stdin instead would break that durability — the agent is not
  attached to the depositing session.
- **Against live root, nothing on the target is secret.** Root reads any file, any
  process's `/proc/<pid>/environ`, and process memory. No on-target scheme
  (including encryption whose key must also reach the target) changes that.

So the achievable win is narrow but real: keep secret plaintext off **persistent
storage**, closing the backup / snapshot / undelete surface. That is exactly what
RAM-backed `tmpfs` gives.

## Decision

### 1. The workdir lives on tmpfs

The agent **workdir** — request, result, and pid files — is deposited under
`/dev/shm` (a RAM-backed `tmpfs` present on Linux targets) instead of `/tmp`.
Secret plaintext in the request, and any secret a result echoes, therefore never
touch persistent disk: not in a root-filesystem backup, not in a disk snapshot,
and not recoverable by undelete. tmpfs is cleared on reboot, which also ends the
agent — no durability is lost (a job already survives SSH drops, not reboots).

### 2. The binary cache stays on `/tmp`

The agent **binary** is not secret and benefits from a persistent, reused cache,
so it stays at `/tmp/shellf-agent-<id>` (unchanged). Only the workdir moves.

### 3. Fallback to `/tmp` when tmpfs is absent

If `/dev/shm` is not a writable directory, the workdir falls back to `/tmp` and the
run proceeds with the ADR-0018 residual (owner-only, deleted after). The choice is
probed once per run over the existing connection (`test -w /dev/shm`).

### 4. `clean` covers both locations

`shellf clean` removes `/dev/shm/shellf-*` as well as `/tmp/shellf-*`, and kills any
resident agent found in either.

## Rejected alternatives

- **Encrypt secret values in the request at rest.** The decryption key must reach
  the target too — where root reads it like anything else — so it is theater
  against the one attacker who can read the disk live. It also fights the model:
  the resident agent polls jobs from disk, with no channel to hand a per-job key to
  an already-running agent. No real gain, real complexity.
- **Stream the request over stdin (no file).** Breaks the detached resident agent's
  durability (ADR-0005): the agent reads its job from disk so it survives an SSH
  drop, which a stdin pipe tied to the session cannot.
- **Overwrite/`shred` the file on delete.** Helps only against undelete, not
  backups or snapshots, and `shred` is unreliable on modern (CoW/journaled)
  filesystems. tmpfs closes all three at once.

## Consequences

- Secret plaintext (request) and secret echoes (result) live only in RAM-backed
  storage — off persistent disk, so outside backups, snapshots, and undelete.
- The live-root residual is **unchanged and still documented**: root on the target
  reads tmpfs and `/proc` in real time. This ADR narrows the at-rest surface, it
  does not claim secrecy against root.
- Transport: the workdir base becomes `/dev/shm` (probed, `/tmp` fallback); the
  binary path is unchanged; `clean` covers both. No wire-protocol or agent-logic
  change.
