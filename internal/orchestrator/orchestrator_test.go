package orchestrator

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"shellf/internal/inventory"
	"shellf/internal/proto"
	"shellf/internal/transport"
)

// fakeTr returns a canned proto.Response, baked per alias by the dial closure.
type fakeTr struct{ resp proto.Response }

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
	resp := map[string]proto.Response{
		"h1": {Results: []proto.StepResult{{Category: "err", Tag: "runtime"}}, Halted: true},
		"h2": {Results: []proto.StepResult{{Category: "ok", Tag: "installed"}}},
	}
	dial := func(alias string) transport.Transport { return fakeTr{resp: resp[alias]} }

	plan := Plan{
		{Target: "all", Steps: []proto.Step{{Instruction: "apt-install", Args: map[string]string{"pkg": "nginx"}}}},
		{Target: "all", Steps: []proto.Step{{Instruction: "apt-install", Args: map[string]string{"pkg": "redis"}}}},
	}
	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil)

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

func TestRun_ResolveErrorTyped(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}},
		Groups: map[string][]string{"all": {"h1"}},
	}
	dial := func(alias string) transport.Transport { return fakeTr{} }
	// a bare-identifier ref that no env resolves → reqFor fails before dialing
	plan := Plan{{Target: "all", Steps: []proto.Step{
		{Instruction: "dir-owner", Refs: map[string]string{"owner": "missing"}},
	}}}
	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil)

	var re *ResolveError
	if !errors.As(reports[0].Hosts[0].Err, &re) {
		t.Fatalf("expected a *ResolveError, got %v", reports[0].Hosts[0].Err)
	}
}

func TestMergeEnvPrecedence(t *testing.T) {
	base := map[string]string{"owner": "base", "keep": "1"}
	host := map[string]string{"owner": "host"}
	set := map[string]string{"owner": "set"}
	if got := mergeEnv(base, host, set)["owner"]; got != "set" {
		t.Fatalf("--set should win: %q", got)
	}
	if got := mergeEnv(base, host, nil)["owner"]; got != "host" {
		t.Fatalf("host should win over base: %q", got)
	}
	if got := mergeEnv(base, nil, nil)["keep"]; got != "1" {
		t.Fatalf("base value lost: %q", got)
	}
}

// captureTr records the per-host Request bytes it is handed.
type captureTr struct {
	alias string
	reqs  map[string][]byte
	mu    *sync.Mutex
}

func (c captureTr) Run(_ string, req []byte) ([]byte, error) {
	c.mu.Lock()
	c.reqs[c.alias] = req
	c.mu.Unlock()
	return json.Marshal(proto.Response{})
}

func TestRun_ResolvesVarsPerHost(t *testing.T) {
	inv := inventory.Inventory{
		Hosts: map[string]inventory.Host{
			"web1": {Address: "1", Vars: map[string]string{"owner": "alice"}},
			"web2": {Address: "2", Vars: map[string]string{"owner": "bob"}},
		},
		Groups: map[string][]string{"web": {"web1", "web2"}},
	}
	reqs := map[string][]byte{}
	var mu sync.Mutex
	dial := func(alias string) transport.Transport { return captureTr{alias: alias, reqs: reqs, mu: &mu} }

	plan := Plan{{Target: "web", Steps: []proto.Step{
		{Instruction: "dir-owner", Args: map[string]string{"path": "/opt"}, Refs: map[string]string{"owner": "owner"}},
	}}}
	Run(plan, inv, "/bin/agent", "apply", dial, map[string]string{}, nil)

	if !strings.Contains(string(reqs["web1"]), `"owner":"alice"`) {
		t.Fatalf("web1 request should resolve owner=alice: %s", reqs["web1"])
	}
	if !strings.Contains(string(reqs["web2"]), `"owner":"bob"`) {
		t.Fatalf("web2 request should resolve owner=bob: %s", reqs["web2"])
	}
}
