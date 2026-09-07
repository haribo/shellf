# shellf vs Ansible — a benchmark you can re-run

`fast` is the fourth word of shellf's subtitle, and nothing else in this repository
measures it. This harness does, reproducibly. It publishes no number: what it produces is
a measurement anyone can take again on their own machine, and argue with.

```sh
SHELLF_BENCH=1 bash test/bench/run.sh
```

Needs Docker and `ansible-playbook`. It takes minutes, so it is opt-in and never on CI's
default path — and its absolute numbers describe the machine that ran it as much as they
describe the tools.

## What it does

For each size N (10, 25, 50, 100 by default, `BENCH_N` to change):

1. `gen.py` emits **both** the shellf plan and the Ansible playbook from one description —
   one instruction facing one task, same order, same arguments. `example/` holds a small
   generated pair, committed so the comparison can be read without running anything.
2. Two identical containers boot from one image, one per tool.
3. **cold** — a fresh host, nothing converged. Timed once.
4. **no-op** — everything already in the desired state, the run an operator repeats daily.
   Timed `BENCH_REPS` times (3 by default), **alternating which tool goes first** so
   neither inherits the other's warm caches.
5. Convergence is checked rather than assumed: shellf must report no `created` / `written`
   / `changed` and at least one `already`; Ansible must report `changed=0`, and its task
   count must equal the instruction count — that assertion caught an off-by-one.
6. **Parity is asserted**: both targets are snapshotted (paths, modes, owners, sha256 of
   every file, the managed user's shell and group list) and diffed. A mismatch fails the
   run and writes the diff to `out/`; no timing is reported over a state that differs.

Results land in `out/` (gitignored).

## What is measured, and what is not

Only instructions whose cost is the **tool's own round-trips**. Nothing is downloaded: the
package `apt.install` names is already present, the service `service.ensure` names is
already running. A benchmark including a `git clone` would be measuring the network.

Ansible runs in its recommended fast configuration — pipelining, `ControlPersist`,
`gathering = explicit` (`ansible.cfg`). Winning against a hobbled adversary proves nothing.
shellf's resident agent survives between runs the way `ControlPersist` does, so neither
side is uniquely warm.

## What these numbers do NOT say

Read this section before quoting anything from this harness.

- **Nothing about many hosts.** One target, both tools. Ansible parallelises across forks;
  a single-target measurement says nothing about ten machines, and this harness makes no
  claim there.
- **Nothing about a real network.** Containers on a local bridge have ~0 RTT. A real link
  would plausibly widen the gap in shellf's favour, since Ansible makes more round trips —
  plausibly, which is to say unmeasured, which is to say unclaimed.
- **Nothing about features.** Ansible covers a vastly larger surface than shellf's stdlib.
  This measures the overhead of the instructions both express, not what either can do.
- **Nothing portable between machines.** Absolute milliseconds are a property of the CPU,
  the disk and the container runtime. What travels is the *slope* — the cost of one more
  instruction — not the intercept.
- **Nothing precise about shellf's own per-instruction cost.** In the homogeneous run its
  time does not move with the instruction count: the values scatter by more than a factor
  of two with no relation to N, because the run is dominated by a noisy fixed cost (the SSH
  setup). The honest statement is "below this harness's noise floor", not a millisecond
  figure. Ansible's slope, being two orders of magnitude larger, *is* measurable here.
- **A single number is the wrong quote.** The ratio depends entirely on the plan size
  picked; the interesting figure is per-instruction cost, which is why several sizes run.

## Files

| file | role |
|---|---|
| `run.sh` | the harness: build, boot, cold, no-op, convergence, parity |
| `gen.py` | one description → the shellf plan and the Ansible playbook |
| `marginal.sh` | the homogeneous run: one repeated instruction, for the per-instruction cost |
| `report.py` | the table and the fitted `fixed + per_instruction × N` |
| `Dockerfile` | the target, pinned by amd64 digest |
| `ansible.cfg` | Ansible's fast configuration |
| `example/` | a small generated pair, committed so the comparison is readable |

## A warning about the target

The containers run `--privileged` with systemd as PID 1, like the e2e harness — which
ended a developer's graphical session four times before it grew the guards this harness
borrows (#528, #529). `--cgroupns=private` is the whole safety of the `docker run` line,
`systemd-sysctl` is masked in the image because it succeeds on the *host* kernel, and
`run.sh` asserts all three outcomes rather than trusting the flags. Do not remove them.
