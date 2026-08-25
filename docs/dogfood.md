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
