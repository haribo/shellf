#!/usr/bin/env bash
# Coverage ratchet: fail if total statement coverage drops below the committed
# floor (.github/coverage-floor.txt). The floor is a mechanical non-regression
# gate — raising it is part of test PRs, lowering it is a deliberate, reviewed
# act. 100% is explicitly NOT the target: the metric stays simple (total
# statements); interface markers and getters are out of the *ambition*, not
# carved out of the *measurement*. See issue #132.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
floor="$(cat "$root/.github/coverage-floor.txt")"
profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

( cd "$root" && go test ./... -coverprofile="$profile" >/dev/null )
total="$(go tool cover -func="$profile" | awk '/^total:/ {sub(/%/, "", $3); print $3}')"

awk -v t="$total" -v f="$floor" 'BEGIN {
  if (t + 0 < f + 0) {
    printf "FAIL: total coverage %.1f%% is below the floor %.1f%%\n", t, f
    printf "      raise coverage, or lower .github/coverage-floor.txt deliberately\n"
    exit 1
  }
  printf "OK: total coverage %.1f%% >= floor %.1f%%\n", t, f
}'
