// Package orchestrator runs a plan across the inventory: `on` blocks in file
// order (sequential), hosts within a block in parallel (fan-out), and a host
// that errors is dropped from later blocks.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"shellf/internal/fleet"
	"shellf/internal/inventory"
	"shellf/internal/proto"
)

// Block assigns a step sequence to a target (group name or host alias).
type Block struct {
	Target string
	Steps  []proto.Step
}

type Plan []Block

// Options carries the run-wide knobs a caller may set. Its zero value is the behaviour
// shellf had before any of them existed, so a caller that does not care passes
// `Options{}` — and a new knob arrives as a field rather than as a tenth positional
// parameter next to three consecutive maps (#462).
type Options struct {
	// Parallel caps how many hosts a block dials at once. 0 takes fleet's default.
	Parallel int

	// Verbose asks each host to report every shell it ran, not only a failing one
	// (#470). It rides in Options because it is a property of the run, like Limit.
	Verbose bool

	// Limit narrows the run to these host aliases or group names. Empty means "the whole
	// plan". It can only ever *narrow*: a plan is the authority on what it touches, and a
	// flag able to add a host would make the plan a suggestion (#460).
	Limit []string
}

// HostOutcome is one host's result for one block.
type HostOutcome struct {
	Host     string
	Response proto.Response
	Err      error
}

// BlockReport is a block's per-host outcomes. Err is set when the block itself could
// not run — an unknown target has no host to hang an outcome on.
type BlockReport struct {
	Target string
	Hosts  []HostOutcome
	Err    error
}

// EmptyLimitError says a --limit selected nothing within a block's target. It is an error
// rather than an empty run for the reason #451 exists: the operator asked for work on a
// subset, and a green run that touched nobody reads exactly like one that converged.
type EmptyLimitError struct {
	Target string
	Limit  []string
}

func (e *EmptyLimitError) Error() string {
	return fmt.Sprintf("--limit %s selects no host within target %q",
		strings.Join(e.Limit, ","), e.Target)
}

// UnknownTargetError names a target no host and no group in the inventory declares.
type UnknownTargetError struct{ Target string }

func (e *UnknownTargetError) Error() string {
	return fmt.Sprintf("unknown target: the inventory declares no host or group named %q", e.Target)
}

// Run executes the plan. Blocks run sequentially; each block fans out over its live
// hosts, and a host that fails (transport error or an err step) is dropped from
// subsequent blocks. baseVars (--vars + plan bindings) and setVars (--set) resolve each
// Step's bare-identifier Refs per host, with precedence
// base < per-host inventory var < --set.
func Run(plan Plan, inv inventory.Inventory, agentBin, mode string, dial fleet.Dial, baseVars, setVars map[string]string, defs map[string]string, opt Options) []BlockReport {
	// Every target is resolved before anything runs. A name the inventory does not
	// declare is a typo in the plan, knowable without a single connection — and a run
	// that discovers it at block 3 has already changed the hosts of blocks 1 and 2.
	// So the whole plan is refused, and the report names every offending target rather
	// than the first (#451).
	if bad := unknownTargets(plan, inv); len(bad) > 0 {
		return bad
	}

	// The limit is resolved once, before any connection: a name nobody declared is a typo
	// in a flag, knowable without dialling, and it must not silently mean "no host" (#451).
	allowed, err := resolveLimit(opt.Limit, inv)
	if err != nil {
		return []BlockReport{{Target: plan[0].Target, Err: err}}
	}

	dead := map[string]bool{}
	var reports []BlockReport

	for _, block := range plan {
		members, _ := inv.Members(block.Target) // known: checked above
		var live []string
		for _, alias := range members {
			if allowed != nil && !allowed[alias] {
				continue // outside --limit: intersect, never extend
			}
			if !dead[alias] {
				live = append(live, alias)
			}
		}
		// A limit that meets none of this block's hosts is refused, and refused per block:
		// `--limit web1` against a plan whose second block targets the database tier is a
		// mistake worth naming, not a block to skip in silence.
		if allowed != nil && len(members) > 0 && !intersects(members, allowed) {
			reports = append(reports, BlockReport{
				Target: block.Target,
				Err:    &EmptyLimitError{Target: block.Target, Limit: opt.Limit},
			})
			continue
		}

		// One `on` = one interpreter (ADR-0012): unannotated shells need every
		// targeted host to resolve to the identical interpreter — checked here,
		// before any SSH. Annotated shells are uniform by construction.
		if err := interpAgreement(inv, live, block); err != nil {
			report := BlockReport{Target: block.Target}
			for _, alias := range live {
				report.Hosts = append(report.Hosts, HostOutcome{Host: alias, Err: &ResolveError{Err: err}})
				dead[alias] = true
			}
			reports = append(reports, report)
			continue
		}

		block := block // capture for the closure
		reqFor := func(alias string) ([]byte, error) {
			host, _ := inv.Resolve(alias)
			env := HostEnv(alias, host, inv, baseVars, setVars)
			// Templates are no longer rewritten here: `file.template` is a def, and
			// `~file.render` substitutes on the control host over this host's env,
			// through the channel (ADR-0024 preserved, #334).
			steps, err := proto.ResolveRefs(block.Steps, env, host.Interpreter)
			if err != nil {
				return nil, &ResolveError{Err: err}
			}
			return json.Marshal(proto.Request{Mode: mode, Steps: steps, Defs: defs, Verbose: opt.Verbose})
		}
		results := fleet.Run(live, agentBin, reqFor, dial, opt.Parallel)

		report := BlockReport{Target: block.Target}
		for _, hr := range results {
			report.Hosts = append(report.Hosts, HostOutcome{Host: hr.Target, Response: hr.Response, Err: hr.Err})
			if hr.Err != nil || failed(hr.Response) {
				dead[hr.Target] = true
			}
		}
		reports = append(reports, report)
	}
	return reports
}

