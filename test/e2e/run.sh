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
# The target image is built from test/e2e/Dockerfile and tagged by that file's hash, so a
# run reuses it and only pays the build when the image itself changes (#366). The old
# harness apt-installed sshd into debian:stable-slim on every run — and still could not
# exercise systemd, docker or ufw.
image_tag="shellf-e2e:$(sha256sum "$here/Dockerfile" | cut -c1-12)"

cleanup() { docker rm -f "$cname" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

say "build shellf"
( cd "$root" && go build -o "$work/shellf" ./cmd/shellf )

if ! docker image inspect "$image_tag" >/dev/null 2>&1; then
  say "build the target image ($image_tag) — once, then reused"
  docker build -q -t "$image_tag" -f "$here/Dockerfile" "$here" >/dev/null
fi

say "start the throwaway target ($image_tag)"
# systemd as PID 1 plus a docker daemon inside. `--privileged` is the documented way; the
# narrower recipe (SYS_ADMIN alone) died on the CI runner with exit 255 and no logs.
#
# `--cgroupns=private` is not a detail — it is the whole safety of this line. A container
# whose PID 1 is systemd behaves as a machine manager: it takes ownership of the cgroup
# tree it can see and reconciles it against the units it knows. Given the host's tree
# (`--cgroupns=host` plus a rw bind mount of /sys/fs/cgroup), it finds `user.slice` and
# `session-N.scope` accounted for by no unit of its own, and tidies them away. On systemd,
# session membership *is* cgroup position, so `systemd-logind` concludes the user logged
# out — and the developer's graphical session dies. Observed twice on an Arch/GNOME
# machine, one second after container start, with no crash of any kind.
#
# A private cgroup namespace gives it its own root to manage, and the explicit bind mount
# is then unnecessary: `--privileged` already mounts /sys/fs/cgroup writable inside.
docker run -d --name "$cname" \
  --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock \
  "$image_tag" >/dev/null

# The guard for the above, asserted rather than trusted: inside a private namespace PID 1
# sits at its own root (`0::/`). Under the host namespace it reads
# `0::/system.slice/docker-<id>.scope`, which is exactly the configuration that reached
# out and logged the operator out. A comment claiming a small blast radius is what hid
# this the first time; this is the same claim, checkable.
# Two checks, because each misses what the other catches.
#
# Structural: under a private namespace PID 1 sits at its own root (`/`, or `/init.scope`
# once systemd has moved itself there). Under the host's, Docker places it under
# `/system.slice/docker-<id>.scope` — so the path carrying `docker-` *is* the dangerous
# configuration. Read the v2 line rather than the whole file: legacy v1 lines ride along,
# and the first version of this guard compared the lot and would have failed every run.
pid1_cgroup="$(docker exec "$cname" sh -c 'grep "^0::" /proc/1/cgroup' 2>/dev/null || true)"
case "${pid1_cgroup:-none}" in
  *docker-*|none)
    docker rm -f "$cname" >/dev/null 2>&1
    fail "the target sits in the host cgroup tree (${pid1_cgroup:-<no answer>}) — its systemd would manage the host's sessions" ;;
esac

# Semantic, and the one that matters: the host's login sessions must be invisible from
# inside. Their presence is what a container's systemd tidied away, taking the operator's
# graphical session with it — session membership on systemd *is* cgroup position.
if docker exec "$cname" sh -c 'find /sys/fs/cgroup -maxdepth 4 -name "session-*.scope" | head -1' 2>/dev/null | grep -q .; then
  docker rm -f "$cname" >/dev/null 2>&1
  fail "the target can see host login sessions — it would log the operator out"
fi

# Wait for the boot to settle: `systemctl is-system-running` answers `running` (or
# `degraded`, which a masked-unit container reaches legitimately) once units have started.
booted=""
for _ in $(seq 1 60); do
  state="$(docker exec "$cname" systemctl is-system-running 2>&1 || true)"
  case "$state" in running|degraded) booted=yes; break ;; esac
  sleep 0.5
