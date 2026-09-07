# Bench target: one image, two identical containers (one driven by shellf, one by
# Ansible). Derived from test/e2e/Dockerfile, minus what the bench does not exercise
# (docker, ufw) and plus python3 — Ansible needs an interpreter on the target, shellf
# does not. That asymmetry is a prerequisite difference, deliberately NOT timed here.
# Pinned to the amd64 manifest by digest, deliberately. A `debian:stable-slim` tag resolves
# to whatever variant the local store happens to hold — on the machine where this was first
# written it was the arm64 one, pulled by the cross-arch e2e test, and building from it runs
# the whole bench under qemu: measuring the emulator, not the tools. A digest also makes the
# target reproducible across runs and across machines, which a benchmark needs more than
# most images do.
FROM debian:stable-slim@sha256:0ef0f77425e6677ead26f893cb61707f7fc44467a480625d8974feb7ab2085fe

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends \
      systemd systemd-sysv \
      openssh-server sudo \
      python3 \
      git curl ca-certificates tar coreutils

RUN useradd -m -s /bin/bash deploy \
 && mkdir -p /home/deploy/.ssh /run/sshd \
 && chmod 700 /home/deploy/.ssh \
 && echo 'deploy ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/deploy \
 && chmod 440 /etc/sudoers.d/deploy

# `systemd-sysctl` is in the mask list for the reason test/e2e/Dockerfile records at
# length: it does not fail, it **succeeds on the wrong kernel**. Keys outside a namespace
# are shared with the host, and this container is `--privileged`, so at boot it would
# apply this image's `sysctl.d` to the machine running the bench. Measured once, on a
# developer's machine: `kernel.core_pattern` rewritten and the graphical session gone one
# second later (#528). The bench sets no sysctl of its own and needs none of this.
RUN systemctl enable ssh \
 && systemctl mask systemd-firstboot systemd-udevd systemd-udev-trigger \
                   systemd-modules-load systemd-network-generator systemd-sysctl

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
