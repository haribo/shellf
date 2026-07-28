# Issue conventions

See also: [git-commits.md](git-commits.md), [git-workflow.md](git-workflow.md).

## Title

```
<imperative description>
```

A pure description — the type is carried by labels, not the title.

## Rules

1. Imperative present tense ("add" not "added")
2. Lowercase, no period
3. Max 72 characters
4. Descriptive and concise — stands alone without extra context

## Labels

Prefixed labels categorize issues; the title must not duplicate label info.

### Type (required — pick one)

| Label | Usage |
|---|---|
| `type: bug` | defect or malfunction |
| `type: feature` | new feature or improvement |
| `type: chore` | CI, tooling, maintenance, cleanup |
| `type: docs` | documentation change |

### Priority (optional)

| Label | Usage |
|---|---|
| `priority: critical` | requires immediate attention |

## Self-contained content

If an issue may be implemented without the originating discussion (another Claude
session, future self), the body must let it execute without questions:

- **Why** the change is needed (1-3 lines).
- **Decisions already taken** (no "TBD" unless truly open).
- **Explicit mappings** for refactors (current → target per artifact).
- **Validation criteria** (grep patterns, file lists, test names).
- **PR strategy** when multi-task (one PR vs split, with rationale).
- **Out of scope**, to prevent scope creep.

Trivial issues (typos, one-line config) are exempt. Test: if the implementer
would need to ask "what did you mean by X?", the issue is incomplete.

## Epics

When a feature splits into ~3+ related pieces of the same subsystem, write an
**epic** plus short sub-issues rather than independent issues:

- The **epic** carries the shared context once (why, locked decisions, out of
  scope) and lists sub-issues as a checklist.
- **Sub-issues** stay short: `Part of #<epic>`, a Build section, a Validation
  section. No repetition of the epic's context.
- Don't over-epic: an isolated change stays a plain issue. The threshold is
  coherence of design, not size.
