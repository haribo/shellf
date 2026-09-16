# ADR 0058 — A shell that outruns its bound is `err.timeout`

## Status

Active.

## Context

Nothing bounds how long a command may run. `internal/engine/exec_shell.go:74-93` is
`exec.Command` followed by `cmd.Run()`, and `context.Context` appears **zero times** in the
repository. A shell that never returns holds the job, and the resident agent holds it for its
whole TTL — two hours by default (ADR-0005).

`http.wait-for(url, timeout)` exists to bound a wait and does not:

```
end=$(( $(date +%s) + timeout ))
while [ "$(date +%s)" -lt "$end" ]; do
    curl -sfo /dev/null "$url" && exit 0
    sleep 1
done
```

The loop condition is re-read only **between** two curls, and the `curl` carries no
`--max-time`. One call against a host that accepts the connection and never answers runs past
the deadline without limit. The argument is a floor, never a ceiling. `http.check`
(`internal/std/http/http.shellf:6`) and `file.download` (`internal/std/file/file.shellf:149`)
have the same unbounded `curl` with no loop to even pretend (#657).

### What is already not the hazard

Measured before designing, because it changes the shape of the problem: **a command waiting for
an answer does not hang.** Every shell the agent runs gets `/dev/null` on stdin — `exec_shell.go`
never sets `cmd.Stdin`, and Go's `os/exec` gives a nil `Stdin` the null device. On a Debian
target, stdin at `/dev/null`:

| what | outcome |
|---|---|
| a debconf question (`apt-get install -y postfix`) | installs, `exit=0`, defaults taken |
| a dpkg conffile prompt | `end of file on stdin at conffile prompt`, `exit=100` |

So the class to bound is narrower than "anything that does not return": it is a command that
**works without end**, or one whose network peer is silent. Not one that is waiting to be told
something.

### What a bound may not do

A slow transfer is not a stuck transfer. A 2 GB download over a bad line must live; a `curl`
against a machine that accepted the connection and went quiet must die. A **total-duration** cap
cannot tell them apart and kills the first — which is why `curl --max-time` is the wrong tool
here and `--speed-limit`/`--speed-time` is the right one.

Ansible is instructive on the default. Its global `task_timeout` is **0 — no limit**; the bounds
that exist are per-module and at the socket level (`get_url`, `uri`), not on total duration; and
long work is declared per task with `async`. The ecosystem's answer is explicit and local, not a
global guess.

## Decision

### 1. A shell that outruns its bound reports `err.timeout`

An ordinary verdict in the `err` category, tag `timeout`, catchable like any other (ADR-0009):

```
x = http.wait-for("https://app", "30")?
if x == err.timeout { service.restart("app") }
```

Rejected: halting the host outright. A command that ran out of time is a statement about the
world — a service still starting, a mirror gone quiet — and the plan is the only thing that knows
whether that is fatal. Halting is what happens anyway when the plan does not catch it, so making
it expressible costs nothing.

### 2. Two mechanisms, because there are two questions

- **Stalled**: no progress for N seconds. Only decidable where progress has a signal. For a
  transfer that signal is bytes, and `curl --speed-limit 1 --speed-time N` is exactly it.
- **Too long**: a wall-clock cap. The only tool available for an arbitrary `shell { }`, because
  the engine sees no progress there — **output is not progress**. `apt-get install`, `tar xzf`
  over a large archive, a compilation and a legitimate `sleep` are all silent and healthy, so
  killing on silence would break correct plans and be worse than the defect.

Collapsing the two was the first design of this ADR and it was wrong: it would have killed a slow
download.

### 3. The duration cap defaults to no limit

Like Ansible's `task_timeout`. shellf cannot know what a legitimate command takes: a cap low
enough to be useful on a hung `curl` is low enough to kill a package install on a slow link, and
a CI that fails intermittently is worse than no bound at all.

This is a deliberate reduction of the promise. shellf does not claim "a run cannot hang"; it
claims "you can say when it must not". The thing that removes the **measured** defect is §5, not
this.

### 4. The cap is policy, and lives where policy lives

`shell-timeout` in `shellf.conf`, with the matching flag, under ADR-0057's layering: plan →
flag → file → default. Per **shell**, not per step: a def's `apply` may run several, and each is
bounded on its own.

Enforced under the executor (`exec.CommandContext`), with the value carried in `proto.Request`
and applied to the executor the agent builds for the job. `As` and `Using` carry it into the
executors they derive, or an escalated shell is unbounded — which is the one that most wants
bounding.

Rejected: threading a `context.Context` through the evaluator. It is the natural Go answer and
far larger than the problem — the evaluator is a tree walk with no I/O of its own, so the context
would be carried through every frame to be used in one place. It becomes the right shape the day
cancellation from the control host is built, which is a different issue.

### 5. A def's own timeout argument is a separate contract

`http.wait-for(url, "30")` promises thirty seconds to its caller. That is the def's word, not the
engine's backstop, and it is what #657 actually measured. So the stdlib's `curl` calls gain
`--max-time` sized from the **remaining** budget inside the loop, plus `--connect-timeout`; and
`file.download`, where a slow-but-progressing transfer is the normal case, gains
`--speed-limit`/`--speed-time` rather than a total cap.

Two mechanisms again, deliberately: one is what an instruction promises, the other is what
nothing may exceed. Merging them would mean either every def declares a timeout, or no def can
promise one.

### 6. The verdict is the engine's, not the def's

A def turns a failed shell into its own error (`if !r { return err.runtime(r) }`). A shell killed
by the cap does not reach that line: it short-circuits the def with `err.timeout` whatever the def
would have said.

The reason is that the def cannot tell. `ShellResult` carries an exit code, and a killed
command's exit code says nothing reliable — `timeout(1)` uses 124, and a script may exit 124 on
its own. So the executor marks the result explicitly and the evaluator reads the mark. It also
means all 49 stdlib defs become bounded without one of them being edited.

Consequence, written here so it is not discovered: `err.timeout` joins the verdicts a plan may see
from **any** instruction, including one whose def declares no such error.

## Rejected alternatives

- **Exit code 124 as the signal.** `timeout(1)`'s convention, and unusable: the difference between
  "was killed" and "chose to exit 124" is exactly what a verdict must not guess.
- **Killing on output silence, for any shell.** Discussed in §2. A silent command is the norm.
- **A per-def or per-`shell` timeout in the grammar.** A grammar change for a need no measured
  case has expressed yet. `shellf.conf` plus the flag covers what #657 found; if a real plan hits
  the wall, that is the case that justifies syntax — and `with { }` (ADR-0022) should be examined
  before a new keyword is invented.
- **Bounding the primitives.** `~file.read` and the transfers are Go under the agent's control and
  are not where a run hangs. Shells are.
- **An aggressive default.** §3.

## Consequences

- A plan can express "this took too long" and react to it.
- `http.wait-for` honours its argument, which is the defect #657 reported.
- A run can still hang, by default, on a `shell { }` that never returns. That is a choice, it is
  visible here, and `shell-timeout` is how an operator removes it.
- `ShellResult` grows a field and `proto.Request` grows a setting, so the protocol changes shape.
  No compatibility work: the cached agent path carries the digest of the binary
  (`internal/transport/ssh.go:167`), so a control host that knows about the cap never reuses an
  agent that does not.
- `shellf.conf` gains its fourth key, and #646's gate requires it to be documented.
