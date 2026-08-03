# Pull Request

Run local checks, push, and create a PR targeting `develop`.

Accepts an optional argument: PR title. If not provided, generate one from commits.

## Instructions

### 1. Validate branch

- `git branch --show-current`. If on `main` or `develop`: **refuse** — must be a feature branch.

### 2. Resolve issue reference

- Extract the issue number from the branch name (`type/N-description` → `#N`).
- If none found and the title does not start with `chore`/`style`: **refuse** — ask for the issue number.
- One PR may `Closes #a` + `Closes #b` only for sibling sub-issues of the same epic designed together.

### 3. Local checks — fail on first violation, do NOT push if any fails

- `go vet ./...`
- `go test ./...`
- `go build ./...`

Pre-existing failure on `develop` unrelated to this PR still fails the gate: open a
separate issue, stop — do not bypass.

### 4. Test-up-to-date — HARD STOP, answer with evidence

Two acceptable answers:
(a) "Existing tests cover it" — quote the test file(s) + the assertion.
(b) "Tests added here" — list `git diff develop...HEAD --name-only | grep _test.go`.

Unacceptable (= failure): "manually verified", "vet passes", "no test needed", silence.
Exempt: doc-only, config-only, or a refactor with provably identical output (state the proof).

### 5. Prepare and create

- `git log --oneline develop..HEAD` and `git diff develop...HEAD --stat`.
- Title: `type(scope): description` (<70 chars, no `(#N)` suffix).
- Push: `git push -u origin <branch>`.
- Create:

```
gh pr create --base develop --title "<title>" --body "$(cat <<'EOF'
## Summary
<1-3 bullets>

## Test plan
<checklist>

Closes #<N>
EOF
)"
```

Omit `Closes #<N>` for `chore`/`style` PRs. Return the PR URL.
