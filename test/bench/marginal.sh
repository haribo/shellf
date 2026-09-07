#!/usr/bin/env bash
# The main bench fits `fixed + per_instruction x N` over a plan whose 11 non-file
# instructions (apt, service, user, template) cost far more than a file write. That fit
# charges their cost to the "fixed" term, which then means nothing.
#
# This measures the two terms honestly: one instruction kind, homogeneous, repeated —
# `dir.ensure` against `file: state=directory`, already converged. The intercept is then
# the real per-run cost of the tool; the slope, the real cost of one instruction.
#
# The prototype this comes from took a second "fast-poll" binary as an argument, to show
# what shellf would cost with a shorter agent poll cadence. That is no longer a variant:
# the cadence was fixed in the product (#573), so the shipped binary *is* the fast one and
# the parameter is gone.
set -euo pipefail

: "${SHELLF_BENCH:?set SHELLF_BENCH=1 to run the benchmark (needs Docker and ansible-playbook)}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="$here/out"; mkdir -p "$out"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"; cs="marg-shellf-$$"; ca="marg-ansible-$$"
img="shellf-bench:$(sha256sum "$here/Dockerfile" | cut -c1-12)"
trap 'docker rm -f "$cs" "$ca" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
now() { date +%s%N; }
ms()  { echo $(( ($2 - $1) / 1000000 )); }

( cd "$root" && go build -o "$work/shellf" ./cmd/shellf )
ssh-keygen -t ed25519 -N '' -f "$work/id" -q
boot() {
  docker run -d --name "$1" --platform linux/amd64 --privileged --cgroupns=private \
    --tmpfs /run --tmpfs /run/lock "$img" >/dev/null
  for _ in $(seq 1 60); do
    case "$(docker exec "$1" systemctl is-system-running 2>&1 || true)" in running|degraded) break ;; esac
    sleep 0.5
  done
  docker cp "$work/id.pub" "$1:/home/deploy/.ssh/authorized_keys"
  docker exec "$1" sh -c 'chown -R deploy:deploy /home/deploy/.ssh; chmod 600 /home/deploy/.ssh/authorized_keys; systemctl restart ssh'
  sleep 2
  docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"
}
ips="$(boot "$cs")"; ipa="$(boot "$ca")"

mkdir -p "$work/p/plans" "$work/p/inventories" "$work/p/assets" "$work/p/defs" "$work/a"
echo "host target = { address: \"$ips\", user: \"deploy\", key: \"$work/id\" }" > "$work/p/inventories/inv.shellf"
echo "target ansible_host=$ipa ansible_user=deploy ansible_ssh_private_key_file=$work/id" > "$work/a/hosts.ini"

srun() { "$work/shellf" run --inventory "$work/p/inventories/inv.shellf" --insecure "$work/p/plans/plan.shellf" >/dev/null 2>&1; }
med() { printf '%s\n' "$@" | sort -n | awk '{a[NR]=$1} END{print (NR%2)?a[(NR+1)/2]:int((a[NR/2]+a[NR/2+1])/2)}'; }
arun() { ANSIBLE_CONFIG="$here/ansible.cfg" ansible-playbook -i "$work/a/hosts.ini" "$work/a/play.yml" >/dev/null 2>&1; }

echo "k,tool,ms" > "$out/marginal.csv"
for k in 1 2 5 10 25 50 100; do
  { echo "on target {"
    for i in $(seq 1 "$k"); do echo "    dir.ensure(\"/tmp/m/$i\")"; done
    echo "}"; } > "$work/p/plans/plan.shellf"
  { echo "- hosts: target"; echo "  gather_facts: false"; echo "  tasks:"
    for i in $(seq 1 "$k"); do
      echo "  - name: d$i"; echo "    ansible.builtin.file:"; echo "      path: /tmp/m/$i"; echo "      state: directory"
    done; } > "$work/a/play.yml"

  srun; arun    # converge both targets, so every timed run below is a pure no-op
  sv=(); av=()
  # The order alternates, as in run.sh: whichever tool goes first pays for the other's
  # cold caches.
  for r in 1 2 3 4 5; do
    if [ $(( r % 2 )) -eq 1 ]; then
      t0=$(now); srun; t1=$(now); sv+=("$(ms $t0 $t1)")
      t0=$(now); arun; t1=$(now); av+=("$(ms $t0 $t1)")
    else
      t0=$(now); arun; t1=$(now); av+=("$(ms $t0 $t1)")
      t0=$(now); srun; t1=$(now); sv+=("$(ms $t0 $t1)")
    fi
  done
  sb="$(med "${sv[@]}")"; ab="$(med "${av[@]}")"
  echo "$k,shellf,$sb"  >> "$out/marginal.csv"
  echo "$k,ansible,$ab" >> "$out/marginal.csv"
  printf 'k=%-4s shellf=%-8s ansible=%-9s ratio=%.1fx\n' \
    "$k" "${sb}ms" "${ab}ms" "$(echo "$ab/$sb" | bc -l)"
done

# Same state on both targets, or the numbers compare two different things.
d1="$(docker exec "$cs" bash -c 'find /tmp/m -printf "%p %m\n" | sort')"
d2="$(docker exec "$ca" bash -c 'find /tmp/m -printf "%p %m\n" | sort')"
[ "$d1" = "$d2" ] && echo "parity ok" || { echo "PARITY BROKEN"; diff <(echo "$d1") <(echo "$d2"); exit 1; }