// resolveLimit expands every --limit entry to the aliases it names. A nil result means no
// limit was given, which is different from an empty one — the second cannot happen, since
// an entry naming nothing is refused here.
func resolveLimit(limit []string, inv inventory.Inventory) (map[string]bool, error) {
	if len(limit) == 0 {
		return nil, nil
	}
	allowed := map[string]bool{}
	for _, name := range limit {
		members, known := inv.Members(name)
		if !known {
			// Wrapped so the message says where the name came from. Unwrapped, it read
			// "unknown target", which sends the operator hunting through the plan for a
			// typo that is in their flag.
			return nil, fmt.Errorf("--limit: %w", &UnknownTargetError{Target: name})
		}
		for _, alias := range members {
			allowed[alias] = true
		}
	}
	return allowed, nil
}

// intersects reports whether any of members is allowed.
func intersects(members []string, allowed map[string]bool) bool {
	for _, alias := range members {
		if allowed[alias] {
			return true
		}
	}
	return false
}

// unknownTargets returns a report per target the inventory does not declare, in plan
// order and once per distinct name: a plan repeating the same typo says it once.
func unknownTargets(plan Plan, inv inventory.Inventory) []BlockReport {
	var bad []BlockReport
	seen := map[string]bool{}
	for _, block := range plan {
		if _, known := inv.Members(block.Target); known || seen[block.Target] {
			continue
		}
		seen[block.Target] = true
		bad = append(bad, BlockReport{Target: block.Target, Err: &UnknownTargetError{Target: block.Target}})
	}
	return bad
}

// ResolveError is a per-host variable-resolution failure (e.g. an undefined
// variable) — not a transport problem. It lets reports avoid calling the host
// "unreachable" when the real issue is resolution.
type ResolveError struct{ Err error }

func (e *ResolveError) Error() string { return e.Err.Error() }
func (e *ResolveError) Unwrap() error { return e.Err }

// interpAgreement enforces one-`on`-one-interpreter (ADR-0012): if the block has
// an unannotated shell, every targeted host must resolve to the same interpreter.
func interpAgreement(inv inventory.Inventory, live []string, block Block) error {
	if !hasUnannotatedShell(block.Steps) {
		return nil // annotated shells are uniform by construction
	}
	seen := map[string][]string{}
	for _, alias := range live {
		h, _ := inv.Resolve(alias)
		i := h.Interpreter
		if i == "" {
			i = "sh"
		}
		seen[i] = append(seen[i], alias)
	}
	if len(seen) <= 1 {
		return nil
	}
	var parts []string
	for i, hosts := range seen {
		sort.Strings(hosts)
		parts = append(parts, strings.Join(hosts, ",")+"="+i)
	}
	sort.Strings(parts)
	return fmt.Errorf("one `on` = one interpreter: targeted hosts diverge (%s) — annotate the shell (shell(<interp>){}) or split the group", strings.Join(parts, "; "))
}

// hasUnannotatedShell reports whether any plan shell lacks an explicit
// interpreter (so its interpreter comes from the inventory).
func hasUnannotatedShell(steps []proto.Step) bool {
	for _, s := range steps {
		switch {
		case s.Instruction == "shell" && s.Interp == "":
			return true
		case s.If != nil:
			if s.If.Cond != nil && s.If.Cond.Instruction == "shell" && s.If.Cond.Interp == "" {
				return true
			}
			if hasUnannotatedShell(s.If.Then) || hasUnannotatedShell(s.If.Else) {
				return true
			}
		case len(s.Block) > 0:
			if hasUnannotatedShell(s.Block) {
				return true
			}
		case len(s.Parallel) > 0:
			if hasUnannotatedShell(s.Parallel) {
				return true
			}
		}
	}
	return false
}

