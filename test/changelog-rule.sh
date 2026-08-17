#!/usr/bin/env bash
# Every `[Unreleased]` entry stays a headline, not the story (#424).
#
# A guard, like the coverage floor and the def-coverage ratchet, and for the same reason:
# the rule existed in prose from the start — `git-workflow.md` says a PR "adds **a line**"
# — and it eroded anyway. Measured when this was written: 48 entries, median 91 words,
# longest 471, 37 over the ceiling. The released sections, written under the same prose,
# run 18 to 56 words each. Prose did not hold; a check does.
#
# It matters because `release.yaml` publishes the `[X.Y.Z]` section of the **tagged commit**
# as the GitHub release notes. The next tag freezes whatever is in `[Unreleased]` as the
# public notes of that version, and there is no editing pass afterwards.
#
# Checked over `[Unreleased]` only. The released sections are published notes and are not
# rewritten (#424).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
file="$root/CHANGELOG.md"

# 60 words is loose: two full sentences plus a reference land around 40. An entry that
# trips this is telling the story, which the issue it names already holds.
max_words=60

fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

[ -f "$file" ] || fail "no CHANGELOG.md at $file"

# The Unreleased section: from its heading to the next version heading.
section="$(awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f' "$file")"
[ -n "$section" ] || fail "no [Unreleased] section — the release flow expects one"

# One entry per `- ` at column 0; its continuation lines are indented.
mapfile -t problems < <(printf '%s\n' "$section" | awk -v max="$max_words" '
  function flush() {
    if (entry == "") return
    n = split(entry, w, /[ \t]+/)
    words = 0
    for (i = 1; i <= n; i++) if (w[i] != "") words++
    if (words > max) printf "too long (%d words): %s\n", words, head
    if (entry !~ /#[0-9]+/)  printf "no issue reference: %s\n", head
    entry = ""
  }
  /^- / { flush(); entry = $0; head = substr($0, 3, 60); next }
  /^  +[^ ]/ { if (entry != "") entry = entry " " $0; next }
  { flush() }
  END { flush() }
')

# Category order, as git-workflow.md specifies it. A section that invents a heading or
# reorders them makes the released notes read differently from every other version.
order="Added Changed Deprecated Removed Fixed Security"
seen=""
while read -r cat; do
  [ -n "$cat" ] || continue
  case " $order " in *" $cat "*) ;; *) fail "unknown category '### $cat' — use: $order" ;; esac
  # One heading per category: a second `### Changed` splits entries a reader expects to
  # find together, and the release notes carry that split verbatim.
  case " $seen " in *" $cat "*) fail "'### $cat' appears twice — one heading per category" ;; esac
  # Each category must appear after the ones before it in the canonical order.
  for prev in $seen; do
    p_i=0; c_i=0; i=0
    for k in $order; do
      i=$((i + 1))
      [ "$k" = "$prev" ] && p_i=$i
      [ "$k" = "$cat" ] && c_i=$i
    done
    [ "$p_i" -lt "$c_i" ] || fail "categories out of order: '$cat' after '$prev' — use: $order"
  done
  seen="$seen $cat"
done < <(printf '%s\n' "$section" | awk '/^### /{print $2}')

if [ ${#problems[@]} -gt 0 ]; then
  printf '%s\n' "${problems[@]}" >&2
  fail "${#problems[@]} changelog entr(y|ies) break the rule — see docs/git-workflow.md § Changelog"
fi

printf '\033[1;32mOK — every [Unreleased] entry is within %d words and names its issue\033[0m\n' "$max_words"
