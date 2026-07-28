# Orchestration

The orchestration plane assigns instructions to hosts and orders them. It is
separate from execution: a `def` is host-agnostic and never names a host. The
orchestration plan maps `def` calls onto the inventory.

## `on` block

`on <host|group>` runs a sequence of `def` calls against a target. A single host
is a singleton group.

```
on db  { postgres-install() }
on web { nginx-install(); nginx-config() }
```

The `def` inside stay neutral — `on` composes them, never the reverse.

## Execution order

| Axis | Behavior |
|---|---|
| Between `on` blocks | **Sequential**, file order. `on db` completes on all its hosts before `on web` starts. |
| Hosts within one block | **Parallel** (fan-out) — conflict-free, each host independent. |
| `def` within one block, per host | **Sequential**. `nginx-install` then `nginx-config` on that host. |

One SSH session per host carries the whole block's sequence (the agent is pushed
once, not per `def`).

## `parallel` block

Runs `def` concurrently **on a single host**. Intra-host parallelism is explicit
and the user's responsibility — the shell is opaque, so shellf cannot infer the
absence of conflict.

```
on web {
  parallel {
    nginx-install()
    fetch-assets()
  }
  nginx-config()          // after both branches complete
}
```

- **Result**: aggregate — `err` if any branch is `err`. Branches run to
  completion before the aggregate; no short-circuit.
- **Halting**: an aggregate `err` halts the rest of the block (`nginx-config`
  skipped), per the halting rule.
- **Real speedup only without a shared exclusive resource.** shellf does start
  the branches together, but the system may re-serialize them: two `apt` runs
  contend for `/var/lib/dpkg/lock` — the second waits, total time ≈ sequential.
  shellf did its part; dpkg imposed the constraint. Use `parallel` for
  genuinely independent I/O (install one thing while downloading another).

## Failure

Per-host, continue. A host that returns `err` is dropped from later blocks; the
others proceed. The run ends with an aggregate report. No global halt, no
rolling/batch, no dependency DAG at day 1 — added later if needed.

## Philosophy

The target is a power-user who is responsible for their plan. shellf does not
guard against absurd use — two `apt` in one `parallel` is a self-evident mistake,
not something the tool prevents.

In particular, **no lock pre-detection**: checking `/var/lib/dpkg/lock` before
apply would be racy (free at check, taken at apply — TOCTOU), redundant (`apt-get`
already waits via `DPkg::Lock::Timeout`), and would contradict the dry-run stance
(the apt lock is the canonical unpredictable effect). A busy lock surfaces
naturally as `err.runtime` — which is why the error set is open.
