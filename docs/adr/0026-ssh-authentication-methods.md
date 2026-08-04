# ADR 0026 — SSH authentication methods

## Status

Active. Extends the SSH transport (introduced with the agent lifecycle,
[ADR-0005](0005-agent-lifecycle.md)); no prior ADR covered authentication.

## Context

shellf authenticates only with an **unencrypted private key file**: `signer()`
(`internal/transport/ssh.go`) does `ssh.ParsePrivateKey(os.ReadFile(key))`, where
`key` is the inventory `key:` path. There is no `ssh-agent` support and no
passphrase handling, so the standard, safer workflow — an **encrypted** key held by
the agent — is impossible. This excludes a common setup and nudges users toward
keeping a plaintext key on disk.

A clarification that shapes the design: the inventory `key:` field is a **path, not
the key**. A path is not a secret (it is the same on every machine), so carrying it
in the inventory is fine. The problem is not the field; it is that the field is the
*only* way to authenticate. The fix is to add the agent and make `key:` optional —
not to remove the field.

## Decision

### 1. Auth methods, tried in order

The transport offers an ordered list of `ssh.AuthMethod`s; the library tries each
until one authenticates:

1. **Inventory `key:`** — if set, the private key file at that path (a pinned deploy
   key). Highest precedence: an explicit choice wins.
2. **ssh-agent** — if `SSH_AUTH_SOCK` is set, the agent's keys
   (`golang.org/x/crypto/ssh/agent` over that socket). The private key never leaves
   the agent.

If neither is available (no `key:`, no `SSH_AUTH_SOCK`), fail with a clear,
actionable error — never silently.

### 2. `key:` becomes an optional override

`key:` is no longer required. The common case — an encrypted key loaded in the
agent — works with **no `key:` at all**. The field stays for setups that pin a
specific key per host or per group. Docs and examples stop leading with `key:` and
present the agent as the default, `key:` as an override.

### 3. Passphrase-protected key files: secondary

A `key:` file that is encrypted needs `ssh.ParsePrivateKeyWithPassphrase` and a
passphrase source (interactive prompt or an env var). This is a **secondary**
convenience — the agent makes it largely unnecessary — and is added only on a
concrete need, behind a clear passphrase source. Not built by default.

### 4. `~/.ssh/config` is a later follow-on

Honoring `~/.ssh/config` (`IdentityFile`, `IdentityAgent`, `User`, `Port` per host)
is the fuller "just works like ssh" behavior, but it means parsing ssh_config and is
a materially larger surface. Out of scope here; a possible future ADR. The agent is
the 80/20.

## Rejected alternatives

- **Key-file only (status quo).** Excludes the agent and encrypted keys; pushes
  users toward a plaintext key on disk. The gap this ADR closes.
- **Remove the inventory `key:` field.** Misdiagnoses the problem — the field is a
  path, not a secret — and throws away the legitimate pin-a-specific-key case. Keep
  it, demote it.
- **Passphrase parsing as the primary answer.** Makes shellf hold decrypted key
  material and prompt interactively; the agent obviates it. Secondary at most.
- **Parse `~/.ssh/config` now.** Bigger surface than the need; the agent covers the
  common workflow. Deferred.

## Consequences

- The standard workflow (encrypted key + agent) works, with the key never leaving
  the agent — strictly better than reading any key file.
- `key:` is an optional per-host/group override; the docs present agent-first.
- No auth configured → a clear error, not a confusing connect failure.
- Implementation: `signer()` becomes an auth-methods builder — key-file method (if
  `key:` set) then an agent method (if `SSH_AUTH_SOCK` set) — wired into
  `ssh.ClientConfig.Auth`. Passphrase and ssh_config are documented follow-ons.
