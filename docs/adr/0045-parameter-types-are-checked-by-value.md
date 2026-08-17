# ADR 0045 — A parameter's declared type is checked, by value

## Status

Active. First record of what a parameter's type *means*: the annotation existed in the
grammar from the start and was never given a decision. Applies the timing rule of
[ADR-0034](0034-control-host-primitives.md) §5 — a plan's error is reported when the plan
is read, not mid-deploy.

## Context

A def declares its parameters' types — `def ensure(name: str, running: str, enabled: str)`
— and nothing has ever checked them. Measured on `develop`:

- **75 parameters across the stdlib, the examples and the e2e plans. All `str`. Not one
  `bool`.** The type the grammar offers is used by nobody.
- **Any word is accepted as a type.** `def t(p: banana)` parses and runs: the parser reads
  an identifier (`internal/lang/def_parser.go:188`) and nothing reads it back.
- Booleans travel as **strings**: `service.ensure("nginx", "true", "true")`,
  `def compose-up(dir: str, build: str = "false")`, compared as text in `observe`.

That last convention is not merely untidy. Run against the e2e container:

```
service.ensure("cron", "yes", "true")   →   ok.converged
cron: active  →  inactive
```

An operator writes `"yes"` meaning *running*. The `apply` tests `[ "$running" = true ]`, so
anything that is not exactly `"true"` means **stop**, and the `observe` compares `"yes"`
against `true`/`false`, so the def never converges and stops the service again on every
run. The report says `ok.converged`.

This is the failure this project keeps finding: a run that states it did what it did not
do — #390 (a tree landing as the wrong user under `ok.copied`), #411 (an empty file under
`ok.done`). Here the cause is an annotation that documents an intent nothing enforces.

## Decision

### 1. The type vocabulary is closed

`str` and `bool`. Any other word is a parse error naming what was written. `banana` stops
being a type.

### 2. A `bool` parameter accepts a boolean **value**, however it is written

What is refused is a value that is not a boolean — not a way of writing one:

```
service.ensure("cron", true, true)       # accepted
service.ensure("cron", "true", "true")   # accepted — that is a boolean, written as text
service.ensure("cron", "yes", "true")    # refused: "yes" is not a boolean
tls = "true"
service.ensure("nginx", tls, true)       # accepted — the variable holds a boolean
```

The alternative — accepting only the bare literals — is rejected in §5. It would forbid
holding a boolean in a variable, which `--set tls=true` makes an ordinary thing to do, and
it would break every existing plan for a question of style rather than of correctness.

### 3. Checked where the value is still known

At parse time for a plan: variables are already interpolated there
(`ParsePlanWithVars`), so the argument's final value is available before a single host is
contacted. At evaluation for a call from one def to another, where values carry their type.

The refusal names the parameter and the value it received. An operator reading *`running`
expects a boolean, got "yes"* knows the fix; an operator reading a stopped service does
not.

### 4. The wire stays as it is

`Step.Args` and `with { }` are `map[string]string`, at parse and on the channel
(`internal/proto/proto.go`). A boolean already travels as `"true"`. Typing the protocol
would version the channel for no gain the check does not already deliver, since the check
happens before anything is sent.

### 5. What this does not do

- It does not restrict `str` beyond what #411 already refuses (control-host bytes). No
  measured defect justifies more, and `%"…"` paths must keep crossing into `str`
  parameters — that is how a plan's marked path reaches a def (#332).
- It does not make the stdlib take `bool` everywhere. Two defs change, because two defs
  have boolean parameters: `service.ensure` and `docker.compose-up`. `compare` stays a
  `str` — it is an enumeration (`meta` / `sha256`), not a boolean.
- It does not give the language a type system. Two names, checked at the boundary where
  a wrong value has been shown to do damage.

## Rejected alternatives

- **Accept only the bare literals `true` / `false`** ("by the form"). Stricter to read, and
  it forbids a boolean in a variable while breaking every plan that writes `"true"` today.
  The defect measured is a value that is not a boolean, not a boolean spelled with quotes.
- **Type the protocol** so arguments keep their type to the target. A channel version, new
  encoding, and an agent that must agree — to re-derive on the far side something the
  control host already knew before sending.
- **Close the vocabulary and check nothing** (`str`/`bool` accepted, neither enforced).
  Half a fix: `banana` stops parsing, `"yes"` still stops the service. It was the first
  recommendation made on #418 and it was wrong: it postpones the decision to the day
  somebody's service goes down.
- **Leave it alone.** The annotation would keep reading like a guarantee while being
  decoration — the exact shape of defect ADR-0013 and #378 exist to prevent.

## Consequences

- Breaking, in one direction only: a plan passing a non-boolean where a `bool` is declared
  now fails at parse. No plan in this repository does — the migration is two defs and their
  call sites, and `"true"` keeps working everywhere it appears.
- `InstructionSig` carries parameter types, so the parser can check an argument against the
  signature it already uses for names and arity.
- A def author gains a way to say what a parameter is, that the language now backs. The
  next question — whether other types are worth having — arrives with a real need, not
  ahead of one.
