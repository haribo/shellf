# ADR 0020 — Design for the user base, justified by domain precedent

## Status

Active. A design *principle*, not a feature — it governs how feature decisions
are argued in the ADRs that follow.

## Context

shellf is developed against real deployments (the dogfood, e.g. a Traefik/Docker
host). That is invaluable — it surfaces concrete needs — but it recurs a
question: **do we build a capability the current dogfood does not need?**

Two failure modes bracket the answer:

- **Build only what the current user needs.** A tool that fits exactly one
  deployment is not a tool. Per-host configuration, ad-hoc value overrides — a
  deployment tool is expected to have these; omitting them because *this* dogfood
  is single-host makes shellf useless to the next user. (An over-strict "YAGNI"
  applied to a *product* is this mistake.)
- **Build anything a user might want.** The opposite swamp — inventing usages,
  chasing every hypothetical. This is how the anti-Ansible tools died: scope and
  ecosystem creep with no discipline.

We need a rule that greenlights the first kind and refuses the second.

## Decision

### 1. shellf is a product for a user base, not the current dogfood

Features serve the general **deployment domain**, not only the deployment in
front of us. "The current dogfood does not need X" is **not** a reason to omit X.

### 2. A capability is justified by a recognized, attested domain need

X is justified when it is a **recognized, established need of the deployment
domain — with precedent in comparable tools** (Ansible, Salt, Chef, Terraform,
…) — *even if the current dogfood does not need it*.

X is **not** justified by a hypothetical usage that **no established tool has**
("a user might want…"). That is inventing usage, and it is refused.

The test is precedent, not imagination: *does the domain already treat this as a
capability?* — not *could someone conceivably use it?*

### 3. Corollary — per-host config and per-call override are first-class

By this rule: **per-host configuration** and **ad-hoc per-call variable override**
(`with { … }`) are first-class capabilities — both are universal in
config-management. They are built regardless of whether a given dogfood needs
them.

### 4. This does not override the other disciplines

Design-first still holds: a non-obvious decision is anchored in an ADR before
code (ADR-0001), and the "raw shell is a first-class citizen" thesis stands.
This ADR governs *what is worth building*, not *how it is decided or shaped*.

## Rejected alternatives

- **Strict YAGNI (build only for the current dogfood).** Correct for a bespoke
  script; wrong for a product — it starves the tool of the domain's table-stakes
  capabilities and it fits no one but the current user.
- **"Users might want it" as sufficient justification.** Unbounded; it is how the
  predecessors drowned in scope. Precedent in the domain is the required filter.

## Consequences

- Feature ADRs argue from **domain precedent**, not from the current dogfood's
  needs alone, and not from imagined usage.
- Immediately: per-host template rendering and the `with { … }` per-call override
  are greenlit (both attested across config-management tools), and are specified
  in their own ADRs.
- The bar remains real: a proposed capability with no precedent in the domain is
  refused until a concrete, recognized need appears.
