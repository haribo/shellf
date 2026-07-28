package orchestrator

import (
	"encoding/json"
	"testing"

	"shellf/internal/agent"
	"shellf/internal/inventory"
	"shellf/internal/transport"
)

// fakeTr returns a canned agent.Response, baked per alias by the dial closure.
type fakeTr struct{ resp agent.Response }

func (f fakeTr) Run(_ string, _ []byte) ([]byte, error) {
	return json.Marshal(f.resp)
}

var _ transport.Transport = fakeTr{}

func TestRun_DeadHostDroppedFromLaterBlock(t *testing.T) {
	inv := inventory.Inventory{
		Hosts: map[string]inventory.Host{
			"h1": {Address: "1"},
			"h2": {Address: "2"},
		},
		Groups: map[string][]string{"all": {"h1", "h2"}},
	}
	resp := map[string]agent.Response{
		"h1": {Results: []agent.StepResult{{Category: "err", Tag: "runtime"}}, Halted: true},
		"h2": {Results: []agent.StepResult{{Category: "ok", Tag: "installed"}}},
	}
	dial := func(alias string) transport.Transport { return fakeTr{resp: resp[alias]} }

	plan := Plan{
		{Target: "all", Steps: []agent.Step{{Instruction: "apt-install", Args: map[string]string{"pkg": "nginx"}}}},
		{Target: "all", Steps: []agent.Step{{Instruction: "apt-install", Args: map[string]string{"pkg": "redis"}}}},
	}
	reports := Run(plan, inv, "/bin/agent", "apply", dial)

	if len(reports) != 2 {
		t.Fatalf("want 2 block reports, got %d", len(reports))
	}
	if len(reports[0].Hosts) != 2 {
		t.Fatalf("block 1 should touch both hosts, got %d", len(reports[0].Hosts))
	}
	// h1 errored in block 1 → dropped from block 2.
	if len(reports[1].Hosts) != 1 || reports[1].Hosts[0].Host != "h2" {
		t.Fatalf("block 2 should run only on h2, got %+v", reports[1].Hosts)
	}
}
