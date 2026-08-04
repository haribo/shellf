# Claude Guidelines — shellf

AI directives only (guardrails, collaboration, doc references). Product design lives in `DESIGN.md` / `docs/`.
Rules must be concise. One rule per line when possible.

## Language & style

- Collaboration (responses, discussion): **French**.
- All written artifacts (docs, code, commits, issues, PRs): **English**.
- Responses ≤ 15 lines by default. "Avis sévère" = verdict in 1 line + 3 bullets max. Tables only when tabular beats prose. Rationale / background only on explicit request.

## Collaboration

- Severe opinion. Challenge everything, contradict, zero flattery — the user asks for it every turn.
- Quality over satisfaction — push back on over-engineering, incoherence, unjustified additions, including user-proposed. Acknowledge strong points first, cite standards over preference, propose corrections — never strawman.
- If a debate cycles past 3 iterations on the same axis without convergence, propose to decide rather than continue.

## Project status

- **Implemented, integration phase.** The interpreter (lexer/parser/evaluator), SSH transport, the detached **resident** agent (ADR-0005), and an embedded stdlib all exist and are exercised end-to-end. `shellf` = working name, verified available.
- Thesis: "Raw shell, but idempotent, previewable, fast." The shell is a first-class citizen, not the shameful escape hatch. Full spec: `DESIGN.md`; resolved decisions in `docs/adr/`.

## Design ↔ code

- Code is never source of truth. Code/design disagreement = code is the bug, OR design needs an explicit amendment — never both silently.
- Design-first: if the design is silent on a needed behavior, write/amend the design (`DESIGN.md`, `docs/`), then code.
- No code without a design it implements. Anchor a confirmed non-obvious decision (especially after a rejected alternative) in `docs/design/` or `docs/adr/` before building on it.

## Discipline (user-requested)

- Do NOT design imports / ecosystem / sharing yet (phase 3).
- The language is now real (grammar, lexer, parser, evaluator) — the "don't invent a language" caution is **resolved**; grammar changes go through an ADR.
- The first milestone (engine + instructions over SSH via the agent, apply AND check) is **done**; the stdlib is now self-hosted (`def` in shellf, embedded). `Executor` (shell) stays an injectable interface (testability).

## Docs — read in order

1. `DESIGN.md` — full spec: resolved decisions, primitives, open holes, apt-install example.
2. `docs/CONVERSATION.md` — design-discussion history (the WHY).
3. `docs/language.md` — language spec (stable primitives only).
- Documentation strategy: `docs/adr/0001-documentation-strategy.md`.
- ADR lifecycle: never delete an ADR; a reversal is a **new** ADR that supersedes the old (marked `Superseded`). In-place edits only for same-decision clarifications. See ADR-0001 §4.
- All docs under `docs/` (except root `DESIGN.md`/`README.md`). Decisions with rejected alternatives → `docs/adr/`.

## Git

- Conventions: commits `docs/git-commits.md`, workflow `docs/git-workflow.md`, issues `docs/git-issues.md`.
- Changelog: `CHANGELOG.md` (Keep a Changelog) is canonical; each PR adds a line under `## [Unreleased]`; a release mirrors that version's section into the GitHub Release notes — see `docs/git-workflow.md`.
- Branches `main` (released) and `develop` (integration) are permanent — never push directly, always via PR. Feature work: issue → branch `type/N-desc` from `develop` → PR into `develop` (squash).
- **Merging `develop`→`main`, or tagging/publishing a release, ALWAYS requires explicit human approval — every time, no exception.** A standing "be autonomous" instruction covers `feature`→`develop` only; it never authorizes touching `main`, cutting a release, or pushing a tag.
- Before editing, check the branch — if on `main` or `develop`, propose a branch and wait.
- Every change starts with a GitHub issue except trivial ones (typo, formatting, dep bump).
- For commits, PRs, merges, issues, releases: invoke `/git-commit`, `/gh-pr-create`, `/gh-merge-develop`, `/gh-issue`, `/release` via the Skill tool — do not run `git commit` / `gh pr create` / `gh pr merge` / `gh issue create` directly. `/release` stops at two human-approval gates (main merge, tag) — never bypass them.
- Conventional commits, single line, **no AI references**. Never commit/push without explicit approval.
