# Examples

**One project, two plans** — which is what a real project looks like. The layout is the
one every shellf project uses (ADR-0038):

```
examples/
  plans/          webserver.shellf, blog.shellf
  defs/           blog/, common/          → called blog.data-dir, common.banner
  assets/         blog/compose.env.tmpl   → addressed %"blog/compose.env.tmpl"
  inventories/    inventory.shellf
```

A def is addressed by name, not by where it sits next to: `defs/common/banner.shellf`
declares `banner`, and any plan calls `common.banner(…)` with no import and no flag.
Content paths are relative to `assets/`, so they read the same from any plan.

Fill in real addresses before running; authentication uses your ssh-agent by default
(ADR-0026).

Every plan here is loaded **and resolved against every host it targets** by CI
(`TestExamplesResolvePerHost`), so a plan that could not run turns the build red — which
is how `blog.shellf` was found calling `ufw.open(port, …)` instead of `ufw.open("${port}",
…)`, a form the language forbids.

## `plans/webserver.shellf` — the 30-second read

A package install, a service, files, `as root`, and `?` error handling. Start here.

```
shellf run    --inventory examples/inventories/inventory.shellf --dry-run examples/plans/webserver.shellf
shellf run    --inventory examples/inventories/inventory.shellf        examples/plans/webserver.shellf
shellf status --inventory examples/inventories/inventory.shellf        examples/plans/webserver.shellf
```

## `plans/blog.shellf` — a containerized stack

A fuller tour: a **user def** (`blog.data-dir`), a **def shared between plans**
(`common.banner`), a **secret** (`db_password` via `--secret-file`), a **per-host
template** (`%"blog/compose.env.tmpl"`), `docker.network`, `docker.compose-up`, and
`ufw.open` in a `for` loop.

```
shellf run --inventory examples/inventories/inventory.shellf \
  --secret-file db_password=./db_password --dry-run examples/plans/blog.shellf
```

Each feature appears in exactly one example, with a comment saying what it shows.
`defs/common/` is shared by both plans and has no plan of its own — which is the point of
addressing a def by name: nothing had to move for the second plan to use it.