done
if [ -z "$booted" ]; then
  # Say why, not just that. A boot failure here is environment-specific — cgroup layout,
  # missing capability, a unit that cannot start — and "never finished booting" sends the
  # reader nowhere.
  printf 'last state: %s\n' "${state:-<no answer>}"
  printf 'container: %s\n' "$(docker inspect -f '{{.State.Status}} exit={{.State.ExitCode}}' "$cname" 2>&1)"
  echo '--- container logs ---'; docker logs --tail 40 "$cname" 2>&1 || true
  echo '--- failed units ---'; docker exec "$cname" systemctl --failed --no-pager 2>&1 || true
  fail "the target never finished booting"
fi

say "install an ephemeral key"
ssh-keygen -t ed25519 -N '' -f "$work/id" -q
docker cp "$work/id.pub" "$cname:/home/deploy/.ssh/authorized_keys"
docker exec "$cname" sh -c '
  chown -R deploy:deploy /home/deploy/.ssh
  chmod 600 /home/deploy/.ssh/authorized_keys
  systemctl restart ssh
'

ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$cname")"
[ -n "$ip" ] || fail "could not read container IP"

# Wait for sshd to accept connections.
for _ in $(seq 1 30); do
  if docker exec "$cname" systemctl is-active --quiet ssh; then break; fi
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
run() { "$work/shellf" run --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$@" "$here/plans/plan.shellf"; }

say "1. check mode is inert (previews 'would', touches nothing)"
out="$(run --dry-run 2>&1)"; printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'would' || fail "check mode should preview a 'would' outcome"
docker exec "$cname" test -e /tmp/shellf-e2e && fail "check mode created state on the target"

say "1b. status on a fresh host shows drift (current → desired)"
out="$("$work/shellf" status --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$here/plans/plan.shellf" 2>&1)"
printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'present: false → true' || fail "status should show present drift on a fresh host"
# #334: `status` runs each def's observe, and an observe may call a control-host
# primitive — so status needs the channel too. Without it every template reports
# err.agent while the greps above still pass, which is how this was missed.
printf '%s' "$out" | grep -q 'err.agent' && fail "status could not reach the control host (a def's observe failed)"
# #338: status reports, it does not act (ADR-0013). It used to: the engine handled check
# mode and fell through to apply, so every remaining Go instruction ran for real — this
# very step printed `file.put(...) ok.written` and `shell(echo … > …) ok.ran` on a fresh
# host. The assertion is on the target, not on the report: a report cannot prove nothing
# was written.
docker exec "$cname" test -e /tmp/shellf-e2e && fail "status created state on the target"
printf '%s' "$out" | grep -qE 'ok\.(written|ran|created)' && fail "status reported an action it should only have previewed"

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
# dir.copy delivered the control-host tree, text + binary, byte-for-byte. Since #335 it is
# a def over `~dir.sync` (ADR-0039), so this also exercises the streaming transfer, the
# manifest and the staged writes against a real target.
docker exec "$cname" grep -q 'delivered by dir-copy' /tmp/shellf-e2e/delivered/hello.txt || fail "dir-copy did not deliver the text file"
want_bin="$(sha256sum "$here/assets/tree/assets/logo.bin" | cut -d' ' -f1)"
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

# #335 / ADR-0039 §1: a converged tree transfers **zero bytes**, not merely writes nothing.
# `dir.copy` reports `already` only if `~dir.sync` wrote no file — the property the old
# control-side expansion never had, since it inlined the whole tree on every run.
printf '%s' "$out" | grep -qE 'dir\.copy.*ok\.already' || fail "a converged tree must report already, not a fresh copy"

