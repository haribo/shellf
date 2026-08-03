# Develop Merge

Squash-merge a feature PR into `develop` with strict validation.

Accepts an optional argument: PR number or branch name. If absent, detect from the current branch.

## Instructions

### 1. Resolve target PR

- PR number → use directly. Branch name → find its open PR targeting `develop`.
- No argument → current branch's open PR targeting `develop`. No PR found → **refuse**.

### 2. Collect (in parallel)

- `gh pr view <n> --json number,title,state,baseRefName,mergeable,reviews,statusCheckRollup,commits,body`
- `gh pr checks <n>`
- `gh pr diff <n>`

### 3. Validate — collect ALL violations, then report together

| # | Check | Rule |
|---|---|---|
| 1 | State | `OPEN` |
| 2 | Base | `develop` — refuse any other |
| 3 | Mergeable | `MERGEABLE` — if `CONFLICTING`, demand rebase |
| 4 | CI | every required check `pass`/`success` — no pending, no failure |
| 5 | Reviews | zero `CHANGES_REQUESTED` |
| 6 | Diff | read it — flag debug prints, TODO/FIXME, secrets, commented-out or unrelated code |

Any failure → report all violations and **stop**.

### 4. Squash commit message

- Follow `docs/git-commits.md`. Derive `type(scope):` from the actual changes, not blindly from the PR title.
- Single line, ≤72 chars (excl. suffix), imperative, no capital, no period. Breaking → `type(scope)!:`.
- Append ` (#<PR>)`. Present it to the user for approval before merging.

### 5. Merge

- `gh pr merge <n> --squash --delete-branch --subject "<approved>" --body ""`. On failure, report — do not retry.

### 6. Cleanup

- `git checkout develop && git pull origin develop`.
- Delete the local feature branch if present. Confirm with `git log --oneline -1`.
