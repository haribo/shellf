# Claude Guidelines — shellf

AI directives only (guardrails, collaboration, doc references). Product design lives in `docs/`.
Rules must be concise. One rule per line when possible.

## General

- This file takes precedence over auto-memory. If an auto-memory entry contradicts a rule here, follow this file and update or remove the conflicting memory; do not act on the stale memory.
- Never assume a produced artifact matches its request — a generation, a render or a transform routinely ignores or distorts instructions. Before judging, presenting or consuming it, inspect what was actually produced and state the invariant it must satisfy; where the invariant is checkable, write the check that counts the violations. Claiming "it now matches X" without having verified is a defect.

## Language & style

- Collaboration (responses, discussion): **French**.
- All written artifacts (docs, code, commits, issues, PRs): **English**.
- Responses ≤ 15 lines by default. "Avis sévère" = verdict in 1 line + 3 bullets max. Tables only when tabular beats prose. Rationale / background only on explicit request.

## Collaboration

- When the user asks for an opinion, be severe, honest and challenging — the goal is code that meets professional standards, not the user's agreement. Zero flattery, no hedging, no false balance
- Verdict first (1 line), then 3 bullets of substance at most. Say plainly when something is wrong, and say so when it is right — an unearned validation is a defect
- Quality over satisfaction — push back on over-engineering, incoherence, and unjustified additions, including when user-proposed
- Critique constructively: acknowledge what is sound first, cite established standards (RFC, WCAG, NN/G, language idioms) rather than personal preference, propose the correction — never mere opposition, never a strawman of the user's position
- The user decides in the end: challenge until the decision, then execute it in full. If a debate cycles past 3 iterations on the same axis without converging, propose to decide rather than continue

## Project status

- **Implemented, integration phase.** The interpreter (lexer/parser/evaluator), SSH transport, the detached **resident** agent (ADR-0005), and an embedded stdlib all exist and are exercised end-to-end. `shellf` = working name, verified available.
- Thesis: "Raw shell, but idempotent, previewable, fast." The shell is a first-class citizen, not the shameful escape hatch. Architecture and open holes: `docs/design.md`; language spec: `docs/language.md`; resolved decisions in `docs/adr/`.

## Design ↔ code

- Code is never source of truth — a code/design disagreement means the code is the bug, or the design needs an explicit amendment, never both silently
- If the design is silent on a needed behavior, write the design first, then the code
- Anchor a confirmed non-obvious decision — especially one where an alternative was rejected — in the design docs or an ADR before building on it

## Implementing an issue

- **No issue is trusted** — not one written a year ago, not one written an hour ago, not one you wrote yourself. Age is not the criterion: #387 describes code written the same day and its central claim still had to be read in the file
- Verify every claim **in the files** before acting, and cite `file:line` for each. "I read the code" is not verification; a citation the reader can re-open is
- Say explicitly what you could **not** confirm, and record a claim that turns out false **in the issue itself** — a wrong claim dropped in silence is raised again six months later
- Also check the issue is still current: close it with evidence if already delivered, post an audit comment if the architecture drifted under it
- **Then, before touching anything**, explain the problem **simply and concisely** — an example when an example is what makes it clear. The user validates *that explanation*, not the issue. Build after
- Exempt: trivial changes (typo, formatting, dep bump), the same boundary that exempts them from needing an issue. Friction that buys nothing is how a rule gets routed around

## Discipline (user-requested)

- Imports are **shipped** and exercised end to end — local (ADR-0015) and remote modules (ADR-0016); maintain them like the rest. What stays out of scope is the layer above: a registry, a namespace, any publication index (phase 3).
- The language is now real (grammar, lexer, parser, evaluator) — the "don't invent a language" caution is **resolved**; grammar changes go through an ADR.
- The first milestone (engine + instructions over SSH via the agent, apply AND check) is **done**; the stdlib is now self-hosted (`def` in shellf, embedded). `Executor` (shell) stays an injectable interface (testability).

## Testing

