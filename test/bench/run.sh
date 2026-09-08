#!/usr/bin/env bash
# shellf vs Ansible — same target image, same instructions, same order.
#
# What is measured: the TOOL's overhead. Every instruction in the plan is one whose cost
# is dominated by the tool's own round-trips, not by an external service (no package
# actually downloaded, no git clone, no docker pull) — otherwise the bench would measure
# apt, not shellf.
#
# What is reported: two numbers per N, because they answer different questions.
#   cold  — a fresh host, nothing converged: first provisioning.
#   no-op — everything already in the desired state: the run an operator does daily.
#
# Parity is asserted, not assumed: the two containers' resulting state is diffed.
#
# Opt-in (`SHELLF_BENCH=1`) and never on CI's default path: it takes minutes, and its
# absolute numbers describe the machine that ran it as much as the tools.
set -euo pipefail

: "${SHELLF_BENCH:?set SHELLF_BENCH=1 to run the benchmark (needs Docker and ansible-playbook)}"

# The targets below are `--privileged` containers with systemd as PID 1, exactly like the
# e2e harness — which ended a developer's graphical session four times before it grew the
# guards this file borrows (#528, #529). Read test/e2e/run.sh for the full account. The
# short version: `--cgroupns=private` is the whole safety of the `docker run` line, and the
# image masks `systemd-sysctl`, which does not fail but succeeds on the *host* kernel.

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
NS="${BENCH_N:-10 25 50 100}"
REPS="${BENCH_REPS:-3}"
img="shellf-bench:$(sha256sum "$here/Dockerfile" | cut -c1-12)"
# Everything this run produces goes to out/, which is gitignored: a benchmark that dirties
# the tree is one nobody runs twice. The parity diff has to survive the run, so it cannot
# live in the temp dir that gets torn down.
out="$here/out"
mkdir -p "$out"
csv="$out/results.csv"

cs="bench-shellf-$$"; ca="bench-ansible-$$"
cleanup() { docker rm -f "$cs" "$ca" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

say()  { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
now()  { date +%s%N; }
ms()   { echo $(( ($2 - $1) / 1000000 )); }

command -v ansible-playbook >/dev/null || fail "ansible-playbook not found — the bench needs both tools"

# Recorded before any target exists, compared after they have booted. The guard is the
# specific known-dangerous outcome rather than trust in a flag (#528).
host_core_pattern="$(sysctl -n kernel.core_pattern 2>/dev/null || true)"

say "build shellf"
( cd "$root" && go build -o "$work/shellf" ./cmd/shellf )

if ! docker image inspect "$img" >/dev/null 2>&1; then
  say "build the target image ($img) — once, then reused"
  docker build --platform linux/amd64 -q -t "$img" -f "$here/Dockerfile" "$here" >/dev/null
fi

ssh-keygen -t ed25519 -N '' -f "$work/id" -q

# Boot one throwaway target and hand back its IP.
boot() {
  local name="$1"
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" --platform linux/amd64 --privileged --cgroupns=private \
    --tmpfs /run --tmpfs /run/lock "$img" >/dev/null
  local state=""
  for _ in $(seq 1 60); do
    state="$(docker exec "$name" systemctl is-system-running 2>&1 || true)"
    case "$state" in running|degraded) break ;; esac
    sleep 0.5
  done
  case "$state" in running|degraded) ;; *) fail "$name never booted (last: $state)" ;; esac

  # Structural: under a private cgroup namespace PID 1 sits at its own root. Under the
  # host's, Docker places it under `/system.slice/docker-<id>.scope` — the configuration
  # that reached out and logged the operator out (#528).
  local pid1
  pid1="$(docker exec "$name" sh -c 'grep "^0::" /proc/1/cgroup' 2>/dev/null || true)"
  case "${pid1:-none}" in
    *docker-*|none) fail "$name sits in the host cgroup tree (${pid1:-<no answer>}) — its systemd would manage the host's sessions" ;;
  esac
  # Semantic, and the one that matters: the host's login sessions must be invisible.
  if docker exec "$name" sh -c 'find /sys/fs/cgroup -maxdepth 4 -name "session-*.scope" | head -1' 2>/dev/null | grep -q .; then
    fail "$name can see host login sessions — it would log the operator out"
  fi
  # And it must not have written to the host's kernel on the way up.
  if [ "$(sysctl -n kernel.core_pattern 2>/dev/null || true)" != "$host_core_pattern" ]; then
    fail "$name rewrote the host's kernel.core_pattern — systemd-sysctl is not masked (#528)"
  fi
  docker cp "$work/id.pub" "$name:/home/deploy/.ssh/authorized_keys"
  docker exec "$name" sh -c '
    chown -R deploy:deploy /home/deploy/.ssh
    chmod 600 /home/deploy/.ssh/authorized_keys
    systemctl restart ssh'
  for _ in $(seq 1 30); do
    docker exec "$name" systemctl is-active --quiet ssh && break; sleep 0.5
  done
  docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name"
}