say "3b. --dry-run on a converged host announces nothing (#339)"
# The reason the delegation form exists (ADR-0037 §2). `file.template` is now `file.write`
# with rebound arguments, so the callee's `observe` runs in check mode and answers. Placed
# in an `apply`, that call would be skipped in `--dry-run` and the preview would announce
# a write that would not happen — which is exactly what step 1 asserts on a *fresh* host,
# and what nothing asserted on a converged one.
out="$(run --dry-run 2>&1)"; printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'file.template.*ok.already' || fail "a converged template must preview as already, not would"
if printf '%s' "$out" | grep -qE 'file\.template.*would'; then
  fail "a converged template must not preview a write"
fi

say "4. status reports the converged state (no drift arrows)"
out="$("$work/shellf" status --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$here/plans/plan.shellf" 2>&1)"
printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'present: true' || fail "status should report present: true"
printf '%s' "$out" | grep -q 'err.agent' && fail "status could not reach the control host (a def's observe failed)"
# a template's own state must be reported, not just the instructions around it (#334)
printf '%s' "$out" | grep -q 'synced: true' || fail "status should report a template as synced"
if printf '%s' "$out" | grep -q '→'; then
  fail "status after apply should show no drift arrows (all converged)"
fi

say "5. the allow-list holds over a real bridge (#329)"
# The unit tests prove the control host refuses an undeclared resource; they cannot prove
# the refusal survives the transport. This runs a plan whose def asks for a path built at
# runtime — the allow-list is syntactic, so the literal `${n}.tmpl` is what gets declared
# while the ask, once the def body interpolates, is for `motd.tmpl`. Nothing else can
# produce an undeclared ask: every literal in every shipped def is scanned.
#
# Generated under $work, not kept in test/e2e/: a package directory may hold only defs
# beside its plan, so a second plan file there breaks loading for the first one.
mkdir -p "$work/refused/plans" "$work/refused/defs/s"
cat > "$work/refused/defs/s/sneak.shellf" <<'EOF'
def sneak(n: str, dst: str) {
    apply {
        ~file.write(dst, ~file.read(%"${n}.tmpl"))
        return ok.written
    }
}
EOF
cat > "$work/refused/plans/plan.shellf" <<'EOF'
on target {
    s.sneak("motd", "/tmp/shellf-e2e/sneaked")
}
EOF
# The run exits non-zero by design (a refusal is an error), so the exit code is captured
# rather than allowed to kill the harness.
out="$("$work/shellf" run --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$work/refused/plans/plan.shellf" 2>&1)" || true
printf '%s\n' "$out"
printf '%s' "$out" | grep -q 'was not declared by the plan' || fail "an undeclared resource must be refused over the bridge"
# The message names the resource: a refusal the operator cannot read is a support ticket.
printf '%s' "$out" | grep -q 'file.read:motd.tmpl' || fail "the refusal must name the resource it refused"
# And the refusal must be a refusal, not a warning: nothing lands on the target.
docker exec "$cname" test -e /tmp/shellf-e2e/sneaked && fail "a refused read still wrote its destination"

say "6. a dropped bridge is relaunched mid-run (#347, #329 case 3)"
# The property that justifies a socket in the agent's workdir rather than a pipe
# (ADR-0031 §2): the agent stays detached and keeps listening, so a dropped session costs
# a reconnection, not the job. Until #347 the control host dialled once and gave up.
#
# The plan sleeps first, so the bridge can be killed on the target *before* the step that
# needs the control host. A run that survives that is the whole claim.
mkdir -p "$work/drop/plans" "$work/drop/assets"
printf 'late render, after the bridge died\n' > "$work/drop/assets/late.tmpl"
cat > "$work/drop/plans/plan.shellf" <<'EOF'
on target {
    shell { sleep 6 }
    file.template(%"late.tmpl", "/tmp/shellf-e2e/late")
}
EOF
"$work/shellf" run --inventory "$work/inventory.shellf" --insecure --secret-file apisecret="$work/secret" "$work/drop/plans/plan.shellf" > "$work/drop.out" 2>&1 &
runpid=$!
# Wait for the bridge to exist, then kill it — during the sleep, before the template.
for _ in $(seq 1 40); do
  if docker exec "$cname" pgrep -f '__bridge' >/dev/null 2>&1; then break; fi
  sleep 0.25
