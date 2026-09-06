#!/usr/bin/env bash
# golangci-lint, at the version CI runs (#589).
#
# It catches what `go vet` does not: #588 shipped two test helpers left dead by a move, and
# the only thing that saw them was this linter — after the push, because there was no way
# to run it here.
#
# The version is read from the workflow rather than written twice: a second copy is a copy
# that drifts, and the two would disagree exactly when it matters.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=test/toolchain.sh
. "$here/toolchain.sh"
root="$(cd "$here/.." && pwd)"

fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

version="$(awk '/golangci-lint-action/{found=1} found && /version:/{print $2; exit}' \
  "$root/.github/workflows/lint.yaml")"
[ -n "$version" ] || fail "no golangci-lint version in .github/workflows/lint.yaml"

cd "$root"
# `go run` rather than a system install: the version is then the workflow's, on any machine,
# with nothing to keep in sync by hand.
go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$version" run "$@"
printf '\033[1;32mOK — golangci-lint %s is clean\033[0m\n' "$version"
