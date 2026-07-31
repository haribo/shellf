// Package orchestrator runs a plan across the inventory: `on` blocks in file
// order (sequential), hosts within a block in parallel (fan-out), and a host
// that errors is dropped from later blocks.
package orchestrator

import (
	"encoding/json"

	"shellf/internal/proto"
	"shellf/internal/fleet"
	"shellf/internal/inventory"
)

// Block assigns a step sequence to a target (group name or host alias).
type Block struct {
	Target string
	Steps  []proto.Step
}

type Plan []Block

// HostOutcome is one host's result for one block.
type HostOutcome struct {
	Host     string
	Response proto.Response
	Err      error
}

// BlockReport is a block's per-host outcomes.
type BlockReport struct {
	Target string
	Hosts  []HostOutcome
}

// Run executes the plan. Blocks run sequentially; each block fans out over its
// live hosts. A host that fails (transport error or an err step) is dropped
// from subsequent blocks.
// Run executes the plan. baseVars (--vars + plan bindings) and setVars (--set)
// resolve each Step's bare-identifier Refs per host, with precedence
// base < per-host inventory var < --set.
func Run(plan Plan, inv inventory.Inventory, agentBin, mode string, dial fleet.Dial, baseVars, setVars map[string]string) []BlockReport {
	dead := map[string]bool{}
	var reports []BlockReport

	for _, block := range plan {
		var live []string
		for _, alias := range inv.Members(block.Target) {
			if !dead[alias] {
				live = append(live, alias)
			}
		}

		block := block // capture for the closure
		reqFor := func(alias string) ([]byte, error) {
			host, _ := inv.Resolve(alias)
			env := mergeEnv(baseVars, host.Vars, setVars)
			steps, err := proto.ResolveRefs(block.Steps, env)
			if err != nil {
				return nil, &ResolveError{Err: err}
			}
			return json.Marshal(proto.Request{Mode: mode, Steps: steps})
		}
		results := fleet.Run(live, agentBin, reqFor, dial)

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

// ResolveError is a per-host variable-resolution failure (e.g. an undefined
// variable) — not a transport problem. It lets reports avoid calling the host
// "unreachable" when the real issue is resolution.
type ResolveError struct{ Err error }

func (e *ResolveError) Error() string { return e.Err.Error() }
func (e *ResolveError) Unwrap() error { return e.Err }

// mergeEnv layers the variable tables by increasing precedence: base (--vars +
// plan bindings) < per-host inventory vars < --set.
func mergeEnv(base, host, set map[string]string) map[string]string {
	env := make(map[string]string, len(base)+len(host)+len(set))
	for k, v := range base {
		env[k] = v
	}
	for k, v := range host {
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