done
docker exec "$cname" pgrep -f '__bridge' >/dev/null 2>&1 || fail "no bridge was ever opened, so this step proves nothing"
docker exec "$cname" pkill -f '__bridge' || fail "could not kill the bridge"
# `wait` must not kill the harness under `set -e` before its status is read — the point
# of this step is to report the failure, not to die of it.
rc=0; wait "$runpid" || rc=$?
cat "$work/drop.out"
[ "$rc" -eq 0 ] || fail "the run did not survive a dropped bridge (exit $rc)"
# The detached agent must be untouched: a dropped session kills the bridge, not the job.
docker exec "$cname" pgrep -f '__agent-resident' >/dev/null 2>&1 || fail "the agent died with its bridge"
# And the step that needed the control host after the drop actually got its answer.
docker exec "$cname" grep -q 'late render' /tmp/shellf-e2e/late || fail "the post-drop render never reached the target"

say "7. every stdlib def runs against the target, and re-runs converged (#367)"
# The plan that makes "every def is tested" a fact. `def-coverage.sh` guarantees the list
# is complete; this step guarantees the calls actually work — a plan that names every def
# and never runs would satisfy the ratchet and prove nothing.
mkdir -p "$work/cov/inner"
printf 'one\n' > "$work/cov/inner/one.txt"
printf 'two\n' > "$work/cov/inner/two.txt"
tar czf "$here/assets/cov/sample.tar.gz" -C "$work/cov" inner

cov() { "$work/shellf" run --inventory "$work/inventory.shellf" --insecure "$@" "$here/plans/coverage.shellf"; }
rc=0; out="$(cov 2>&1)" || rc=$?; printf '%s\n' "$out"
printf '%s' "$out" | grep -qE 'err\.' && fail "a def failed against the target"

# Idempotence, def by def: on a second run every def that *observes* state must report a
# converged outcome. This is where a def that only looks idempotent gets caught — it is
# how `dir.owner` was found reporting `changed` forever, its observe comparing
# `user:group` against an argument that named a user (#367).
#
# Action-shaped defs are excluded, and the set is derived rather than listed: a def with
# no `observe` phase always acts by design (ADR-0029) — `service.restart` restarting is
# the point, not a defect. Deriving it means the exclusion cannot rot as the stdlib moves.
rc=0; out="$(cov 2>&1)" || rc=$?; printf '%s\n' "$out"
[ "$rc" -eq 0 ] || fail "the coverage plan failed on its second run (exit $rc)"

observed="$(
  for f in "$root"/internal/std/*/*.shellf; do
    pkg="$(basename "$(dirname "$f")")"
    awk -v pkg="$pkg" '
      /^(override )?def [a-z]/ { if (name && seen) print pkg "." name; name=$0; sub(/^(override )?def /,"",name); sub(/\(.*/,"",name); seen=0 }
      /observe \{/ { seen=1 }
      END { if (name && seen) print pkg "." name }
    ' "$f"
  done | sort -u
)"

acting="$(printf '%s' "$out" | grep -oE '^\s+[a-z][a-z0-9.-]*\(.*\) ok\.[a-z]+$' \
  | sed -E 's/^\s+([a-z][a-z0-9.-]*)\(.*\) ok\.([a-z]+)$/\1 \2/' \
  | grep -vE ' (already|present|ready|match|converged)$' | awk '{print $1}' | sort -u)"

for d in $acting; do
  if printf '%s\n' "$observed" | grep -qx "$d"; then
    fail "$d observes state yet acted on a converged target — its observe never matches"
  fi
done

# `dir.sync` removed what the source does not have, and `dir.copy` did not — one word
# apart, so the difference is asserted rather than assumed (#373).
docker exec "$cname" test -e /tmp/cov/mirror/stale.txt && fail "dir.sync did not remove the extra file"
docker exec "$cname" test -f /tmp/cov/mirror/hello.txt || fail "dir.sync did not deliver the tree"

