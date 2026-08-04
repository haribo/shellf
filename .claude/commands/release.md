# Release

Cut a release: roll the changelog, merge `develop`→`main`, tag, and let CI publish
notes from `CHANGELOG.md`. See `docs/git-workflow.md` (Release workflow + Changelog).

Accepts an optional argument: the version `X.Y.Z` (no `v`). If absent, ask.

**Two hard approval gates. Never merge to `main` or push the tag without an
explicit human "go" each time — this overrides any standing "be autonomous".**

## Instructions

### 1. Preconditions — collect ALL violations, then stop if any

- `git checkout develop && git pull origin develop`.
- `develop` tip checks are green: `gh run list --branch develop --limit 1` → success.
- `## [Unreleased]` in `CHANGELOG.md` has at least one entry → else **refuse**
  ("nothing to release").
- Version: the argument, or ask. Validate SemVer `X.Y.Z`. `git tag -l vX.Y.Z` and
  `git ls-remote --tags origin vX.Y.Z` must be **empty** → else refuse (tags are
  immutable; never re-tag a released version).

### 2. Roll the changelog (PR into `develop`)

- Branch `chore/release-vX.Y.Z` from `develop`.
- In `CHANGELOG.md`:
  - rename `## [Unreleased]` → `## [X.Y.Z] - <today, ISO YYYY-MM-DD>`;
  - insert a fresh empty `## [Unreleased]` above it;
  - update link refs: `[Unreleased]: …/compare/vX.Y.Z...HEAD`, and add
    `[X.Y.Z]: …/compare/v<prev>...vX.Y.Z` (or `…/releases/tag/vX.Y.Z` if first).
  - Do **not** edit the entries' wording here — they were written per-PR.
- Commit `chore(release): vX.Y.Z`, push, open a PR into `develop`, wait for green,
  squash-merge (`/gh-merge-develop`). Then `git checkout develop && git pull`.

### 3. Merge `develop`→`main` — **APPROVAL GATE 1**

- `gh pr create --base main --head develop --title "chore(release): vX.Y.Z"` with a
  short body. Wait until every check is green (`gh pr checks`).
- **Stop. Show the PR and ask the user to approve the merge to `main`.**
- On explicit approval only: `gh pr merge <n> --merge` (merge commit, never squash).

### 4. Tag and push — **APPROVAL GATE 2**

- `git fetch origin main` → the merge commit is the new `main` tip.
- **Stop. Ask the user to approve tagging `vX.Y.Z` on that commit (irreversible,
  public).**
- On explicit approval only:
  `git tag -a vX.Y.Z <main-tip> -m "vX.Y.Z" && git push origin vX.Y.Z`.
- The tag push triggers `release.yaml`, which extracts the `[X.Y.Z]` section from the
  tagged `CHANGELOG.md` and publishes it as the notes.

### 5. Verify

- `gh run list --workflow release.yaml --limit 1` → success.
- `gh release view vX.Y.Z`: the tag is on `main`, the notes equal the `[X.Y.Z]`
  section (no commit dump), and `shellf-linux-amd64` is attached.
- Optionally download it and confirm `./shellf-linux-amd64 --version` prints
  `shellf vX.Y.Z`.

## Refuse / stop conditions

- `Unreleased` empty, non-SemVer version, or an already-existing tag → refuse in
  step 1.
- Either approval gate not explicitly granted → stop; do not proceed.
- CI not green at any gate → stop; never merge or tag on red.
