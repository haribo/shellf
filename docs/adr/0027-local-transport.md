# ADR 0027 — A local transport for the control host

## Status

Active. Adds a second `Transport` (alongside SSH) under the existing seam
(`internal/transport/transport.go`). No change to the agent, engine, or
orchestrator.

## Context

shellf is SSH-only: to run a plan against the machine you are on, you must stand up
an sshd and SSH to localhost. That is a slow dev loop (try a plan before prod) and
blocks a legitimate use — configuring the control host itself.

The transport is a one-method seam:

```go
type Transport interface {
    Run(agentBin string, req []byte) (resp []byte, err error)
}
```

and the agent binary is already `os.Executable()`. A local transport is cheap. The
decisions that need recording are not the plumbing — they are how a plan *names* the
local host, and what `as root` does on your own machine.

## Decision

### 1. An explicit `local: "true"` inventory field selects the local transport

```
host self = { local: "true" }        # no address — it is this machine
```

A host with `local: "true"` is reached by the local transport; `address` is not
required (and ignored if present). The CLI's per-host dial returns the local
transport for such hosts, SSH otherwise.

### 2. The local transport runs the same agent, request, and protocol

`Local.Run(agentBin, req)` executes `agentBin __agent` as a subprocess with `req` on
**stdin** and reads the response from **stdout** — the same ephemeral agent path SSH
already uses for a one-shot job, minus push/deposit/poll (the binary is already here,
nothing to ship or clean up). It is a transport swap and nothing else: the
orchestrator, engine, and agent cannot tell the difference.

### 3. `as root` behaves exactly as on a remote target

`as root` escalates via sudo, unchanged. On the control host that is your own machine,
so sudo may prompt for a password or fail if it needs interaction — identical to a
remote target with interactive sudo. shellf does **not** special-case local privilege:
run as root, or configure passwordless sudo, for a plan that escalates non-interactively.
No local-only behavior, by design.

## Rejected alternatives

- **A reserved address `address: "local"`.** Least ceremony, but silently
  reinterprets a value that could be a real hostname — a footgun. `local: "true"` is
  a distinct connection field (cf. Ansible's `ansible_connection: local`), and leaves
  `address` meaning only what it says.
- **A `--local` CLI flag.** The transport is a property of a *host*, not a whole run;
  a flag cannot express a plan that touches the control host and remotes together, and
  it lives in the wrong layer. Inventory field, not run mode.
- **Any local-only code path** in the orchestrator/engine/agent. It would diverge from
  SSH and rot; the `Transport` seam exists precisely so the layers above stay blind.
- **A `connection: "local" | "ssh" | …` enum** for future transports. YAGNI — only
  local is needed now; a boolean is smaller. A wider enum is its own decision if a
  third transport ever lands.

## Consequences

- `host self = { local: "true" }` runs a plan against the control host with no sshd,
  same reports as SSH (modulo host name), `--check` still inert.
- The diff is confined to `internal/transport/` (a `Local` transport), inventory
  parsing (the `local` field), and the CLI dial wiring. No orchestrator/engine/agent
  change.
- `as root` locally is real sudo — documented, not hidden. The SSH e2e keeps its own
  coverage; a local path gets its own test.
