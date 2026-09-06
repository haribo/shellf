#!/usr/bin/env bash
# Pin the Go toolchain a check runs with, to the one `go.mod` names (#589).
#
# `GOTOOLCHAIN=auto` only ever *upgrades*: a machine on 1.27 is already past `toolchain
# go1.26.6`, so Go uses what it has and the check measures something CI never measures.
# Measured on the same tree: coverage read 80.9% under 1.27 and 81.8% under 1.26.6, which
# is the difference between failing the floor and clearing it, and `deadcode` panics under
# 1.27 because the version it is pinned to predates that syntax.
#
# Sourced, not executed: it exports into the caller's environment.
#
#   . "$(dirname "${BASH_SOURCE[0]}")/toolchain.sh"
#
# Reading the line rather than hardcoding it: a second copy of the version is a copy that
# drifts, and this file would be the one nobody thinks to update.
_shellf_toolchain() {
  local root line
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  line="$(awk '/^toolchain /{print $2; exit}' "$root/go.mod")"
  if [ -z "$line" ]; then
    # No toolchain line: `go 1.26.0` is a minimum, not a pin, so there is nothing to
    # reproduce and the caller's own toolchain is as good an answer as any.
    return 0
  fi
  export GOTOOLCHAIN="$line"
}
_shellf_toolchain
