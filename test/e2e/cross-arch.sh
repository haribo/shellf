#!/usr/bin/env bash
# Cross-architecture end-to-end test: an amd64 control host provisioning an arm64 target
# (#457, ADR-0048).
#
# #453 shipped the feature and its PR said plainly that no arm64 host had been available,
# so this path had never been executed. It was then run by hand and it worked — this
# script is that run, kept, so the next change to the transport cannot take it away in
# silence.
#
# What it asserts, in order:
#   1. a BARE build (no embedded peer) refuses the arm64 target, and pushes nothing
#   2. a BUNDLED build applies the plan
#   3. the agent left on the target is an aarch64 ELF  ← the assertion that matters
#   4. a second run is idempotent
#
# Assertion 3 is the point. A run can go green for reasons that have nothing to do with
# the architecture; reading `e_machine` out of the ELF header is what separates "it
# worked" from "it pushed the right binary".
#
# The target is emulated (QEMU + binfmt), so this is slower than the native harness and
# lives in its own CI job. Opt-in like run.sh: it needs Docker.
set -euo pipefail

: "${SHELLF_E2E:?set SHELLF_E2E=1 to run the end-to-end harness (needs Docker)}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
cname="shellf-cross-$$"
# The architecture is in the tag, not only in the build flag: a tag that says nothing
# about it can name an image of the wrong architecture, and `docker run --platform` then
# refuses to use it and tries to *pull* `shellf-cross`, failing with "pull access denied"
# — a message about registry permissions for what is an architecture mismatch. Measured
# while writing this script, not imagined.
image_tag="shellf-cross-arm64:$(sha256sum "$here/cross/Dockerfile" | cut -c1-12)"

cleanup() {
  docker rm -f "$cname" >/dev/null 2>&1 || true
  rm -rf "$work"
  # The embed slot is committed empty and must go back that way, or a later build silently
  # ships an agent this script left behind.
  : > "$root/internal/agentbin/peer/agent"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() {
  printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2
  [ -n "${out:-}" ] && printf '%s\n' "$out" >&2
  exit 1
}

# --- the two binaries under test -------------------------------------------------------

say "build the agent pair (two passes, as release.yaml does)"
( cd "$root"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$work/bare-arm64" ./cmd/shellf
  # Bare: this is the control binary that must REFUSE, so it is built before the peer is
  # dropped in.
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$work/shellf-bare" ./cmd/shellf
  cp "$work/bare-arm64" internal/agentbin/peer/agent
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags bundled -o "$work/shellf-bundled" ./cmd/shellf
  : > internal/agentbin/peer/agent
)

# --- the emulated target ---------------------------------------------------------------

# Rebuild unless an image of the RIGHT architecture is already there. Checking existence
# alone is not enough: a stale same-named image built for the host architecture would be
# reused, and the run would fail three steps later for a reason that names none of this.
cached_arch="$(docker image inspect "$image_tag" --format '{{.Architecture}}' 2>/dev/null || true)"
if [ "$cached_arch" != "arm64" ]; then
  say "build the arm64 target image ($image_tag)"
  # `--pull` so the base image is fetched for arm64 rather than reused from whatever
  # architecture a previous `docker pull debian:stable-slim` left in the local cache.
  docker build --platform linux/arm64 --pull -q -t "$image_tag" -f "$here/cross/Dockerfile" "$here/cross" >/dev/null \
    || fail "could not build an arm64 image — is binfmt/QEMU registered? (docker run --privileged --rm tonistiigi/binfmt --install arm64)"
fi

say "start the arm64 target"
docker run -d --platform linux/arm64 --name "$cname" "$image_tag" >/dev/null \
  || fail "could not start the arm64 target — QEMU/binfmt missing, or $image_tag is not an arm64 image"

# A leg that quietly ran against an amd64 container would assert nothing at all while
# staying green — so the architecture is confirmed before anything else happens.
arch="$(docker exec "$cname" uname -m)"
[ "$arch" = "aarch64" ] || fail "the target must be aarch64, got '$arch' — is binfmt/QEMU registered?"
say "target architecture confirmed: $arch"

ssh-keygen -t ed25519 -N '' -f "$work/id" -q
docker cp "$work/id.pub" "$cname:/home/deploy/.ssh/authorized_keys" >/dev/null
docker exec "$cname" chown -R deploy:deploy /home/deploy/.ssh
docker exec "$cname" chmod 600 /home/deploy/.ssh/authorized_keys

ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$cname")"
for _ in $(seq 1 60); do
  docker exec "$cname" sh -c 'pgrep -x sshd >/dev/null' && break
  sleep 1
done
docker exec "$cname" sh -c 'pgrep -x sshd >/dev/null' || fail "sshd never came up on the target"

# --- the plan ---------------------------------------------------------------------------

mkdir -p "$work/proj/plans" "$work/proj/inventories"
printf 'host pi = { address: "%s", user: "deploy", key: "%s/id" }\n' "$ip" "$work" \
  > "$work/proj/inventories/hosts.shellf"
cat > "$work/proj/plans/cross.shellf" <<'PLAN'
on pi {
  dir.ensure("/tmp/shellf-cross")
  file.write("/tmp/shellf-cross/hello.txt", "pushed from an amd64 control host\n")
}
PLAN

run_plan() { # $1 = binary, rest = extra flags
  local bin="$1"; shift
  ( cd "$work/proj" && "$bin" run --inventory inventories/hosts.shellf --insecure "$@" plans/cross.shellf )
}

# 1 ------------------------------------------------------------------------------------
say "1. a bare build refuses the arm64 target, and pushes nothing"
if out="$(run_plan "$work/shellf-bare" 2>&1)"; then
  fail "a bare amd64 build must refuse an arm64 target, it succeeded"
fi
printf '%s\n' "$out" | grep -q "no arm64 agent" \
  || fail "the refusal must name the missing agent: $out"
docker exec "$cname" sh -c 'ls /tmp/shellf-agent-* >/dev/null 2>&1' \
  && fail "nothing may be pushed to a target the binary cannot serve"

# 2 ------------------------------------------------------------------------------------
say "2. a bundled build provisions the arm64 target"
out="$(run_plan "$work/shellf-bundled")" || fail "the bundled build failed against arm64: $out"
printf '%s\n' "$out" | grep -q "ok.created" || fail "the plan did not create anything: $out"
docker exec "$cname" test -f /tmp/shellf-cross/hello.txt \
  || fail "apply did not write the file on the target"

# 3 ------------------------------------------------------------------------------------
say "3. the agent on the target is an aarch64 ELF"
# Byte 18 of the ELF header is e_machine; 0xB7 is EM_AARCH64. Read from the file that was
# actually pushed, because "the run went green" does not say which binary ran.
machine="$(docker exec "$cname" sh -c \
  'od -An -tx1 -j18 -N1 "$(ls /tmp/shellf-agent-* | head -1)"' | tr -d ' \n')"
[ "$machine" = "b7" ] \
  || fail "the pushed agent must be aarch64 (e_machine=b7), got '$machine'"

# 4 ------------------------------------------------------------------------------------
say "4. a second run is idempotent"
out="$(run_plan "$work/shellf-bundled")" || fail "the second run failed: $out"
printf '%s\n' "$out" | grep -q "ok.already" || fail "a second run must converge: $out"
printf '%s\n' "$out" | grep -q "ok.created\|ok.written" \
  && fail "a second run must change nothing: $out"

printf '\n\033[1;32mOK — an amd64 control host provisioned an %s target\033[0m\n' "$arch"
