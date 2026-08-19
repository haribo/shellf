# shellf — design

> **What this document does:** it says *why shellf is built this way* — the product thesis,
> the architectural bets, what is still open. It does not describe the language: that is
> [`language.md`](language.md). The history of decisions, with their rejected
> alternatives, lives in [`adr/`](adr/) — not here.
>
> Status: language **implemented** (interpreter, SSH transport, resident agent, embedded
> stdlib). Name `shellf` (working name, available).

Legend: 🟠 **to settle** · 🔴 **risk / hard problem**

---

## 00 · Product thesis

### The pitch

"**Raw shell, but idempotent, previewable, and fast.**"

With Ansible/Puppet/Chef, falling back to `shell` is the defeat (idempotence is lost). Yet
everybody falls back to it. shellf is built **around** that reality, not against it.

### The three architectural bets

- **Agentless, a single binary.** The user needs nothing but the binary and SSH access.
  Nothing to install on the targets.
- **A self-erasing resident agent that evaluates on the target.** Pushed over SSH, cached
  by hash and left **resident** between jobs; it erases itself after an inactivity TTL
  (nothing survives a reboot) — no durable permanent install
  ([ADR-0005](adr/0005-agent-lifecycle.md)). It interprets the program **on the machine**
  (one connection, near-local speed). This is the Salt/Puppet minion. **≠ pyinfra**, which
  evaluates locally and ships nothing but shell.
- **Two plans.** An **orchestration** plan (control side: "on these 40 hosts, in this
  order") plus an **execution** plan (the agent, on the target). Do not mix them — that is
  Ansible's Jinja sin.

### Philosophy

**The user is responsible; the tool does not decide for them** (the spirit of C and the
shell, not of a DBMS). Audience: power users. **The accepted counterpart:** irreproachable
observability (freedom without traceability is a flamethrower).

---

## 01 · What the architecture must guarantee

### Dry-run = decisions, not effects

We do not predict what a command does (impossible: apt lock, network…). We predict the
**decisions**: the reading phases run, the action phase is skipped. Report across 200
machines: "nginx would be installed on 12, already present on 188".

**The contract that makes this possible:** the reading phases are **side-effect free**.
Without that, the dry-run is worthless. Which phases, in which mode:
[`language.md`](language.md), [ADR-0035](adr/0035-phases-and-modes.md).

It is a contract, so it gets verified: the engine must never call the action phase in
dry-run nor in `status`. It did for months without anything noticing (#338) — the
invariant was written in a comment, not in a test.

### Three distinct questions, three phases

- **Precondition** — "*can* this work?" (package exists, disk, permissions) → failure =
  **error**, before any action.
- **State observation** ([ADR-0013](adr/0013-observe-state-contract.md)) — the *current*
  state, compared against the requested arguments → converged = **skip**. Instead of a
  hand-written boolean, the instruction returns its state and the engine derives both the
  skip *and* the `status` report from it.
- **Change detection** — "did the action *actually* change anything?" → feeds the report
  and the chaining (`if x.changed { … }`).

Conflating them loses either idempotence or previewability.

### Parallelism

- **Across hosts = a pillar.** The same plan on N machines at once. Free, a large gain,
  zero conflict.
- **Within a host = explicit, the user's call.** A declared `parallel { }` block; the shell
  is opaque, so the tool cannot infer the absence of a conflict.

---

## 02 · Open holes

### 🟠 Non-atomic composite — partial failure

A multi-step action is not transactional: if step 2 fails, step 1 has already acted. **A
single observation at the head does NOT make a composite idempotent**, and rollback is not
solved — it will not be, and
[ADR-0040](adr/0040-rerunnable-steps-and-unsafe-shell.md) says why.

What shellf guarantees instead is weaker and tenable: a failed run leaves the host in a
state a subsequent run can resume from. That holds exactly when every step tolerates being
replayed — and it is refused at writing time rather than hoped for: shell that a def
already knows how to do does not parse, except under `unsafe shell`.

Still open: atomicity itself. A partial state survives, visible and replayable, but it
survives.

### 🔴 The real wall: the ecosystem, not the technology

The language is 5% of the work; a correct cross-distro stdlib of instructions plus the
ecosystem is 95%. Alternatives to Ansible die of their **community cold start**, not of
their technology. The way out: not to *beat* Ansible Galaxy, but to make the moment where
"I give up on the module and write shell" clean and idempotent. The bar drops from "years"
to "day one".

### 🟠 Testing the language against different shapes

A language is not proven by one example — it **overfits**. `file.copy` (where observation
is no longer "present/absent" but "does the content match?") and `service.ensure` (two
orthogonal dimensions: running? + enabled at boot?) exist today and did not bend the
model. The question stays open for what comes next: a shape that does not fit is a signal,
not an implementation detail.
