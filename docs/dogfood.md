# Dogfood reports

What shellf could not express, measured by writing a real deployment rather than by
imagining a list of instructions.

`docs/design.md` §02 names the wall: *"the language is 5% of the work; a correct
cross-distro stdlib of instructions plus the ecosystem is 95%"*. This file is how the 95%
gets a backlog. The method is the point — a stdlib grows either from evidence or from
imagination, and the second is how a configuration tool ends up with four hundred
half-correct modules.

## Method

1. Write a deployment somebody actually wants running, entirely in shellf.
2. Every time no def covers the need, write `unsafe shell { … }` and record it. That block
   **is** the finding: it names the missing instruction and shows the exact shape to
   support.
3. Run it. Twice. A plan that only previews proves nothing — this project has twice shipped
   a def whose `--dry-run` was green and whose `apply` was wrong (#486, #487).
4. Turn each finding into an issue carrying the shell it replaces.
5. Record what deliberately **stays** raw. Without that list the stdlib grows forever,
   because every shell block looks like a candidate.

The admission rule is ADR-0040 §3: `unsafe shell` around an atomic lock, an `eval` or a
one-off is doing its job. What counts as a gap is an operation **every** deployment would
want.

---

## 2026-08 — a two-host deployment

Debian 13, two machines. PostgreSQL on one, a service reading it on the other, each with
its own firewall. The smallest deployment that cannot be written as one host.

113 lines of plan, applied twice against two containers, second run converged.

**Result: 1 `unsafe shell` block, 1 bug, 1 language gap confirmed — and 3 mistakes of my
own that were not shellf's.**

*(The block became `postgres.role` / `postgres.database` in #545, so the plan now carries
none. The finding stands as it was measured — this line records what happened to it, which
is what the count is for.)*

### The honest count first

Three of the four times this plan stopped, the plan was wrong, not shellf:

| stopped at | cause |
| --- | --- |
| `dir.ensure` — permission denied | no `as root` around a generic def |
| `file.mode(…, "0640")` | **a real bug** — #543 |
| `systemd.unit("db-status", …)` | the name must carry its extension; the def says so |
| `nginx` fails to start | the plan reused the machine another example already runs :80 on |

Worth recording as a result rather than as noise. A first-time author of a plan makes these
mistakes, and what the three non-bugs demonstrate is that **each was refused before anything
was applied**, with a message naming the fix. A tool that half-applied and then reported a
problem would have left two machines in an unknown state.

It also means the finding count is one, not four. A dogfood that counts its author's
mistakes as product gaps measures nothing.

### The bug

**`file.mode(path, "0640")` never converged** (#543). `stat -c '%a'` strips a leading zero,
so the def applied the mode correctly and then reported `err.unconfirmed` — every run,
since the def shipped. `chmod` accepts both spellings and `0640` is the canonical one.

Every mode written anywhere in this repository was three digits, so nothing had ever
exercised it. The adverse cases (#489) vary the starting state; the hostile arguments
(#527) vary the path. **Neither varies the spelling of a value.**

Only visible because of ADR-0050, merged the same morning: without re-observing after an
acting apply, the def answered `ok.changed` and was wrong in silence.

### Missing instructions

| # | The shell that had to be written | Issue |
| - | -------------------------------- | ----- |
| 1 | `psql -tAc "SELECT 1 FROM pg_roles WHERE rolname=…"` then `CREATE ROLE` / `createdb` | #545 |

One block, and it is the one every stateful deployment needs. The shape is always the same:
a login role with a password, a database owned by it, both idempotent. The observe is a
`SELECT`; the apply is a `CREATE`.

It carries the same hazard as #503 did: the password must not reach `psql` through argv,
where `ps` shows it to every user on the machine. A def owns that; a shell block in an
example teaches whatever it happens to show.

### Near-misses — a def exists and does not fit

- **`file.replace` cannot find PostgreSQL's config** (#546). The path carries the major
  version — `/etc/postgresql/17/main/postgresql.conf` — so the plan hardcodes `17` and
  breaks on the next Debian. Not a gap in `file.replace`, which did its job; a gap in there
  being nothing that answers "where does this host keep its postgres configuration".

### The language gap, now measured rather than predicted

**A plan cannot read another host's inventory entry.** `${inventory.db.address}` was left
out of scope by ADR-0052, recorded as excluded pending "a real deployment that needs it".

This is that deployment, and the answer is not the one the exclusion assumed. The address
is written twice — once as `db`'s own `address`, once as `svc`'s `db_address`:

```
host svc = { address: "…", db_address: "10.0.0.40" }
host db  = { address: "10.0.0.40" }
```

Two hosts is where it is merely untidy. The shape of the problem is that **the duplication
grows with the number of consumers, not with the number of databases**: ten services
reading one database means the address is written eleven times, and nothing detects the one
that was not updated. There is no error, no drift report — the wrong service simply talks
to the wrong machine, or to nothing.

Recorded as #547. Not decided here: ADR-0052 listed real questions this raises (resolution
order between hosts, hosts outside the `on` block), and a measured cost does not by itself
answer them.

### What deliberately stays raw

- **`su - postgres -c …`** as the way to reach the database as its owner. Any `postgres.*`
  def would do this internally; the point is that the *escalation idiom* is not a gap.
- **Waiting for a service on another machine to accept connections.** The plan does not do
  it, and it did not need to: `on db` completes before `on svc` starts, which is the
  ordering shellf already guarantees. A `tcp.wait-for` would be inventing a need this
  deployment did not have.


## 2026-08 — a single-host web deployment

Debian 13. Traefik as the shared reverse proxy with Let's Encrypt, one application built on
the host behind it, a nightly backup on a systemd timer, ufw, a service account. Nothing
exotic: what a small production host looks like.

Written against the e2e container image, applied twice. 141 lines of plan.

**Result: 6 `unsafe shell` blocks, 3 near-misses, 2 bugs, 1 language gap.**

Tracked as epic #498 (the missing defs) plus #507, #508, #509, #510.

### Missing instructions

Each block below is what had to be written by hand.

| # | The shell that had to be written | Issue |
| - | -------------------------------- | ----- |
| 1 | `timedatectl set-timezone Europe/Paris` | #499 |
| 2 | `printf '…' > /etc/sysctl.d/90-app.conf && sysctl --system` | #500 |
| 3 | `useradd --system --no-create-home --shell /usr/sbin/nologin app` | #501 |
| 4 | `install -m 600 /dev/null …/acme.json` | #502 |
| 5 | `printf 'admin:%s\n' "$(openssl passwd -apr1 "$pw")" > …/users` | #503 |
| 6 | `docker image prune -af --filter "until=168h"` | #504 |

Two of them are worth more than a line.

**#503, the htpasswd credential, is not idempotent and nothing says so.** `openssl passwd`
salts randomly, so the file is rewritten on every run with a different hash — `ok.ran`
both times, the file different each time. Anything gated on that file changing fires
forever. An observe cannot compare hashes here; it has to *verify* the stored hash against
the password. That is why it needs a def and not a better shell block.

**#502 has a window, not just a gap.** `file.write(path, "")` followed by `file.mode` is
two steps, and between them the file is world-readable. For an ACME store, that window
holds a private key by the time anyone notices.

### Near-misses — a def exists and does not fit

- **`user.ensure` always runs `useradd -m`** (#501). A service account — no home, no login,
  a UID from the system range — cannot be expressed. The account that owns service files is
  not a user who logs in.
- **No `dir.mode`** (#505). `file.mode` chmods a directory perfectly well, and reads as a
  lie at the call site. `dir.ensure` / `dir.owner` / `dir.copy` / `dir.sync` all exist.
- **A systemd unit takes four instructions** (#506): two `file.write`, a `daemon-reload`,
  and a `service.ensure` pointed at a *timer*. Nothing validates the unit — a malformed
  `[Service]` is written happily and discovered when the backup silently never runs.

### Bugs the deployment surfaced

- **`dir.owner` converges over a path that does not exist** (#507). Its observe reads
  success from `find` printing nothing, and a missing path prints nothing. A `--dry-run`
  says `dir.ensure … would.created` and, one line down, `dir.owner … ok.already`.
- **`apt.update` can essentially never converge** (#488). Its observe asks whether an
  `*Release` file was modified in the last hour. Apt preserves the repository's
  `Last-Modified` and does not touch the file on a `304`, so the mtime says when *Debian*
  last published — and the files are named `…_InRelease`, which `-name '*Release'` does not
  match either. Two independent reasons for the same permanent non-convergence.

### The language gap

**A per-host inventory variable cannot go inside a string** (#509).

```
http.check("https://${domain}/healthz", "200")
→ undefined variable "domain" in interpolation
```

`${…}` resolves at parse, before any host exists (`docs/language.md:218`, ADR-0003); a bare
identifier resolves per host but can only be passed *whole*, and there is no concatenation.
The only way forward was to put the assembled URL in the inventory beside the domain it
repeats. Every derived value then needs its own field, and the copies drift the first time
a domain changes.

### One design finding

**A plan that verifies its own deployment cannot be previewed** (#508). `http.wait-for` is
a question, so it runs in every mode (ADR-0035) — including `--dry-run`, where the service
it waits for has by definition not been deployed. It failed, spent its full 60 second
timeout, and halted the preview. Everything after it went unseen. Deploy-then-verify is the
most ordinary plan shape there is.

### What deliberately stays raw

Nothing, this time — and that is itself a finding worth recording.

Every one of the six blocks does something any deployment would want, so all six became
issues. ADR-0040 exists to make the hatch **rare**, not unused, and the shapes it names —
an atomic `mkdir` lock, an `eval`, a genuine one-off — simply did not come up in a
deployment this ordinary. A later report that finds legitimate `unsafe shell` should say so
here, with the reason; a report that finds none again means the stdlib is behind, not that
the hatch is dead.

### Not measured

- **Cross-distro.** Debian/systemd only, like the stdlib.
- **ACME and TLS for real.** No public domain, so Traefik came up but never obtained a
  certificate. Whether the `acme.json` handling (#502) is sufficient in practice is
  unverified.
- **`timedatectl`** (#499). The e2e container has no system dbus, so the block failed there
  for a reason that has nothing to do with shellf. The gap stands; its implementation may
  have to avoid `timedatectl` entirely.
- **The application build.** It needed a real git repository, so that section was dropped
  from the applied run. `git.sync` was exercised in preview only.

### Progress marker

Re-running this deployment after the epic lands should reduce **6 `unsafe shell` blocks** to
0. That number is the measure — not the count of defs added, which says nothing about
whether the right ones were built.