say "8. the shipped examples run for real (#356)"
# They are what a new user copies, and neither had ever been executed — which is how
# `blog.shellf` shipped enabling a deny-inbound firewall without an SSH rule, and calling
# `ufw.open(port, …)` in the bare form the language forbids. Parsing proved nothing.
#
# Two targets, not one: the examples' inventory declares `web` and `app` as separate
# machines, and they are. Sharing a container makes `blog` fail to bind :80 because
# `webserver` installed nginx there — an artefact of the test setup, not of the examples,
# and papering over it in the plan would teach a port nobody chose.
appname="$cname-app"
docker run -d --name "$appname" \
  --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock \
  "$image_tag" >/dev/null
trap 'docker rm -f "$cname" "$appname" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
for _ in $(seq 1 60); do
  st="$(docker exec "$appname" systemctl is-system-running 2>/dev/null || true)"
  case "$st" in running|degraded) break ;; esac
  sleep 0.5
done
docker cp "$work/id.pub" "$appname:/home/deploy/.ssh/authorized_keys"
docker exec "$appname" sh -c '
  chown -R deploy:deploy /home/deploy/.ssh
  chmod 600 /home/deploy/.ssh/authorized_keys
  systemctl restart ssh
'
appip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$appname")"
[ -n "$appip" ] || fail "could not read the app container IP"

cat > "$work/examples-inventory.shellf" <<EOF
host web = {
    address: "$ip", user: "deploy", key: "$work/id",
    pkg: "nginx-light", webroot: "/opt/dogfood"
}
host app = {
    address: "$appip", user: "deploy", key: "$work/id",
    domain: "blog.example.test"
}
EOF
printf 'examples-db-password' > "$work/dbpw"

example() {
  "$work/shellf" run --inventory "$work/examples-inventory.shellf" --insecure \
    --secret-file db_password="$work/dbpw" "$@"
}

