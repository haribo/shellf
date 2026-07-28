# Commit

Commit ALL pending changes following project conventions, grouped into finalized logical steps.

## Instructions

1. Read `docs/git-commits.md` and apply rules strictly
2. Run `git branch --show-current` — if on the default branch (`main`/`master`), propose a branch name (e.g., `feat/short-description`) and **wait** for the user to switch
3. Run `git status` and `git diff` to understand ALL pending changes
4. Analyze all changed files as a whole:
   - Group files by logical step (tightly coupled changes = one commit)
   - Each commit = a **finalized logical step**, not work-in-progress
   - Coupled changes (signature change + all call sites) belong in the same commit
5. Present the proposed commit plan (files per commit + messages) for approval before executing

## Rules

- Never push to remote
- Nothing remains uncommitted after execution
- Each message follows `docs/git-commits.md` strictly
- Never commit without explicit user approval