# The state both tools must produce, read from the target itself: paths, modes,
# ownership, content hashes, and the user/group facts. Timestamps are excluded — they
# differ by construction and prove nothing.
snapshot() {
  docker exec "$1" bash -c '
    find /srv/app -printf "%p %m %u:%g\n" | sort
    find /srv/app -type f -print0 | sort -z | xargs -0 sha256sum
    getent passwd benchapp | cut -d: -f1,7
    id -nG benchapp | tr " " "\n" | sort | tr "\n" ","
    echo' 2>&1
}

echo "n,instructions,tool,phase,ms" > "$csv"
record() { echo "$1,$2,$3,$4,$5" >> "$csv"; }

for N in $NS; do
  gen="$work/gen-$N"
  python3 "$here/gen.py" "$N" "$gen" >/dev/null
  cp "$here/assets/app.conf.tmpl" "$gen/assets/"
  cp "$here/assets/app.conf.j2" "$gen/ansible/"
  instr=$(( 4 + N + 7 ))

  say "N=$N ($instr instructions) — booting two identical targets"
  ips="$(boot "$cs")"; ipa="$(boot "$ca")"

  cat > "$gen/inventories/inventory.shellf" <<EOF
host target = { address: "$ips", user: "deploy", key: "$work/id" }
EOF
  cat > "$gen/ansible/hosts.ini" <<EOF
target ansible_host=$ipa ansible_user=deploy ansible_ssh_private_key_file=$work/id
EOF

  shellf_run() { "$work/shellf" run --inventory "$gen/inventories/inventory.shellf" --insecure "$gen/plans/plan.shellf" 2>&1; }
  ansible_run() { ANSIBLE_CONFIG="$here/ansible.cfg" ansible-playbook -i "$gen/ansible/hosts.ini" "$gen/ansible/playbook.yml" 2>&1; }

  # --- cold ---------------------------------------------------------------------
  t0=$(now); so="$(shellf_run)" || fail "shellf cold run failed:\n$so"; t1=$(now)
  record "$N" "$instr" shellf cold "$(ms $t0 $t1)"
  t0=$(now); ao="$(ansible_run)" || fail "ansible cold run failed:\n$ao"; t1=$(now)
  record "$N" "$instr" ansible cold "$(ms $t0 $t1)"
  printf 'cold  shellf=%sms ansible=%sms\n' \
    "$(awk -F, -v n=$N '$1==n&&$3=="shellf"&&$4=="cold"{print $5}' "$csv")" \
    "$(awk -F, -v n=$N '$1==n&&$3=="ansible"&&$4=="cold"{print $5}' "$csv")"

  # --- no-op --------------------------------------------------------------------
  # The order alternates between repetitions: running one tool always first would hand it
  # (or cost it) whatever the other leaves warm.
  for r in $(seq 1 "$REPS"); do
    if [ $(( r % 2 )) -eq 1 ]; then order="shellf ansible"; else order="ansible shellf"; fi
    for tool in $order; do
      t0=$(now)
      case "$tool" in
        shellf)  so="$(shellf_run)"  || fail "shellf no-op run failed:\n$so" ;;
        ansible) ao="$(ansible_run)" || fail "ansible no-op run failed:\n$ao" ;;
      esac
      t1=$(now); record "$N" "$instr" "$tool" noop "$(ms $t0 $t1)"
    done
  done

  # --- convergence: a no-op run must change nothing, on both sides ---------------
  if printf '%s' "$so" | grep -qE 'ok\.(created|written|changed|ensured|added|set|converged|installed)'; then
    printf '%s\n' "$so"; fail "shellf's no-op run still acted — not converged"
  fi
  printf '%s' "$so" | grep -q 'already' || { printf '%s\n' "$so"; fail "shellf reported no 'already'"; }
  if ! printf '%s' "$ao" | grep -qE 'changed=0'; then
    printf '%s\n' "$ao"; fail "ansible's no-op run still reported changes — not converged"
  fi

  ok_count="$(printf '%s' "$ao" | grep -oE 'ok=[0-9]+' | tail -1 | cut -d= -f2)"
  [ "${ok_count:-0}" -eq "$instr" ] || { printf '%s\n' "$ao"; fail "ansible ran $ok_count tasks, expected $instr"; }

  # --- parity: both targets must hold the same state -----------------------------
  snapshot "$cs" > "$work/state-shellf-$N.txt"
  snapshot "$ca" > "$work/state-ansible-$N.txt"
  if ! diff -u "$work/state-shellf-$N.txt" "$work/state-ansible-$N.txt" > "$work/diff-$N.txt"; then
    say "PARITY DIFF (N=$N) — the two tools did not produce the same state"
    cat "$work/diff-$N.txt"
    cp "$work/diff-$N.txt" "$out/parity-diff-$N.txt"
    fail "parity broken at N=$N — see $out/parity-diff-$N.txt"
  fi
  echo "parity ok (identical state on both targets)"

  docker rm -f "$cs" "$ca" >/dev/null 2>&1 || true
done

say "results"
cp "$work"/state-*.txt "$out/" 2>/dev/null || true
python3 "$here/report.py" "$csv"
