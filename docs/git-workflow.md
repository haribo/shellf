# Git workflow

See also: [git-commits.md](git-commits.md) for commit conventions,
[git-issues.md](git-issues.md) for issue conventions.

## Branches

| Branch | Role |
|---|---|
| `main` | Released. Tagged versions, merged from `develop`. |
| `develop` | Integration. All feature work lands here first. |

Both are permanent — **never push directly, always via PR**.

## Issue-first workflow

Every change starts with a GitHub issue, except trivial changes (typo,
formatting, dependency bump) and the initial bootstrap.

- The issue describes the **what/why**; the PR describes the **how**.
- The branch name includes the issue number for traceability.
- The PR body references the issue with `Closes #N` to auto-close on merge.

```
issue #12 → branch feat/12-inventory-parser → PR "Closes #12" → squash merge
```

## Feature workflow

```
/gh-issue                                    # 1. create issue
git checkout -b feat/12-desc develop         # 2. branch from develop
/git-commit                                  # 3. work, commit
git fetch origin && git rebase origin/develop # 4. rebase before PR
/gh-pr-create                                # 5. PR — MUST target develop
gh pr checks                                 # 6. wait for CI
/gh-merge-develop                            # 7. squash merge
```

## Release workflow

```
gh pr create --base main --head develop      # when develop is validated
gh pr checks
gh pr merge --merge                          # merge commit, never squash
git tag vX.Y.Z && git push origin vX.Y.Z
```

## Merge strategy

| Target | Strategy | Command |
|---|---|---|
| Feature → `develop` | **Squash** | `/gh-merge-develop` |
| `develop` → `main` | **Merge commit** | `gh pr merge --merge` |

Never merge a feature PR with `--merge`. Never target `main` with a feature PR.

## CI gating

GitHub Actions gate every PR: `go vet ./...`, `go test ./...`, `go build ./...`,
plus PR-validation jobs (commit messages, PR title, issue reference). All must be
green before merge. A doc-only PR still passes the Go jobs (they are fast on no-op
diffs); there is no manual skip.

## Rules

- Never push directly to `main` or `develop` — always via PR.
- **Merging `develop`→`main` and pushing a release tag require explicit human approval, every time — never autonomous.** A standing "be autonomous" instruction applies to `feature`→`develop` only; it never extends to `main` or to cutting a release.
- One logical change per PR — split unrelated work.
- Keep feature branches short-lived (days, not weeks).
- Rebase on `develop` before opening a PR.
- A PR changing user-observable behavior updates `docs/design/` in the same diff.

## Branch naming

```
feat/12-short-description
fix/34-short-description
refactor/56-short-description
docs/78-short-description
chore/short-description
```

Prefix matches commit type; include the issue number after the slash; kebab-case.
May omit the number for trivial `chore`/`style` without an issue.
