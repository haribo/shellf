# The cross-architecture target: the smallest machine that can answer "which binary did
# shellf push, and does it run?".
#
# Deliberately *not* test/e2e/Dockerfile. That image carries systemd as PID 1, a docker
# daemon and ufw, because the stdlib coverage plan needs them. Under QEMU emulation all of
# it is slow and brittle, and none of it is needed here: this leg asserts which
# architecture got pushed, not what the stdlib can do once it is there.
FROM debian:stable-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends \
      openssh-server sudo coreutils ca-certificates \
 && rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash deploy \
 && mkdir -p /home/deploy/.ssh /run/sshd \
 && chmod 700 /home/deploy/.ssh \
 && echo 'deploy ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/deploy \
 && chmod 440 /etc/sudoers.d/deploy

# sshd in the foreground: no init here, so nothing else has to come up before the test can
# connect.
CMD ["/usr/sbin/sshd", "-D", "-e"]
