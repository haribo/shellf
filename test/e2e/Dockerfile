# The e2e target: a machine shellf can actually provision.
#
# The old harness built this from debian:stable-slim and apt-installed sshd on *every*
# run — part of its cost — and still could not exercise eleven stdlib defs, because a
# slim container has no init, no docker and no firewall. "The container cannot do it" is
# the problem to solve, not a reason to test less (#366).
#
# Built once and tagged by this file's hash, so the cost is paid when the image changes,
# not when the harness runs.
FROM debian:stable-slim

ENV DEBIAN_FRONTEND=noninteractive

# systemd so `service.*` and `systemd.daemon-reload` are exercised for real rather than
# skipped; ufw for the firewall defs; the rest is what the stdlib shells out to (git,
# curl, tar, sudo, sha256sum via coreutils).
#
# `docker.io` is the daemon only on Debian — the `docker` client lives in `docker-cli`,
# which is why it is named explicitly. Verified rather than assumed: the first build of
# this file installed docker.io alone and left no `/usr/bin/docker` at all.
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends \
      systemd systemd-sysv \
      openssh-server sudo \
      docker.io docker-cli docker-compose \
      ufw iptables \
      git curl ca-certificates tar xz-utils coreutils

# The package lists are kept, deliberately. Removing them is the usual way to shrink an
# image, and it makes the target unrepresentative: a real Debian host has them, so
# `apt.install` works there and failed here with "Unable to locate package". An example
# should not have to carry an `apt.update()` that only this container needs.

# The unprivileged user shellf connects as. Passwordless sudo because the whole point is
# to exercise `as root` escalation (ADR-0011) — this is a throwaway target, not a model
# of a production host.
RUN useradd -m -s /bin/bash deploy \
 && mkdir -p /home/deploy/.ssh /run/sshd \
 && chmod 700 /home/deploy/.ssh \
 && echo 'deploy ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/deploy \
 && chmod 440 /etc/sudoers.d/deploy

# sshd as a unit, so systemd owns it like on a real machine: `service.ensure("ssh", …)`
# then means something. Mask what a container cannot do and that would only fail loudly
# at boot.
#
# `systemd-sysctl` is masked for a different and sharper reason: it does not fail, it
# **succeeds on the wrong kernel**. sysctl keys outside a namespace are shared with the
# host, and this container is `--privileged`, so at boot it applied this image's
# `sysctl.d` files to the machine running the tests. Measured on an Arch/GNOME developer
# machine: `kernel.core_pattern` came out of a harness run set to `core` — Debian's value —
# where Arch configures `|/usr/lib/systemd/systemd-coredump …`, so nothing on that host
# would have produced a usable core dump again until it was restored by hand. The
# developer's graphical session ended one second after the container started (#528).
#
# Nothing here needs it. The `sysctl.set` coverage sets its own keys explicitly, and picks
# a `net.` one precisely because those live in the network namespace — this mask is the
# same hazard from the other end: the image's sysctls rather than the plan's.
RUN systemctl enable ssh docker \
 && systemctl mask systemd-firstboot systemd-udevd systemd-udev-trigger \
                   systemd-modules-load systemd-network-generator systemd-sysctl

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
