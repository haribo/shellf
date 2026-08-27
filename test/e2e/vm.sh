#!/usr/bin/env bash
# Run the e2e harness inside a throwaway VM (#529).
#
# `test/e2e/run.sh` starts a `--privileged` container with systemd as PID 1. That container
# shares the kernel of whatever machine it runs on, and it has ended a developer's graphical
# session four times: twice before this script existed (see run.sh's `--cgroupns=private`
# comment) and twice on 2026-08-27, the second while verifying a fix for the first. Three
# distinct leaks are known — the cgroup tree, `vm.swappiness`, `kernel.core_pattern` (#528)
# — and the fourth incident is still unexplained.
#
# So the harness does not change; where it runs does. Inside this VM the same container
# shares the VM's kernel, and the VM is disposable. CI is untouched: a GitHub runner is
# already throwaway, which is why `ci.yaml` keeps calling run.sh directly.
#
# Deliberately qemu + cloud-init rather than libvirt or vagrant: no daemon, no root, no
# system-wide network config, and the whole thing is three files in the repo.
#
#   test/e2e/vm.sh up       boot it (idempotent)
#   test/e2e/vm.sh run      boot if needed, sync the repo, run the harness inside
#   test/e2e/vm.sh ssh      a shell in the VM
#   test/e2e/vm.sh down     stop it
#   test/e2e/vm.sh destroy  stop it and delete its disk
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
cache="${XDG_CACHE_HOME:-$HOME/.cache}/shellf-vm"
disk="$cache/disk.qcow2"
seed="$cache/seed.iso"
key="$cache/id_vm"
pidfile="$cache/qemu.pid"
port=2222

# Debian 13 (trixie) generic cloud image: the same family as the e2e target, so the
# container it runs behaves as it does on a runner. Pinned by name, not by checksum: this is
# a disposable test VM, and a stale pin is a build that breaks for nobody's benefit. The
# image is verified by nothing here on purpose — it never touches anything but itself.
img_url="https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2"
base="$cache/base.qcow2"

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

ssh_vm() {
  ssh -q -i "$key" -p "$port" \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o LogLevel=ERROR -o ConnectTimeout=5 \
      debian@127.0.0.1 "$@"
}

running() { [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; }

build_seed() {
  # cloud-init: a user with our key, docker from Debian's own repo, and nothing else. The
  # harness brings its own image and binary.
  local d; d="$(mktemp -d)"
  cat > "$d/meta-data" <<EOF
instance-id: shellf-e2e
local-hostname: shellf-e2e-vm
EOF
  cat > "$d/user-data" <<EOF
#cloud-config
users:
  - name: debian
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat "$key.pub")
package_update: true
packages:
  - docker.io
  - rsync
runcmd:
  - [ systemctl, enable, --now, docker ]
  - [ usermod, -aG, docker, debian ]
  - [ touch, /var/lib/cloud/shellf-ready ]
EOF
  # `-V cidata` is not decoration: cloud-init finds its datasource by that volume label.
  xorriso -as mkisofs -quiet -output "$seed" -volid cidata -joliet -rock \
          "$d/user-data" "$d/meta-data" 2>/dev/null
  # No `trap … RETURN`: it stays armed for every later function return, and fires with `$d`
  # out of scope under `set -u`.
  rm -rf "$d"
}

cmd_up() {
  if running; then echo "already up (pid $(cat "$pidfile"))"; return 0; fi
  mkdir -p "$cache"

  [ -f "$key" ] || { say "generate an ephemeral key"; ssh-keygen -t ed25519 -N '' -f "$key" -q; }

  if [ ! -f "$base" ]; then
    say "download the Debian cloud image (once)"
    curl -fSL --progress-bar -o "$base.part" "$img_url" || fail "could not fetch the cloud image"
    mv "$base.part" "$base"
  fi

  # A fresh overlay every boot: the VM is disposable, and a harness that inherits the
  # previous run's state is a harness that proves less than it claims.
  say "create a disposable overlay disk"
  rm -f "$disk"
  qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$disk" 20G

  [ -f "$seed" ] || { say "build the cloud-init seed"; build_seed; }

  say "boot the VM"
  # user-mode networking with one forwarded port: no bridge, no root, no host firewall
  # change. The VM reaches the internet for apt and docker pulls; nothing reaches it.
  qemu-system-x86_64 \
    -machine q35,accel=kvm -cpu host -smp 4 -m 6144 \
    -drive if=virtio,format=qcow2,file="$disk" \
    -drive if=virtio,format=raw,file="$seed",readonly=on \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:$port-:22 \
    -device virtio-net-pci,netdev=n0 \
    -display none -daemonize -pidfile "$pidfile" \
    || fail "qemu failed to start"

  say "wait for cloud-init to finish (docker, user, key)"
  for _ in $(seq 1 120); do
    if ssh_vm 'test -f /var/lib/cloud/shellf-ready' 2>/dev/null; then
      echo "ready"
      ssh_vm 'docker --version; systemctl is-active --quiet docker && echo "docker: active"'
      return 0
    fi
    sleep 2
  done
  fail "the VM never became ready — try: $0 ssh"
}

cmd_run() {
  cmd_up
  say "sync the repository into the VM"
  # Built on the host and shipped as a binary: run.sh compiles with `go build -o "$work/shellf"`,
  # and a Go build over a shared filesystem is slow enough to matter. The VM needs no toolchain.
  ssh_vm 'mkdir -p ~/shellf'
  rsync -az --delete \
    --exclude '.git' --exclude 'tmp' \
    -e "ssh -q -i $key -p $port -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR" \
    "$root/" debian@127.0.0.1:shellf/ || fail "rsync failed"

  say "run the harness inside the VM"
  # A TTY only when there is one to give. `ssh -t` without a terminal on this side does not
  # merely lose colour: the remote command is signalled and dies mid-way — measured, as a
  # `docker build` cancelled with `context canceled` while the VM was perfectly healthy.
  # Which matters because a run in the background, or from CI, has no terminal at all.
  local tty_flag=()
  if [ -t 0 ]; then tty_flag=(-t); fi   # `&&` here would exit the script under `set -e`
  ssh ${tty_flag[@]+"${tty_flag[@]}"} -i "$key" -p "$port" \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
      debian@127.0.0.1 \
      'cd ~/shellf && command -v go >/dev/null || { curl -fsSL https://go.dev/dl/$(curl -fsSL "https://go.dev/VERSION?m=text" | head -1).linux-amd64.tar.gz | sudo tar -C /usr/local -xz; export PATH=$PATH:/usr/local/go/bin; }; export PATH=$PATH:/usr/local/go/bin; SHELLF_E2E=1 bash test/e2e/run.sh'
}

cmd_ssh()     { cmd_up; ssh -t -i "$key" -p "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR debian@127.0.0.1; }
cmd_down()    { running || { echo "not running"; return 0; }; kill "$(cat "$pidfile")"; rm -f "$pidfile"; echo "stopped"; }
cmd_destroy() { cmd_down || true; rm -f "$disk"; echo "disk removed (base image and key kept)"; }

case "${1:-run}" in
  up) cmd_up ;;
  run) cmd_run ;;
  ssh) cmd_ssh ;;
  down) cmd_down ;;
  destroy) cmd_destroy ;;
  *) fail "usage: $0 {up|run|ssh|down|destroy}" ;;
esac
