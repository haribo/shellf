#!/usr/bin/env bash
# Every stdlib def that declares an `observe` has an adverse plan, or is named here (#674).
#
# A ratchet, not a gate. `def-coverage.sh` can be absolute because it sits at 48/49; this sits at
# 22/34, so an absolute rule would be a wall nobody could build. What it does instead is make the
# gap **countable and shrinking**:
#
#   - a def with no adverse plan and no line below turns the build red;
#   - a def named below that *has* one turns it red too, so the list cannot rot into a permanent
#     excuse.
#
# The second half is what `def-coverage.sh` does not need and this does.
#
# Why this matters more than a coverage number: every defect of the last two days came from an
# `observe` asking a weaker question than its `apply` guarantees — #486, #594's four, #635, #636,
# #658, #676, #680. Three defs were audited on 14 Sep and three were wrong. That is the rate of
# this class, and `adverse-cases.md` used to say the protection was "#489 staying open". #489 is
# closed.
#
# **It counts calls in adverse plans only**, never mentions in `run.sh`. A def can be covered by a
# bespoke step there — several are — but "is this step a hostile-state test" is not decidable by
# grep, and matching a def name against a shell script is the exact mistake `def-coverage.sh`
# records making ("a def named in a comment of run.sh counted as tested. Two defs passed that way
# and neither had ever run"). So a def covered by a step is named below **with the step**, which
# puts the human judgement where a reader can check it.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"

fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

# Defs with no adverse plan. Each line says why — a step that covers it, or an admission of debt.
# Adding one is part of the change that needs it, and it belongs in the PR where a reviewer reads
# the reason.
no_adverse_plan() {
  case "$1" in
    # --- covered by a bespoke step in run.sh, which asserts the machine ---------------------
    # Its setup removes the package on every run, so it cannot live in an adverse plan that the
    # convergence sweep also reads.
    apt.install)     return 0 ;;  # step 23 (#486)
    # A refusal halts the plan, and the adverse harness reads any `err.` as red.
    file.download)   return 0 ;;  # step 28 (#599)
    postgres.config) return 0 ;;  # step 30 (#618)
    git.clone)       return 0 ;;  # step 31 (#679)
    # The firewall has to be *down* for these, which is a state the other cases run inside.
    ufw.enable)      return 0 ;;  # step 26 (#515)
    ufw.default)     return 0 ;;  # step 26 (#515)
    ufw.open)        return 0 ;;  # step 26b (#553)

    # --- never exercised at all ---------------------------------------------------------------
    # Exempt from def-coverage.sh too, with its reason: it installs docker on a host whose image
    # already ships it. The least verified instruction in the stdlib has the widest reach.
    docker.install)  return 0 ;;

    # --- debt: no hostile-state test anywhere -------------------------------------------------
    # This list is the point of the script. It shrinks or it does not, and either is visible.
    docker.network)  return 0 ;;
    postgres.hba)    return 0 ;;
    postgres.role)   return 0 ;;
    # Checked on a real target while fixing #676: sshd honours an included drop-in whatever owns
    # it, so this def's observe already matches its apply. A case would still pin that.
    sshd.config)     return 0 ;;
  esac
  return 1
}

# Defs declaring an `observe`, qualified by their package directory.
observing="$(
  for f in "$root"/internal/std/*/*.shellf; do
    pkg="$(basename "$(dirname "$f")")"
    awk -v pkg="$pkg" '
      /^(override )?def [a-z][a-z0-9.-]*\(/ { name = $0; sub(/^(override )?def /, "", name); sub(/\(.*/, "", name); seen = 0 }
      /^    observe \{/ { if (name != "" && !seen) { print pkg "." name; seen = 1 } }
    ' "$f"
  done | sort -u
)"

# Calls made by the adverse plans, comments stripped — the same counting rule def-coverage.sh
# uses, for the same reason.
covered="$(
  cat "$root"/test/e2e/plans/adverse-*.shellf 2>/dev/null \
    | sed 's/#.*$//' \
    | grep -oE '\b[a-z][a-z0-9_-]*\.[a-z][a-z0-9-]*\s*\(' \
    | sed -E 's/\s*\($//' | sort -u
)"

undeclared=""   # no plan, and not named above
stale=""        # named above, but a plan exists now
for d in $observing; do
  if printf '%s\n' "$covered" | grep -qx "$d"; then
    if no_adverse_plan "$d"; then stale="$stale $d"; fi
  else
    if ! no_adverse_plan "$d"; then undeclared="$undeclared $d"; fi
  fi
done

if [ -n "$undeclared" ]; then
  printf 'FAIL: %d observing def(s) have no adverse plan and are not named in this script:\n' "$(echo $undeclared | wc -w)"
  for d in $undeclared; do printf '      %s\n' "$d"; done
  printf '\n      write test/e2e/plans/adverse-%s.shellf, or add the def to no_adverse_plan()\n' '<def>'
  printf '      with the reason — a hostile starting state is what catches an observe that\n'
  printf '      asks less than its apply guarantees (#674)\n'
  exit 1
fi

if [ -n "$stale" ]; then
  printf 'FAIL: %d def(s) named in this script now have an adverse plan:\n' "$(echo $stale | wc -w)"
  for d in $stale; do printf '      %s\n' "$d"; done
  printf '\n      remove them from no_adverse_plan() — the list shrinks, or it is an excuse\n'
  exit 1
fi

total="$(printf '%s\n' "$observing" | wc -l)"
# `if`, not `&&`: under `pipefail` a loop whose last command returns 1 fails the whole
# pipeline, and `set -e` then kills the script — so this gate would have been red on every
# run, for a reason having nothing to do with coverage. An `if` with no `else` returns 0.
named="$(for d in $observing; do if no_adverse_plan "$d"; then echo x; fi; done | wc -l)"
printf '\033[1;32mOK — %d of %d observing defs have an adverse plan; %d named with a reason\033[0m\n' \
  "$((total - named))" "$total" "$named"
