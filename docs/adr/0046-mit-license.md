# ADR 0046 — shellf is MIT-licensed

## Status

Active. First record on licensing; supersedes nothing.

## Context

The repository carried no license: no `LICENSE`, and no mention in `README.md`, `go.mod`
or any of the 45 preceding ADRs — `grep -rniE "licen[cs]e"` over the Markdown and config
returned nothing, and `gh repo view --json licenseInfo` answered `null`.

Absent an explicit grant, the default is **all rights reserved**. Nobody may legally fork,
package or redistribute shellf. For a project that ships a single static binary and whose
[ADR-0016](0016-remote-modules.md) invites importing instruction libraries from other
people's repositories, that is a hole in the premise, not a formality.

The dependency side sets no constraint: the only direct dependency,
`golang.org/x/crypto`, is BSD-3-Clause, as is the indirect `golang.org/x/sys` — both
compatible with anything considered here.

## Decision

**MIT**, `Copyright (c) 2026 Nicolas CHAUVIN`.

- **Maximum compatibility**, GPLv2 included, and recognised on sight by packagers (AUR,
  Homebrew, Nixpkgs) — which is what matters for a tool distributed as a binary.
- **One license text across the author's projects**: vigie is already MIT. Two licenses to
  reason about would be a cost with no matching benefit.
- **Nothing in shellf calls for more.** The obligations MIT lacks — a patent grant, file
  marking, a `NOTICE` — answer risks a solo project with no patentable technique does not
  carry.

## Rejected alternatives

- **Apache-2.0.** The one substantive difference is the explicit patent grant (§3). Against
  it: incompatibility with GPLv2, a `NOTICE` file to maintain, and the modified-file
  marking of §4b. The risk it covers does not apply here.
- **MPL-2.0.** Per-file copyleft: friction on every fork, for a reciprocity this project
  does not seek.
- **BUSL-1.1.** Source-available, not open source. Warranted only if monetisation were a
  goal; it is not, and it would contradict inviting third-party modules.

Recorded so it is not re-litigated: protecting the name **shellf** is trademark law, and is
independent of the license. It is not a criterion between MIT and Apache-2.0.

## Consequences

- `LICENSE` at the repository root, the unmodified MIT text; GitHub reports the license and
  packagers can read it without asking.
- Contributions are received under the same terms, which is what MIT's inbound=outbound
  reading means in the absence of a CLA. A DCO, a `CONTRIBUTING.md`, per-file headers and
  the licensing of user defs and remote modules each remain open questions, and each gets
  its own decision if it is ever wanted.
