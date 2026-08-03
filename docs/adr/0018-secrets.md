# ADR 0018 — Secrets: file/env input, value redaction, honest target limits

## Status

Active.

## Context

shellf has no secrets story, and the naïve one leaks. A value passed as an
argument (a DB password, an rclone token) appears:

1. **in shellf's output** — argument values are rendered in step labels
   (`file-write(rclone_pass=S3cr3t!, /etc/rclone.conf)`), so they land in the
   terminal, run reports, `--check`, `status`, and **CI logs**;
2. **on the target's disk** — the per-host `Request` JSON is written to
   `/tmp/shellf-<hash>-<user>/req-*.json` in cleartext during the job;
3. **in the target's process env** — args are injected via the environment, so a
   value is visible in `/proc/<pid>/environ` while a shell runs;
4. **in a shell's stdout** echoed back into the response.

And `--set pass=…` also leaks into shell history and `ps`.

A *fully* secure story (the secret never readable on the target) needs request
encryption with key management, or a stdin-only transport with no resident agent
— a large undertaking. This ADR does the high-value, honest subset and states
plainly what it does not cover.

## Decision

### 1. Secrets are entered from a file or an env var, never inline

```
shellf run plan.shellf --secret-file rclone_pass=./secrets/rclone.txt
shellf run plan.shellf --secret-env  db_pass=DB_PASSWORD
```

`--secret-file name=path` reads the value from a file; `--secret-env name=VAR`
from an environment variable (both repeatable). The value is **never** in the
command line (so not in `ps`/history) and **never** in the plan or lockfile (so
not committed to git). A secret joins the highest-precedence variable tier (like
`--set`) for resolution.

### 2. Value-based redaction everywhere shellf prints

Every secret **value** is replaced with `***` in all of shellf's output — step
labels, run/check reports, `status`, and a shell's echoed stdout in a report.
Redaction is **by value** (the string is masked wherever it appears), so it
catches the secret even when it surfaces indirectly. This closes surfaces #1 and
#4 (the control-host and log leaks — the widest).

An empty secret is not redacted (masking the empty string would erase
everything).

### 3. Target hardening — reduced, not eliminated (stated honestly)

The secret still reaches the target: it must, to be written into the config it
provisions. shellf minimises the exposure but does **not** claim the secret never
touches the target:

- the request file is created `0600` and the workdir is already `0700`
  (unreadable to other non-root users), and it is deleted promptly after the job;
- the workdir lives on tmpfs, so the plaintext never touches persistent disk
  ([ADR-0025](0025-secrets-at-rest-tmpfs.md) — closes backups/snapshots/undelete);
- **root on the target can still read** the request file (while it exists) and the
  process environment during execution. This is documented, not hidden.

Making the secret unreadable even to **live target root** is not achievable on the
target (root reads memory and `/proc`); ADR-0025 narrows the at-rest surface as far
as it honestly can.

## Rejected alternatives

- **Inline `--secret name=value`.** Leaks into shell history and `ps`; the whole
  point is to keep the value off the command line. File/env only.
- **Secrets in the plan or a vars file.** Committed to git — the opposite of a
  secret. They enter at run time from file/env.
- **Redaction by parameter name** (mask the arg of known-secret params). Misses a
  secret that surfaces elsewhere (a shell's stdout, a derived value). Value-based
  is robust.
- **Claiming full target secrecy in v1.** Dishonest — the secret is on the target
  disk and env. We reduce and disclose; encryption is a separate ADR.

## Consequences

- The control-host and log leak (the widest surface) is closed: secrets show as
  `***` in every report, preview, and status line.
- Secrets come from files/env at run time, so nothing secret is committed.
- The target still sees the secret (env + the request file); shellf hardens
  (0600 + prompt delete) and **documents** the residual root-visible exposure
  rather than pretending it away.
- Implementation: the CLI collects secret values (file/env) into the
  highest-precedence var tier and into a redaction set; the report renderers mask
  those values. Target file-permission hardening is a small, separate change.
- Follow-on: at-rest secrecy on the target is addressed by
  [ADR-0025](0025-secrets-at-rest-tmpfs.md) (tmpfs workdir), which keeps the
  plaintext off persistent disk; live-root exposure is inherent and stays
  documented.
