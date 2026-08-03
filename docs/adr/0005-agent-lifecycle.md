# ADR 0005 — Agent lifecycle

## Status

Active

## Context

The transport pushes the agent binary to each target and runs it. Two questions kept coming up — how long does the agent live, and what cleans up after it — and the answer was **debated and re-opened several times**. This ADR anchors the decision so it is not re-litigated. See issue #46.

## Decision

The agent is a **transient resident** process — *not* a supervised daemon, and *not* an ephemeral per-job process.

- **Binary cached on disk.** Pushed once, named by a hash of its bytes (`/tmp/shellf-agent-<hash>`); later runs reuse it — no re-transfer (PR1, done).
- **Stays alive between jobs.** After finishing a job the agent does not die; it waits for the next job (from a later run) by **watching a request file in `/tmp`** — no listening socket.
- **Detached execution.** A job runs detached from the SSH session, so it **survives a connection drop mid-job**; the control reconnects and collects the result.
- **Self-terminating, zero trace.** After **inactivity (a settable, long TTL)** the agent **erases everything** — its workdir *and* its own binary — and **self-kills**. No trace of shellf remains. Re-transfer is avoided **not** by keeping the binary but by the **long TTL**: within it the agent and its binary stay alive, so runs of the same working session (e.g. morning then evening) do **not** re-transfer; past it, everything is erased. A single run after a long idle period re-transfers once — the accepted price of leaving no trace. An explicit `clean` (#69) forces the wipe early.
- **Nothing after reboot.** The agent is not a supervised service; a reboot clears `/tmp` and nothing relaunches it.

This is a **transient** resident: it lives across a burst of runs, then removes itself. It is the settled model — **do not re-open**.

## Rejected alternatives

- **Ephemeral per-job** (the process dies after every job; residues purged at the next run). Simpler, no resident process — but it re-spawns each job, never reuses the warm agent, and its cleanup depends on a *next* run ever happening. Rejected: the chosen design keeps the agent reusable and **self-cleaning without needing a next run**.
- **Supervised daemon** (a systemd unit relaunched at boot). Rejected: it would **install and supervise a service** and survive reboots — against "nothing installed on the target". The chosen agent is unsupervised, self-terminating, and gone after reboot. (Earlier framing of the chosen design as a "daemon" was imprecise — it is not.)
- **Listening socket** for the rendezvous. Rejected: adds a network surface and its own auth; a watched file in `/tmp` + re-SSH is enough.

## Consequences

- Delivered in the #46 epic: binary cache (PR1, done), detached execution + file-based wakeup/poll (PR2), inactivity self-kill + residue cleanup (PR3).
- Rendezvous is **file-based in `/tmp`**; the inactivity TTL is **configurable**.
- The lifecycle is fixed at "transient resident, self-terminating" — future changes are a **new ADR that supersedes this one**, per ADR-0001 §4.