for plan in "$root"/examples/plans/*.shellf; do
  name="$(basename "$plan")"
  rc=0; out="$(example "$plan" 2>&1)" || rc=$?
  printf '%s\n' "$out"
  # The exit code is the signal, not the presence of `err.` in the report: an example
  # demonstrating `?` shows a caught error on purpose, and since #356 a caught error no
  # longer fails the run. Grepping for `err.` would refuse the very feature being taught.
  [ "$rc" -eq 0 ] || fail "example $name failed (exit $rc)"

  # And it converges: an example that cannot be run twice teaches a plan nobody can re-run.
  rc=0; out="$(example "$plan" 2>&1)" || rc=$?
  printf '%s\n' "$out"
  [ "$rc" -eq 0 ] || fail "example $name failed on its second run (exit $rc)"
done

# Artefacts asserted on the target, not in the report.
docker exec "$cname" test -f /opt/dogfood/index.html || fail "the webserver example delivered nothing"
docker exec "$appname" grep -q 'blog.example.test' /opt/blog/.env || fail "the blog .env was not rendered per host"
docker exec "$appname" sh -c 'docker compose -f /opt/blog/docker-compose.yml ps --status running | grep -q blog' \
  || fail "the blog stack is not running"

say "9. a remote module is fetched, pinned and used (#357)"
# `import <alias> "<url>@<version>"` (ADR-0016) was covered by a unit test and had never
# run against a target. The module is a bare git repo the harness builds here: no network,
# nothing published, and the mechanism — clone, version pin, shellf.lock, qualified call —
# is exercised for real.
#
# The *example* of a remote import is a different thing and stays absent on purpose: it
# would need a published module, which is a compatibility promise the language cannot make
# while its grammar is still moving.
# A module is a **flat def package** — `.shellf` files at its root, called `alias.def`.
# Not a project layout: ADR-0038 changed how a *project* is arranged, not a module.
mkdir -p "$work/module" "$work/modproj/plans" "$work/modproj/inventories"
cat > "$work/module/greet.shellf" <<'EOF'
def hello(path: str) {
    observe { return state(present: shell { test -f "$path" }.exit == 0) }
    apply {
        r = shell { printf 'from a remote module\n' > "$path" }
        if !r { return err.runtime(r) }
        return ok.written
    }
}
EOF
( cd "$work/module" && git init -q -b main .   && git config user.email e@x && git config user.name e   && git add -A && git commit -qm module && git tag v1.0.0 ) >/dev/null 2>&1
git clone -q --bare "$work/module" "$work/module.git" >/dev/null 2>&1

cat > "$work/modproj/plans/plan.shellf" <<EOF
import m "file://$work/module.git@v1.0.0"

on target {
    m.hello("/tmp/shellf-e2e/from-module")
}
EOF
cp "$work/inventory.shellf" "$work/modproj/inventories/inv.shellf"
mkdir -p "$work/modproj/defs" "$work/modproj/assets"

rc=0; out="$("$work/shellf" run --inventory "$work/modproj/inventories/inv.shellf" --insecure \
  "$work/modproj/plans/plan.shellf" 2>&1)" || rc=$?
printf '%s\n' "$out"
[ "$rc" -eq 0 ] || fail "a remote module must resolve and run (exit $rc)"
docker exec "$cname" grep -q 'from a remote module' /tmp/shellf-e2e/from-module \
  || fail "the remote module's def did not run on the target"
# The version is pinned in a lock at the project root, so a second run cannot drift.
[ -f "$work/modproj/shellf.lock" ] || fail "shellf.lock was not written at the project root"
grep -q 'v1.0.0' "$work/modproj/shellf.lock" || fail "the lock does not pin the imported version"

say "10. a changed source is re-delivered, an unchanged one is not (#378)"
# Regression test for #378: `file.copy` observed *existence*, which is true forever after
# the first run, so a source edited between two runs was never delivered — and the run
# said `ok.already` over the stale file. Convergence and correctness are not the same
# property; the coverage step checks the first, this one checks the second.
#
# It needs a source that changes between two runs, which no other step has: coverage.shellf
# delivers fixed assets, so it converges whether or not this bug is present.
mkdir -p "$work/drift/plans" "$work/drift/inventories" "$work/drift/assets"
cp "$work/inventory.shellf" "$work/drift/inventories/inv.shellf"
cat > "$work/drift/plans/plan.shellf" <<'EOF'
on target {
    dir.ensure("/tmp/shellf-drift")
    file.copy(%"conf.txt", "/tmp/shellf-drift/conf.txt")
}
EOF
drift() { "$work/shellf" run --inventory "$work/drift/inventories/inv.shellf" --insecure \
  "$work/drift/plans/plan.shellf" 2>&1; }

printf 'v1\n' > "$work/drift/assets/conf.txt"
out="$(drift)" || fail "the first delivery failed"
docker exec "$cname" grep -qx 'v1' /tmp/shellf-drift/conf.txt || fail "the first delivery did not land"

printf 'v2\n' > "$work/drift/assets/conf.txt"
out="$(drift)" || fail "the second delivery failed"; printf '%s\n' "$out"
docker exec "$cname" grep -qx 'v2' /tmp/shellf-drift/conf.txt \
  || fail "a changed source was not re-delivered — the destination is stale (#378)"
printf '%s' "$out" | grep -qE 'file\.copy.*ok\.copied' \
  || fail "a changed source must report copied, not already (#378)"

# The other half: fixing the staleness must not cost idempotence.
out="$(drift)" || fail "the third delivery failed"
printf '%s' "$out" | grep -qE 'file\.copy.*ok\.already' \
  || fail "an unchanged source must report already"

say "PASS — check inert, apply provisioned, re-apply idempotent, status converged, allow-list held, bridge relaunched, every def exercised, examples run, remote module used, changed source re-delivered"
