# ADR 0047 — `main` is the default branch, and only `develop` may target it

## Status

Active. Records what [docs/git-workflow.md](../git-workflow.md) already prescribes;
supersedes nothing.

## Context

The branch model has been settled since the first release: `main` is released, `develop`
integrates, and a feature branches from `develop` and returns there. `release.yaml`
publishes from the tagged commit on `main`, and CLAUDE.md gates merging into it behind
explicit human approval, every time.

The repository's **default branch** was `develop`, which contradicted all of that in the
one place a visitor sees first. Landing there means reading a `README.md` documenting
instructions that are not released, and a `CHANGELOG.md` whose top section describes a
version nobody can download. The v0.5.0 release reports `target: develop` through the API
while its tag is on `main` — cosmetic, but it is the same confusion surfacing.

## Decision

### 1. The default branch is `main`

A visitor lands on what ships. The README they read, the changelog they scan and the binary
they can download describe the same thing.

### 2. A PR into `main` may only come from `develop`, and CI enforces it

`gh pr create` without `--base` targets the default branch. While that default was
`develop`, forgetting the flag opened a feature PR against the integration branch —
harmless. With `main` as the default, the same omission aims at `main`, which
`docs/git-workflow.md` forbids: *"Never target `main` with a feature PR."*

So the guard is not a follow-up, it is part of this decision: `pr-validation.yaml` fails a
PR whose base is `main` and whose head is not `develop`, naming the rule. Branch protection
cannot express this — GitHub has no "restrict the source of a PR" — and the workflow
already holds the other PR-shape rules (conventional title, conventional commits, issue
reference), so it is where a reader looks for them.

The switch without the guard trades a visible default for a silent risk. That trade is what
made this worth a record rather than a settings change.

### 3. The CI badge reports `main`

It pinned `branch=develop`, so a green badge described integration while the reader was
looking at the released page. A badge that answers a question other than the one it appears
to answer is worse than no badge.

## Rejected alternatives

- **Leave `develop` as the default.** It is what git-flow tooling often does, and it is
  wrong for a repository whose front page is read by people deciding whether to try the
  tool: they would be reading unreleased documentation.
- **Switch the default and rely on discipline for PR targets.** The discipline exists in
  prose, and prose is what #424 just showed erodes. A rule that can be checked should be.
- **Require PR reviews on `main`** as the guard instead. It stops nothing here — a
  self-approved feature PR into `main` would still be wrong — and it is a separate
  question for a solo project.

## Consequences

- A clone lands on `main`; contributors branch from `develop` explicitly, as
  `docs/git-workflow.md` already instructs.
- `gh pr create` without `--base` now targets `main` and fails the guard, naming the fix.
  The project's skills pass `--base develop` explicitly and are unaffected.
- The three workflows already trigger on `[develop, main]` by name, so nothing else moves.
