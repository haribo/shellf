# ADR 0011 — Privilege escalation: `as <user>`

## Status

Active

## Context

shellf runs a def's `shell { … }` as the SSH user, verbatim, with no escalation.
[ADR-0002](0002-module-distribution.md) assumed "raw shell run on production **as
root**", but real setups SSH as a non-root user (`deploy`, `ansible`, …) with
`sudo`, and escalate per task. The dogfood (#103) hit this immediately: an
inventory `user: deploy` fails on the first `apt-get` (needs root). See #113.

Ansible's model is two layers: `remote_user` (who you connect as) and `become` /
`become_user` / `become_method` (escalate for a task, default `sudo` to `root`).
shellf needs the same separation.

## Decision

### 1. `as <user>` — a block modifier, always before `{`

Escalation is `as <user>` placed **immediately before a `{ … }` block**, the same
way in every form:

```
as root { … }                          # anonymous block
shell as root { systemctl daemon-reload }
on web as root { … }
def install(pkg: str) as root { … }
```

It modifies **how** the block executes, decided **before** running — not a
result. It names the **target user** (`root` in the common case, any user in
general). The escalation **tool** (`sudo` / `doas` / `su`) is inventory
configuration, never part of the keyword.

### 2. Two levels: intrinsic (def) + extrinsic (caller)

- **Intrinsic** — a def declares its own need: `def install(pkg: str) as root { }`.
  Callers do not repeat it: `apt.install(nginx)` escalates on its own. Writing
  `as root { apt.install(nginx) }` would be noise.
- **Extrinsic** — a caller escalates a *generic* def or shell it knows needs root,
  by wrapping: `as root { dir-ensure("/opt/sys") }`. Whether such a def needs root
  depends on the argument (path), so the def cannot decide — the caller does.

Stdlib split:
- **Intrinsic `as root`**: `apt.install`, `service`, `ufw.*`, `docker.*`,
  `user-group`, `dir-owner`.
- **Not intrinsic** (path-dependent): `dir-ensure`, `file-write`, `file-line`,
  `archive-extract`, `git-clone`, `file-download`.

### 3. Always before `{` — no call suffix

`as <user>` **always** immediately precedes a `{ … }` block. A single instruction
call that needs root is **wrapped** (`as root { call() }`), never suffixed
(`call() as root`). One placement rule buys three things: you know the escalation
context **before** reading a (possibly long) body; it matches `def` / `on` /
`shell` head placement; and there is no second rule for call sites. The small
cost of wrapping a lone call is rare — most root-needing defs are intrinsic.

### 4. Composition

A def's own `as root` wins for its internals. An enclosing block's `as root` is a
**default** for the un-annotated shells/instructions inside it. **Explicit beats
inherited.**

### 5. Mechanism

The resident agent keeps running as the **SSH user** (consistent with the
per-user agent path, [issue #114]). `as <user>` wraps *that block's* `shell { … }`
executions in the escalation method read from the **inventory** (`sudo` by
default; `doas` / `su` configurable) — never a hardcoded `sudo`. Escalation is
**per shell execution**, so it stays granular: a `dir-ensure` in the user's home
runs as the user, an `as root` block runs its shells escalated.

### 6. Deferred

- **Become password**: assume passwordless escalation (`NOPASSWD` sudo) first —
  the common setup. Password handling (never in the plan; via a prompt or secret
  store) is a follow-up.
- **Non-root become**: `as <user>` supports any user, but `root` is ~99%.

## Rejected alternatives

- **`sudo` as the keyword** — couples the language to one tool; `sudo`/`doas`/`su`
  is an inventory detail, not language surface.
- **Suffix placement** (`shell { } as root`, `call() as root`) — the modifier
  lands *after* a possibly long body; inconsistent with `def`/`on` head
  placement; and needs a second rule for call sites. `as <user>` before `{` is one
  rule.
- **Escalation on a result** (`if sudo s { }`) — escalation modifies *execution*,
  not an already-run result.
- **Running the whole agent under `sudo`** (root agent) — all-or-nothing; loses
  the granularity the two-level model needs (a home-dir `dir-ensure` would
  needlessly run as root). Per-block escalation keeps it granular.
- **`become` / `root` as the keyword** — `become` is jargon; `root` cannot name
  another user. `as <user>` reads naturally and generalizes.

## Consequences

- **Grammar**: an `as <ident>` modifier before a `{ … }` block on — an
  **anonymous block** (a new *sequential-group* step; today a `{ … }` only exists
  bound to `on`/`if`/`parallel`), `shell`, `on <target>`, and a `def` signature.
- **Types**: `proto.Step` gains a `Become` field; a new sequential-group step
  carries the anonymous block; the def AST gains the intrinsic become.
- **Evaluator/agent**: the become context flows to `shell { }` execution; the
  agent wraps the exec in `<method> [-u <user>]` (method from the inventory).
  Agent stays the SSH user; escalation is per-shell-exec.
- **Inventory**: an escalation-method field (default `sudo`).
- **Amends** [ADR-0002](0002-module-distribution.md) (which assumed root SSH).
- Implemented in follow-up `type: feature` issue(s); addresses #113. This ADR is
  docs-only.