- **NEVER run `test/e2e/run.sh` directly on a development machine — use `test/e2e/vm.sh run`.** The harness starts a `--privileged` container with systemd as PID 1; it shares the host kernel and has ended a developer's graphical session four times, rewriting `kernel.core_pattern` and `vm.swappiness` on the way (#528, #529). `vm.sh` runs the identical harness inside a throwaway VM, so the same container shares the VM's kernel instead. CI calls `run.sh` directly on purpose: a runner is already disposable.
- **Every stdlib def is exercised against a real target by `test/e2e/plans/coverage.shellf`, no exception.** `test/e2e/def-coverage.sh` fails the build when one is not, so a new def arrives with its coverage or turns CI red. An exemption is allowed, named in that script with its reason — never silent.
- A def that declares `observe` must report a converged outcome on a second run; the harness derives that set from the stdlib and checks it. A def with no `observe` is action-shaped (ADR-0029) and is excluded by construction, not by a list.
- Unit tests with a fake executor cannot prove idempotence: the fake answers whatever the test asks. `dir.owner` reported `changed` on every run for months and every unit test passed — it took running it twice against a container to see it.
- Bug fixes start with a failing test that reproduces the bug: write the test first and watch it fail, then fix, then re-run it green (red → green)
- That test stays as the regression test for this bug — reference the issue number in it, so a later reader knows what it guards and does not delete it as noise
- Bug fixes must reproduce the failure from observed evidence (logs, network capture, repro steps); never invent the failure scenario from a hypothesis
- Never modify existing tests without explicit approval
- If a test fails after code changes, report it instead of fixing it silently
- Adding new tests is always allowed

## Docs — read in order

1. `docs/design.md` — WHY shellf is built this way: thesis, architectural bets, open holes. Not the language.
2. `docs/language.md` — HOW to write shellf: the language spec. Current by definition.
- History of decisions lives in `docs/adr/`, not in the docs above — each ADR carries its own rejected alternatives.
- Documentation strategy: `docs/adr/0001-documentation-strategy.md`.
- ADR lifecycle: never delete an ADR; a reversal is a **new** ADR, and both sides carry the link — `Superseded by ADR-NNN` on the old, `Supersedes ADR-MMM` on the new. A one-sided link is how the chain rots
- An ADR records a **decision**, never the state of the code. A note saying something is "not yet implemented" is true for a day and misleading afterwards — it was added to ADR-0036 and went stale within one. Track the gap in the issue; the ADR says what was decided and why.
- An ADR whose decision no longer applies, with no replacement, is marked `Deprecated` — never edited away or moved
- In-place edits only for corrections of form and for clarifications that do not change the decision
- ADR lifecycle details and the full status vocabulary: see ADR-0001 §4
- All docs under `docs/`; the root keeps only what tooling and readers expect there (`README.md`, `CHANGELOG.md`, `CLAUDE.md`). Decisions with rejected alternatives → `docs/adr/`.

## Git

- Conventions: commits `docs/git-commits.md`, workflow `docs/git-workflow.md`, issues `docs/git-issues.md`.
- Changelog: `CHANGELOG.md` (Keep a Changelog) is canonical; each PR adds a line under `## [Unreleased]`; a release mirrors that version's section into the GitHub Release notes — see `docs/git-workflow.md`.
- Branches `main` (released) and `develop` (integration) are permanent — never push directly, always via PR. Feature work: issue → branch `type/N-desc` from `develop` → PR into `develop` (squash).
- **Merging `develop`→`main`, or tagging/publishing a release, ALWAYS requires explicit human approval — every time, no exception.** A standing "be autonomous" instruction covers `feature`→`develop` only; it never authorizes touching `main`, cutting a release, or pushing a tag.
- Before editing, check the branch — if on `main` or `develop`, propose a branch and wait.
- Every change starts with a GitHub issue except trivial ones (typo, formatting, dep bump).
- For commits, PRs, merges, issues, releases: invoke `/git-commit`, `/gh-pr-create`, `/gh-merge-develop`, `/gh-issue`, `/release` via the Skill tool — do not run `git commit` / `gh pr create` / `gh pr merge` / `gh issue create` directly. `/release` stops at two human-approval gates (main merge, tag) — never bypass them.
- Conventional commits, single line, **no AI references**. Never commit/push without explicit approval.
