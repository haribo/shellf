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
# 1. roll CHANGELOG.md: `## [Unreleased]` → `## [X.Y.Z] - YYYY-MM-DD` (PR to develop)
# 2. cut the release (each step needs explicit human approval — see Rules):
gh pr create --base main --head develop      # when develop is validated
gh pr checks
gh pr merge --merge                          # merge commit, never squash
git tag vX.Y.Z && git push origin vX.Y.Z
```

The tag push triggers `release.yaml`, which **extracts the `[X.Y.Z]` section from
the tagged `CHANGELOG.md`** and publishes it as the release notes — the notes are
part of the tagged commit, never edited in after the fact.

Prefer the `/release` skill, which runs this flow and stops at each approval gate.

## Changelog

`CHANGELOG.md` (repo root, [Keep a Changelog](https://keepachangelog.com) format)
is the **single source of truth**. A GitHub Release body is that version's section,
**mirrored** — never a second, divergent narrative.

- Latest version first; dates ISO `YYYY-MM-DD`; Semantic Versioning declared.
- Categories, in order: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`,
  `Security`. Link versions to their release/compare pages.
- **Write for humans, not machines**: user-facing lines, not commit subjects, and
  never a raw `--generate-notes` dump. Exclude noise (dotfiles, dev deps, style,
  doc formatting, merges).
- A release moves `[Unreleased]` into `## [X.Y.Z] - <date>` and uses that section
  as the release notes; keep an empty `[Unreleased]` on top.

### The entry rule (checked)

Every PR with a user-observable change adds **one entry** under `## [Unreleased]`:

- **One or two sentences**: what changed for the reader, and the issue number. Not the
  mechanism, not the rejected alternatives, not the failure mode — the issue holds those,
  and the PR holds the reasoning. Keep a Changelog puts it as *the headline and the hook,
  not the full story*.
- **`#N` is required.** It is the thread a reader pulls when the entry is not enough.
- **`**BREAKING** —`** opens an entry that changes how an existing plan is written.
- **One heading per category**, in the order above.

`test/changelog-rule.sh` enforces it on `[Unreleased]` and runs in CI. The ceiling is 60
words — two full sentences with a reference land near 40. This was prose for the project's
whole life and eroded anyway: 48 entries, median 91 words, longest 471, against 18–56 for
every released section (#424). It matters because `release.yaml` publishes the section of
the **tagged commit** as the GitHub release notes: whatever is in `[Unreleased]` at tag
time becomes public, and there is no editing pass afterwards.

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
