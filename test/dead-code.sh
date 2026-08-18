#!/usr/bin/env bash
# No function may sit unreachable from the binary (#447).
#
# A guard with an empty list, and it is worth stating why that is possible: everything the
# analyser flagged when this was written was either a convenience wrapper nobody ran
# (`ParsePlan`, `EvalDef`, `EvalDefWith`, `agent.Serve`, `Channel.Ask`) or a residue whose
# last caller had been removed (`UsesPrimitive` in #392, `renderTemplate` in #334). Deleting
# them left nothing behind, so an exemption list would be a place for the next one to hide.
#
# Run **without** `-test`: with it, the analyser reports nothing at all, because every
# function is reachable from some test. Without it, a function only tests call shows up —
# which is the point. A test-only helper belongs to the package it tests, unexported, and
# `parsePlan` was made so rather than exempted.
#
# What this does NOT catch, and why it does not replace TestDispatch_IsNotShadowedByADef:
# code that is statically reachable but semantically dead. `engine.FileCopy` was
# instantiated in a switch and never ran, because a def of the same name always won at run
# time (#445). No reachability analysis sees that.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"

fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

cd "$root"
# stdout carries the findings, stderr the toolchain's own chatter — `go run` prints
# "go: downloading …" the first time. Folding the two made the download itself look like
# dead code, which is how this script failed on its own first run.
err="$(mktemp)"
trap 'rm -f "$err"' EXIT
out="$(go run golang.org/x/tools/cmd/deadcode@v0.39.0 ./cmd/shellf 2>"$err")" \
  || fail "deadcode could not run: $(cat "$err")"

if [ -n "$out" ]; then
  printf '%s\n' "$out" >&2
  fail "unreachable code — delete it, or make it reachable (#447)"
fi

printf '\033[1;32mOK — every function is reachable from the binary\033[0m\n'
