# ADR 0001 — Documentation strategy

## Status

Active

## Context

shellf is in meta-language design. Documentation risks accumulating ad-hoc rules across sessions, with no shared model of audience, source of truth, or format. This ADR fixes the framework so decisions are not re-litigated every session. Adapted from the tribnest project, stripped of its backend/frontend split (shellf is a single Go binary).

## Decisions

### 1. Audience

PRIMARY: solo dev + Claude assistants. SECONDARY: future contributors. OUT OF SCOPE: non-tech. Style optimized for PRIMARY: dense, factual, table-heavy, no narrative pedagogy.

### 2. Source of truth

| Source | Scope |
|---|---|
| `DESIGN.md`, `docs/design/`, `docs/language.md` | WHAT/WHY: language semantics, primitives, user-observable behavior, rationale |
| Code (Go, once it exists) | HOW: implementation |
| `docs/adr/` | Decisions with rationale + rejected alternatives |

Forbidden: docs paraphrasing code (type defs, signatures, file trees derivable by reading the code).

### 3. Document types

Three only.

| Type | Location | Scope |
|---|---|---|
| Spec / design | `DESIGN.md`, `docs/language.md`, `docs/design/` | Language + product: WHAT, WHY when non-obvious |
| ADR | `docs/adr/` | Decisions, alternatives rejected, consequences |
| Doc technique | `docs/*.md` (excl. `adr/`) | Conventions neither spec nor ADR |

### 4. Lifecycle

- **ADR**: editable in place; git log = history; `Status` = `Active` or `Superseded by ADR-XYZ` (major reversals only).
- **Spec**: tracks the design discussion; keep current, no revision logs inside.
- **Drift detection**: reactive (a divergence is noticed / a focused audit), not calendar-based.

**Severe 4-point test** (new doc, new H2/H3, changed rule): (1) singular concern, no "and"; (2) not already said by code; (3) no duplicate (grep confirms); (4) a senior dev would write it spontaneously. Fail any → remove, merge, or rewrite. Skipped for typos/reformulations.

### 5. Format and style

| Element | Rule |
|---|---|
| Tables | Prefer over prose whenever content is a set of (key, value, condition) |
| Code blocks | **Allowed** for shellf language examples and shell/CLI output — the language IS the product, its syntax is the specified artifact, not code paraphrase. **Forbidden** when paraphrasing Go implementation. |
| Diagrams | Prefer state-transition tables. Mermaid only if non-tabular and renders natively on GitHub. No ASCII art, no PNG. |
| Headings | H1 = title, H2 = sections, H3 = sub, H4 max. |
| Cross-references | Markdown `[text](path)`. No line numbers. |
| File naming | lowercase, kebab-case. |
| Emojis | Forbidden in docs. |

## Rationale

"Everyone" = "no one": pedagogy for non-tech conflicts with stenographic style for solo dev + AI. The intentional gap between spec and code prevents drift and cognitive overhead. The code-block exception (vs tribnest's ban) exists because shellf's product IS a language — its syntax examples are the specified artifact, not a paraphrase of the implementation.

## Consequences

- Every file in `docs/` falls into exactly one of the three types.
- A new doc technique file must pass the 4-point test before creation or retention.
- `docs/adr/` created with this entry as its first.
- No backend/frontend ADR split — single binary.
