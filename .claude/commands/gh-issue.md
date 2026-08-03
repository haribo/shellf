# Issue

Create a GitHub issue following project conventions.

Accepts an optional argument: issue title. If not provided, ask the user.

## Instructions

1. Read `docs/git-issues.md` and apply rules strictly.
2. Collect details:
   - If a title is provided, use it; otherwise ask for a description and craft the title.
   - Ask which `type:` label applies (`bug`, `feature`, `chore`, `docs`) if not obvious.
   - Optionally ask for a `priority:` label.
3. Validate the title: imperative present, lowercase, no period, ≤72 chars, no type prefix.
   Fix and show the corrected version if it violates a rule.
4. Craft the body (self-contained per `docs/git-issues.md`). Present title + labels + body for approval.
5. Create it: `gh issue create --title "<title>" --label "type: <t>" --body "<body>"`
   (add `--label "priority: <p>"` if specified). Return the issue URL.
