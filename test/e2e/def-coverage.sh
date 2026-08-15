#!/usr/bin/env bash
# Every stdlib def must be exercised against a real target (#367).
#
# A ratchet, like the coverage floor: it lists defs that no e2e plan calls, and fails when
# that list is not empty. A new def therefore arrives with its coverage or turns the build
# red — prose in a contributing guide would not have caught the gap this was written for
# (5 defs covered out of 36).
#
# It counts **calls in plans**, not mentions. The first version of this check matched any
# occurrence anywhere under test/e2e/, so a def named in a comment of run.sh counted as
# tested. Two defs passed that way and neither had ever run.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"

# Defs that cannot be exercised honestly against the harness container. Each is named with
# its reason, on the record: a silent exemption is a hole nobody re-examines.
exempt() {
  case "$1" in
    # Installs docker on the target. The harness image ships it already, so calling this
    # would either no-op against a state the plan did not create, or reinstall the daemon
    # the transfer tests depend on.
    docker.install) return 0 ;;
  esac
  return 1
}

defs="$(grep -rhoE '^(override )?def [a-z][a-z0-9.-]*\(' "$root"/internal/std/*/*.shellf \
        | sed -E 's/^(override )?def //; s/\($//' \
        | while read -r name; do echo "$name"; done)"

# Pair each def with its package directory to get the qualified name the plans call.
qualified="$(
  for f in "$root"/internal/std/*/*.shellf; do
    pkg="$(basename "$(dirname "$f")")"
    grep -oE '^(override )?def [a-z][a-z0-9.-]*\(' "$f" \
      | sed -E "s/^(override )?def /$pkg./; s/\($//"
  done | sort -u
)"

# Calls made by the e2e plans and their defs, comments stripped.
called="$(
  cat "$root"/test/e2e/plans/*.shellf "$root"/test/e2e/defs/*/*.shellf 2>/dev/null \
    | sed 's/#.*$//' \
    | grep -oE '\b[a-z][a-z0-9_-]*\.[a-z][a-z0-9-]*\s*\(' \
    | sed -E 's/\s*\($//' | sort -u
)"

missing=""
for d in $qualified; do
  if exempt "$d"; then continue; fi
  if ! printf '%s\n' "$called" | grep -qx "$d"; then
    missing="$missing $d"
  fi
done

total="$(printf '%s\n' "$qualified" | wc -l)"
if [ -n "$missing" ]; then
  printf 'FAIL: %d stdlib def(s) are never called by an e2e plan:\n' "$(echo $missing | wc -w)"
  for d in $missing; do printf '      %s\n' "$d"; done
  printf '\n      add it to test/e2e/plans/coverage.shellf, or exempt it in this script\n'
  printf '      with the reason — every def is exercised against a real target (#367)\n'
  exit 1
fi
printf 'OK: every stdlib def (%s) is exercised by an e2e plan\n' "$total"
