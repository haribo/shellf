// Package orchestrator runs a plan across the inventory: `on` blocks in file
// order (sequential), hosts within a block in parallel (fan-out), and a host
// that errors is dropped from later blocks.
package orchestrator

import (
	"encoding/json"

	"shellf/internal/agent"
	"shellf/internal/fleet"
	"shellf/internal/inventory"
)

// Block assigns a step sequence to a target (group name or host alias).
type Block struct {
	Target string
	Steps  []agent.Step
}

type Plan []Block

// HostOutcome is one host's result for one block.
type HostOutcome struct {
	Host     string
	Response agent.Response
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
func Run(plan Plan, inv inventory.Inventory, agentBin, mode string, dial fleet.Dial) []BlockReport {
	dead := map[string]bool{}
	var reports []BlockReport

	for _, block := range plan {
		var live []string
		for _, alias := range inv.Members(block.Target) {
			if !dead[alias] {
				live = append(live, alias)
			}
		}

		req, _ := json.Marshal(agent.Request{Mode: mode, Steps: block.Steps})
		results := fleet.Run(live, agentBin, req, dial)

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

func failed(r agent.Response) bool {
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
