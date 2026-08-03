# Commit conventions

## Format

```
<type>(<scope>): <description>
<type>(<scope>)!: <description>   ← breaking change
```

## Types

`feat` | `fix` | `docs` | `style` | `refactor` | `perf` | `test` | `chore` | `ci` | `build`

## Scope

Recommended on all commits. Matches the area of change.

Examples: `engine`, `agent`, `executor`, `shell`, `ssh`, `docs`, `justfile`, `ci`

May be omitted for generic `style` or `chore` spanning the whole project.

## Breaking changes

Append `!` after the scope:

```
feat(shell)!: rename run to shell
```

## Rules

1. Single line only — no body, no footer
2. Max 72 characters
3. Imperative present tense ("add" not "added")
4. No capital letter, no period
5. No AI references or promotional content
