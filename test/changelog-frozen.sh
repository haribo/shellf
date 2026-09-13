#!/usr/bin/env bash
# A released `[X.Y.Z]` section is history and must not change (#669).
#
# `test/changelog-rule.sh` checks the entries inside `[Unreleased]`. That leaves a hole this
# repository fell into twice, four days apart: an entry added to a **released** section is not
# invalid, it is invisible.
#
#   #630 — the `unless` entry landed in the published `[0.12.0]` section.
#   #668 — #667's entry for #658 landed in the published `[0.14.0]` section.
#
# Neither was carelessness. A release rolls `[Unreleased]` into `[X.Y.Z]` and leaves behind an
# `[Unreleased]` carrying only the categories the next PRs add — so right after a release, "the
# first `### Fixed` in the file" belongs to the version just published. Anything appending
# relative to a heading lands in the wrong section, and the result reads perfectly.
#
# The check is exact rather than heuristic: `release.yaml` publishes the `[X.Y.Z]` section of the
# **tagged commit** as the GitHub release notes, so the tag holds what was published. Compare.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
file="$root/CHANGELOG.md"

fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

[ -f "$file" ] || fail "no CHANGELOG.md at $file"

# The same extraction `release.yaml` uses to build the notes, so this compares what is actually
# published rather than something adjacent to it.
section() { # <version> <file-contents-on-stdin>
  awk -v ver="$1" '
    /^## \[/    { if (found) exit; if (index($0, "[" ver "]")) { found=1; next } }
    /^\[.+\]: / { if (found) exit }   # stop at the link-reference footer
    found       { print }
  '
}

# Versions whose published section was deliberately edited afterwards. Named here with the
# reason, on the record, the way `test/e2e/def-coverage.sh` names its one exemption: a silent
# allowance is a hole nobody re-examines, and a list nobody has to argue for stops being a
# signal.
#
# The point of this check is not that published notes are immutable. #556 completed the
# migration note of a **breaking** release whose entry was incomplete, the day it shipped, with
# its own issue — that was the right call and a check forbidding it would have been wrong. What
# must not happen is an edit nobody sees, which is what #630 and #668 were.
#
# So: adding a line here is part of the change, and it belongs in the PR that edits the section.
# A reviewer then reads why.
allowed() {
  case "$1" in
    # Deliberate, and the reason this check does not simply forbid editing. #556 completed the
    # migration note of a **breaking** release whose entry named plan syntax only, while the
    # same rule governs `~{…}` in a template. Added the day v0.9.0 shipped, with its own issue.
    0.9.0) return 0 ;;

    # NOT deliberate: this is the defect itself, first instance, and it predates the check.
    # `2c11460` (#277, 7 Aug) appended a `### Fixed` entry to a section tagged on 4 Aug.
    #
    # Left in place rather than repaired, and the reasoning is worth reading before changing it:
    # moving the entry to the version that actually shipped it would make *that* section diverge
    # from *its* tag, so any repair of a historical misfile just moves the divergence. And the
    # authoritative record is already correct — the GitHub release notes for v0.2.2 were
    # generated from the tag and do not contain this entry.
    0.2.2) return 0 ;;
  esac
  return 1
}

versions="$(grep -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' "$file" | tr -d '#[] ' || true)"
[ -n "$versions" ] || { printf 'OK — no released section yet\n'; exit 0; }

# Tags are the reference, so their absence is a broken check rather than a passing one. A
# shallow clone has none: CI fetches them for this job on purpose.
if [ -z "$(git -C "$root" tag -l 2>/dev/null)" ]; then
  fail "no tags in this checkout, so a released section cannot be compared — run \`git fetch --tags\`"
fi

checked=0
skipped=""
allowedList=""
for v in $versions; do
  # A section rolled but not yet tagged is the normal state of the release PR itself: the
  # changelog is rolled into `develop` first, and the tag comes after the merge to `main`.
  if ! git -C "$root" rev-parse -q --verify "refs/tags/v$v" >/dev/null 2>&1; then
    skipped="$skipped v$v"
    continue
  fi
  if allowed "$v"; then
    allowedList="$allowedList v$v"
    continue
  fi
  released="$(git -C "$root" show "v$v:CHANGELOG.md" | section "$v")"
  current="$(section "$v" < "$file")"
  if [ "$released" != "$current" ]; then
    printf '\033[1;31m--- [%s] as published (v%s) +++ as it is now\033[0m\n' "$v" "$v" >&2
    diff <(printf '%s\n' "$released") <(printf '%s\n' "$current") >&2 || true
    fail "the [$v] section has changed since it was published.
      An entry for work that is not out yet belongs under [Unreleased] — that is #630 and #668.
      If the edit is deliberate, add the version to allowed() in this script with the reason."
  fi
  checked=$((checked + 1))
done

if [ -n "$skipped" ]; then
  printf 'note: not yet tagged, so not compared:%s\n' "$skipped"
fi
if [ -n "$allowedList" ]; then
  printf 'note: not compared, named in allowed() with the reason:%s\n' "$allowedList"
fi
printf '\033[1;32mOK — %d released changelog section(s) match their tag\033[0m\n' "$checked"
