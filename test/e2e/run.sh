#!/usr/bin/env bash
# Self-spinning end-to-end test for shellf's real SSH transport + resident agent.
#
# It stands up a throwaway Debian container running sshd, then drives the actual
# `shellf run` binary against it and asserts the three properties that matter:
#   1. --dry-run is inert   (nothing is created on the target)
#   2. apply provisions   (the marker tree appears)
#   3. re-apply is idempotent (a second real run reports zero changes)
#
# Opt-in because it needs Docker and pulls an image: it runs only when SHELLF_E2E
# is set. Everything (keypair, container, workdir) is ephemeral and torn down.
set -euo pipefail

: "${SHELLF_E2E:?set SHELLF_E2E=1 to run the end-to-end harness (needs Docker)}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
cname="shellf-e2e-$$"
image="debian:stable-slim"

cleanup() { docker rm -f "$cname" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

say "build shellf"
( cd "$root" && go build -o "$work/shellf" ./cmd/shellf )

say "start throwaway sshd container ($image)"
docker run -d --name "$cname" "$image" sleep 600 >/dev/null
docker exec "$cname" sh -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq openssh-server >/dev/null
  useradd -m -s /bin/bash deploy
  mkdir -p /home/deploy/.ssh /run/sshd
  chmod 700 /home/deploy/.ssh
'

say "install an ephemeral key"
ssh-keygen -t ed25519 -N '' -f "$work/id" -q
docker cp "$work/id.pub" "$cname:/home/deploy/.ssh/authorized_keys"
docker exec "$cname" sh -c '
  chown -R deploy:deploy /home/deploy/.ssh
  chmod 600 /home/deploy/.ssh/authorized_keys
  /usr/sbin/sshd
'

ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$cname")"
[ -n "$ip" ] || fail "could not read container IP"

# Wait for sshd to accept connections (image boot + first apt can lag).
for i in $(seq 1 30); do
  if docker exec "$cname" sh -c 'pgrep sshd >/dev/null'; then break; fi
  sleep 1
done

cat > "$work/inventory.shellf" <<EOF
host target = {
    address: "$ip",
    user: "deploy",
    key: "$work/id",
    role: "edge"
}
EOF

# A secret provided by file (never on the command line); the plan writes it and
# shellf must redact it from every report.
printf 'SEKRET-abc123' > "$work/secret"
run() { "$work/shellf" run --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$@" "$here/plan.shellf"; }

say "1. check mode is inert (previews 'would', touches nothing)"
out="$(run --dry-run 2>&1)"; printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'would' || fail "check mode should preview a 'would' outcome"
docker exec "$cname" test -e /tmp/shellf-e2e && fail "check mode created state on the target"

say "1b. status on a fresh host shows drift (current → desired)"
out="$("$work/shellf" status --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$here/plan.shellf" 2>&1)"
printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'present: false → true' || fail "status should show present drift on a fresh host"
# #334: `status` runs each def's observe, and an observe may call a control-host
# primitive — so status needs the channel too. Without it every template reports
# err.agent while the greps above still pass, which is how this was missed.
printf '%s' "$out" | grep -q 'err.agent' && fail "status could not reach the control host (a def's observe failed)"

say "2. apply provisions the marker tree (created/written)"
out="$(run 2>&1)"; printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'created' || fail "apply should report dir 'created'"
printf '%s' "$out" | grep -q 'written' || fail "apply should report file 'written'"
docker exec "$cname" test -f /tmp/shellf-e2e/marker || fail "apply did not write the marker"
docker exec "$cname" test -f /tmp/shellf-e2e/ready  || fail "apply did not write ready"
# the user def `mark` (sibling mark.shellf) ran on the target
docker exec "$cname" grep -q "made by a user def" /tmp/shellf-e2e/custom || fail "the user def did not run on the target"
# the imported def `g.hello` (./shared) ran on the target
docker exec "$cname" test -f /tmp/shellf-e2e/imported || fail "the imported def did not run on the target"
# the secret is redacted in shellf's output but written for real on the target
printf '%s' "$out" | grep -q 'SEKRET-abc123' && fail "the secret leaked into shellf's output"
printf '%s' "$out" | grep -q 'content=\*\*\*' || fail "the secret was not redacted in the report"
docker exec "$cname" grep -q 'SEKRET-abc123' /tmp/shellf-e2e/sec || fail "the secret was not written to the target"
# the agent workdir (where the secret-bearing request lands) is on tmpfs, not on
# persistent disk (ADR-0025): no shellf request file is left under /tmp.
docker exec "$cname" sh -c 'ls -d /dev/shm/shellf-*/ >/dev/null 2>&1' || fail "the workdir is not on tmpfs (/dev/shm)"
docker exec "$cname" sh -c 'ls /tmp/shellf-*/req-*.json >/dev/null 2>&1' && fail "a request file leaked onto persistent /tmp"
# a control-host template file was rendered and delivered to the target
docker exec "$cname" grep -q 'generated by shellf' /tmp/shellf-e2e/motd || fail "the template was not rendered onto the target"
docker exec "$cname" grep -q 'token=SEKRET-abc123' /tmp/shellf-e2e/motd || fail "the template did not interpolate the value"
# a per-host inventory var reached the template render (ADR-0024)
docker exec "$cname" grep -q 'role=edge' /tmp/shellf-e2e/motd || fail "the per-host var did not reach the template"
# the `for` loop unrolled and both iterations ran on the target
docker exec "$cname" test -d /tmp/shellf-e2e/one && docker exec "$cname" test -d /tmp/shellf-e2e/two || fail "the for loop did not run both iterations"
# dir-copy delivered the control-host tree, text + binary, byte-for-byte (ADR-0028)
docker exec "$cname" grep -q 'delivered by dir-copy' /tmp/shellf-e2e/delivered/hello.txt || fail "dir-copy did not deliver the text file"
want_bin="$(sha256sum "$here/tree/assets/logo.bin" | cut -d' ' -f1)"
got_bin="$(docker exec "$cname" sha256sum /tmp/shellf-e2e/delivered/assets/logo.bin | cut -d' ' -f1)"
[ "$want_bin" = "$got_bin" ] || fail "dir-copy corrupted the binary file ($want_bin != $got_bin)"
# `with { }` overrode a variable for one template call and one shell call (ADR-0022)
docker exec "$cname" grep -q 'greeting=hello-with' /tmp/shellf-e2e/greet || fail "the template `with` override did not render"
docker exec "$cname" grep -q 'note-with' /tmp/shellf-e2e/note || fail "the shell `with` override did not reach the env"

say "3. re-apply is idempotent (observe converges → 'already', nothing (re)created)"
out="$(run 2>&1)"; printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'already' || fail "re-apply should report 'already' (guard skip)"
if printf '%s' "$out" | grep -qE 'created|written'; then
  fail "re-apply mutated; expected a no-op"
fi

say "4. status reports the converged state (no drift arrows)"
out="$("$work/shellf" status --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$here/plan.shellf" 2>&1)"
printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'present: true' || fail "status should report present: true"
printf '%s' "$out" | grep -q 'err.agent' && fail "status could not reach the control host (a def's observe failed)"
# a template's own state must be reported, not just the instructions around it (#334)
printf '%s' "$out" | grep -q 'synced: true' || fail "status should report a template as synced"
if printf '%s' "$out" | grep -q '→'; then
  fail "status after apply should show no drift arrows (all converged)"
fi

say "PASS — check inert, apply provisioned, re-apply idempotent, status converged"
