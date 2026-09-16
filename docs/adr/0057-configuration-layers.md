# ADR 0057 — Policy lives in a project file, modes stay on the command line

## Status

Active.

## Context

shellf has no configuration file. Every knob is a flag, and `run` now registers thirteen of
them (`cmd/shellf/main.go:155-195`, one definition shared with `status` since #640). Some are
not per-invocation choices at all. `--parallel` and `--agent-ttl` are **policy**: stable for a
project, the same for everyone who runs it, and therefore either retyped on every invocation or
silently left at the default — which is how a fleet ends up running at a width nobody chose.

The question surfaced while designing a bound on how long a step may run (#657). That bound is
policy of the same kind, and answering "where does it live" for one knob without answering it
for the class would decide the general question by accident.

Two bodies of practice were read rather than recalled.

[clig.dev](https://clig.dev/) classifies configuration by **stability**, not by mechanism:
per-invocation → a flag; stable per user or project → flag plus environment; stable within a
project for all users → a version-controlled file specific to the tool. Its precedence, highest
first: flags, environment, project config, user config, system config.

Ansible's [documented precedence](https://docs.ansible.com/projects/ansible/latest/reference_appendices/general_precedence.html)
runs lowest-to-highest *configuration settings → command-line options → playbook keywords →
variables*. Two facts are worth carrying over. `ansible.cfg` is the **lowest** layer, not the
authority. And **the playbook beats the command line**: a task's `async: 3600` overrides any
global setting.

Ansible's [config file](https://docs.ansible.com/ansible/latest/reference_appendices/config.html)
also stops at the first file found, merging nothing, and refuses to load one from a
world-writable directory — the same hazard shellf already handles on the cached agent path
(#391).

## Decision

### 1. Three layers, and the plan is above all of them

Highest to lowest:

1. **The plan.** A declaration in the plan or a def — `as root`, `with { }`, and any future
   per-call knob.
2. **Flags.**
3. **The project config file.**
4. **Built-in defaults.**

The plan sitting above the command line is the part that looks backwards and is not. This is
already shellf's model: `as root` on a def is intrinsic and no flag overrides it; `with { }`
(ADR-0022) overrides per call. The reason is that a plan is *authored* — reviewed, committed,
re-run by someone else — while a flag is typed once by whoever is at the terminal. When the two
disagree, the reviewed statement wins, and an invocation that wants otherwise has to change the
plan, where the change is visible.

Ansible reached the same order. That is corroboration, not the reason.

### 2. A config file does not replace flags

Every policy knob in the file is also a flag, and the flag wins. Removing the flag was
considered and rejected for one concrete reason: **CI**. An unattended run has to override a
setting without editing a version-controlled file, and a tool that forces a commit to change a
timeout for one run is worse in the place shellf is most used.

### 3. Only policy goes in the file

The three categories, applied to what exists today:

| layer | knobs |
|---|---|
| **mode** — what this run does; flags only | `--dry-run`, `--json`, `-v`, `--limit` |
| **input** — what the plan sees; flags only | `--inventory`, `--vars`, `--set`, `--secret-file`, `--secret-env` |
| **policy** — how this project is run; file **and** flag | `--parallel`, `--agent-ttl`, `--known-hosts` |

The rule for a future knob: if two people running the same project should reasonably use the
same value, it is policy. If it describes *this* invocation, it is a mode. If it feeds the plan
a value, it is an input.

`--insecure` is **refused in the file**, deliberately, though it would otherwise read as policy.
`insecure = true` committed to a repository disables host-key verification for everyone who
clones it, silently and permanently. As a flag it stays in the invocation, where it is visible
to the person taking the risk and to anyone reading their shell history.

### 4. One file, at the project root, in the shellf language

`shellf.conf`, beside the four directories of ADR-0038. No search path, no first-found-wins, no
merge: the project root is already the anchor for everything (ADR-0038 §2), so the file is found
the way `plans/` and `defs/` are found.

Written in the shellf language, on the project's own precedent — `docs/design/inventory.md`:
*"Expressed in the shellf language itself — no separate YAML/TOML."* The common advice is "never
invent a config format"; reusing the language the tool already ships and parses is not inventing
one.

**`shellf.lock` is the exception and stays one.** It is not written in the shellf language
(`internal/module/lock.go`) and should not be: it is machine-written, never hand-edited, and a
lock file's whole value is being diffable and boring. The distinction is authored-by-a-human
versus written-by-the-tool, and it is recorded here so the inconsistency is a decision rather
than a discovery.

### 5. A config file in a world-writable directory is refused

Same refusal Ansible makes, for the reason shellf already learned on the agent path (#391): a
file any local user can rewrite decides how a run behaves, and runs escalate. The refusal names
what it found rather than failing obscurely.

### 6. No user-level and no system-level configuration

A departure from clig.dev, and argued rather than dropped.

shellf is a fleet tool whose unit of work is a **shared, committed project**. A per-operator
default in `~/.config/shellf/` means the same plan on the same fleet behaves differently
depending on who ran it, and the difference lives in a file that is not in the repository, not
in the report, and not in the reviewer's diff. That is the reproducibility hazard shellf exists
to remove; the convenience does not pay for it.

`--config <path>` is rejected for the same reason: a run configured from outside the project is
a run nobody can reproduce from the repository.

If a real need appears — one operator, many projects, one genuinely personal preference — it
arrives as a new ADR with the case attached.

## Rejected alternatives

- **A config file instead of flags** — the starting instinct for this ADR. Rejected in §2: CI.
- **TOML or YAML.** A second syntax to learn, a second parser to carry, in a project whose
  inventory is already written in its own language by decision.
- **Environment variables as a layer.** clig.dev suggests them for the middle category. Nothing
  has asked for them, and they are the hardest layer to see when a run behaves unexpectedly: a
  value in a shell profile explains a failure nobody can find in the repository. Reconsidered
  when a case exists, not before. (`--secret-env` is unaffected: it names a *value* source, not
  configuration.)
- **Merging several config files.** The complexity Ansible avoided by stopping at the first
  found, and shellf does not need even that: there is exactly one project root.

## Consequences

- `--parallel`, `--agent-ttl` and `--known-hosts` gain a home in `shellf.conf`; the flags stay
  and outrank it.
- A project can commit how it is run, which is the point: the fan-out width and the agent TTL
  become reviewable facts rather than whatever the last operator typed.
- The documentation gate of #646 asserts that every **flag** appears in `README.md`. It knows
  nothing about config keys, so a key can ship undocumented exactly the way four flags did.
  Extending it is part of the work, not a follow-up.
- `run` and `status` read the same file: they already share one flag definition (#640), and a
  policy that differed between them would be the drift that issue removed.
- The timeout of #657 now has a place to live, and a precedence rule to live under — including
  that a per-step declaration in a plan, if one is ever added, outranks the flag.