// mergeEnv layers the variable tables by increasing precedence: base (--vars +
// plan bindings) < per-host inventory vars < --set.
// HostEnv is the variable table a step is resolved against on one host: the layered
// globals, then this host's own values under the `inventory.` prefix.
//
// Exported because it is the *definition* of per-host resolution, and anything checking a
// plan resolves — `cmd/shellf`'s example test does — must ask the same question the
// orchestrator asks. A second copy of this layering is a second answer.
func HostEnv(alias string, host inventory.Host, inv inventory.Inventory, base, set map[string]string) map[string]string {
	// host.Vars is deliberately absent from the bare table (#540, ADR-0053). A bare
	// reference and `${name}` are the same variable written two ways, and `${name}` is
	// substituted at parse against the plan — so feeding the inventory into one and not the
	// other gave a single name two values, decided by nothing but braces.
	env := mergeEnv(base, set)
	addInventoryFields(env, alias, host)
	addCrossHostFields(env, inv)
	return env
}

// addCrossHostFields exposes every declared host under `inventory.<alias>.<field>`, so a
// plan can read an address that exists once instead of copying it into the entry of every
// machine that needs it (ADR-0054).
//
// A plain table and no resolution step: an inventory holds no expressions — ADR-0052
// rejected derived fields, which is what guarantees it — so this is a lookup and cannot
// cycle. That is the assumption ADR-0052's exclusion rested on being false.
//
// Resolved hosts, not the literal blocks: `defaults` are merged first, so a field means the
// same thing whether the host reads it or its neighbour does (ADR-0054 §2).
//
// Groups are absent on purpose. `${inventory.web.address}` where `web` names a group has no
// single answer, and picking its first member silently is the kind of helpfulness that ships
// a deployment pointed at the wrong machine.
func addCrossHostFields(env map[string]string, inv inventory.Inventory) {
	for alias := range inv.Hosts {
		host, ok := inv.Resolve(alias)
		if !ok {
			continue
		}
		p := proto.InventoryPrefix + alias + "."
		env[p+"name"] = alias
		env[p+"address"] = host.Address
		env[p+"user"] = host.User
		env[p+"port"] = host.Port
		for k, v := range host.Vars {
			// `key` is refused at parse; leaving it out of the table too means a path to a
			// private key is never one lookup away from a rendered file.
			if k == "key" {
				continue
			}
			env[p+k] = v
		}
	}
}

// addInventoryFields exposes this host's own values under the `inventory.` prefix, for the
// `${inventory.<field>}` a plan could not resolve while it was being read (ADR-0052).
//
// Added **after** mergeEnv on purpose: these names are the host's, and `--set` does not
// override them. That is the point of writing the source at the call site — a flag that
// silently redirected `${inventory.domain}` would put back the ambiguity the prefix removes.
//
// `key` is absent, and is refused by the parser rather than merely missing here: it is the
// path to a private key, and a plan asking for it should be told why, not told it is
// undefined.
func addInventoryFields(env map[string]string, alias string, host inventory.Host) {
	env[proto.InventoryPrefix+"name"] = alias
	env[proto.InventoryPrefix+"address"] = host.Address
	env[proto.InventoryPrefix+"user"] = host.User
	env[proto.InventoryPrefix+"port"] = host.Port
	// Free-form fields last: `host web = { domain: "…" }` is what the prefix is mostly for,
	// and a host naming a field `address` means that field.
	for k, v := range host.Vars {
		env[proto.InventoryPrefix+k] = v
	}
}

// mergeEnv layers the plan-side tables: `--vars` and plan bindings, then `--set`
// (ADR-0003 §3, minus the inventory level ADR-0053 removed). It takes no host table on
// purpose — the inventory reaches a plan through `${inventory.…}` and nowhere else, and a
// parameter that does not exist cannot be passed by mistake.
func mergeEnv(base, set map[string]string) map[string]string {
	env := make(map[string]string, len(base)+len(set))
	for k, v := range base {
		env[k] = v
	}
	for k, v := range set {
		env[k] = v
	}
	return env
}

func failed(r proto.Response) bool {
	if r.Error != "" {
		return true
	}
	for _, s := range r.Results {
		if s.Category == "err" {
			return true
		}
	}
	return false
}
