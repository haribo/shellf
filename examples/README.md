# Examples

Two plans, smallest first. Both share one inventory, `inventory.shellf`, kept
**outside** the plan directories on purpose: a plan's own directory is its def
package (ADR-0014), so an inventory kept inside would be parsed as a def and fail.
Fill in real addresses before running; authentication uses your ssh-agent by
default (ADR-0026).

Every plan here is parsed by CI (`TestExamplesParse`), so these stay runnable.

## `webserver/` — the 30-second read

A package install, a service, files, `as root`, and `?` error handling. Start here.

```
shellf run    --inventory examples/inventory.shellf --check examples/webserver/plan.shellf
shellf run    --inventory examples/inventory.shellf        examples/webserver/plan.shellf
shellf status --inventory examples/inventory.shellf        examples/webserver/plan.shellf
```

## `blog/` — a containerized stack

A fuller tour: a **user def** (`data-dir`, in the sibling `defs.shellf`), a **local
import** (`../shared`, called `common.banner`), a **secret** (`db_password` via
`--secret-file`), a **per-host template** (`compose.env.tmpl`), `docker.network`,
`docker.compose-up`, and `ufw.open` in a `for` loop.

```
shellf run --inventory examples/inventory.shellf \
  --secret-file db_password=./db_password --check examples/blog/plan.shellf
```

Each feature appears in exactly one example, with a comment saying what it shows.
`shared/` is a def library imported by `blog/`; it has no plan of its own.
