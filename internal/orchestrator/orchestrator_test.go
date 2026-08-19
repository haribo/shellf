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
		{Target: "all", Steps: []proto.Step{{Instruction: "apt.install", Args: map[string]string{"pkg": "nginx"}}}},
		{Target: "all", Steps: []proto.Step{{Instruction: "apt.install", Args: map[string]string{"pkg": "redis"}}}},
	}
	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{})

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
		{Instruction: "dir.owner", Refs: map[string]string{"owner": "missing"}},
	}}}
	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{})

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
		{Instruction: "dir.owner", Args: map[string]string{"path": "/opt"}, Refs: map[string]string{"owner": "owner"}},
	}}}
	Run(plan, inv, "/bin/agent", "apply", dial, map[string]string{}, nil, nil, Options{})

	if !strings.Contains(string(reqs["web1"]), `"owner":"alice"`) {
		t.Fatalf("web1 request should resolve owner=alice: %s", reqs["web1"])
	}
	if !strings.Contains(string(reqs["web2"]), `"owner":"bob"`) {
		t.Fatalf("web2 request should resolve owner=bob: %s", reqs["web2"])
	}
}

// ADR-0024: a template renders per host, over that host's env — each host's
// request carries its own file-write, and no `file.template` reaches the wire.

// A block naming a target the inventory does not define is an error, not an empty
// report. It used to expand to no host, produce a block with no outcome, and let the
// run exit 0 — a typo in a group name was a deployment that never happened, reported
// as a success (#451).
func TestRun_UnknownTargetIsAnError(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}},
		Groups: map[string][]string{"web": {"h1"}},
	}
	dial := func(string) transport.Transport {
		t.Fatal("an unknown target must be refused before any dial")
		return nil
	}
	plan := Plan{{Target: "wbe", Steps: []proto.Step{{Instruction: "apt.install"}}}}

	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{})

	if len(reports) != 1 {
		t.Fatalf("want 1 block report, got %d", len(reports))
	}
	if reports[0].Err == nil {
		t.Fatal("an unknown target must carry a block error")
	}
	if !strings.Contains(reports[0].Err.Error(), "wbe") {
		t.Fatalf("the error must name the target: %v", reports[0].Err)
	}
	var ue *UnknownTargetError
	if !errors.As(reports[0].Err, &ue) {
		t.Fatalf("the error must be typed, so the CLI can tell it from a transport failure: %T", reports[0].Err)
	}
}

// A group declared with no members is not a typo — it is a legitimate no-op, and it
// stays a success. What it must not do is look identical to work having happened
// (#451).
func TestRun_DeclaredEmptyGroupIsNotAnError(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}},
		Groups: map[string][]string{"spare": {}},
	}
	dial := func(string) transport.Transport {
		t.Fatal("an empty group must dial nothing")
		return nil
	}
	plan := Plan{{Target: "spare", Steps: []proto.Step{{Instruction: "apt.install"}}}}

	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{})

	if len(reports) != 1 || reports[0].Err != nil {
		t.Fatalf("a declared empty group must not error: %+v", reports)
	}
	if len(reports[0].Hosts) != 0 {
		t.Fatalf("and it touches no host: %+v", reports[0].Hosts)
	}
}

// A host that died in an earlier block leaves its group empty. That is not an unknown
// target, and must not be reported as one — the target existed, its hosts are gone.
func TestRun_GroupEmptiedByDeadHostsIsNotUnknown(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}},
		Groups: map[string][]string{"all": {"h1"}},
	}
	dial := func(string) transport.Transport {
		return fakeTr{resp: proto.Response{Results: []proto.StepResult{{Category: "err", Tag: "runtime"}}}}
	}
	plan := Plan{
		{Target: "all", Steps: []proto.Step{{Instruction: "apt.install"}}},
		{Target: "all", Steps: []proto.Step{{Instruction: "apt.install"}}},
	}

	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{})

	if len(reports) != 2 {
		t.Fatalf("want 2 block reports, got %d", len(reports))
	}
	if reports[1].Err != nil {
		t.Fatalf("a group emptied by dead hosts is not an unknown target: %v", reports[1].Err)
	}
}

// `--limit` narrows a run to a subset of what the plan targets, which is the ordinary
// first move before a real deployment: try it on one host, look, then let it loose (#460).
func TestRun_LimitNarrowsToASubset(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}, "h2": {Address: "2"}, "h3": {Address: "3"}},
		Groups: map[string][]string{"web": {"h1", "h2", "h3"}},
	}
	var mu sync.Mutex
	var dialled []string
	dial := func(alias string) transport.Transport {
		mu.Lock()
		dialled = append(dialled, alias)
		mu.Unlock()
		return fakeTr{resp: proto.Response{Results: []proto.StepResult{{Category: "ok", Tag: "done"}}}}
	}
	plan := Plan{{Target: "web", Steps: []proto.Step{{Instruction: "apt.install"}}}}

	Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{Limit: []string{"h2"}})

	// Asserted on the dial, not on the report: what matters is which hosts were touched.
	if len(dialled) != 1 || dialled[0] != "h2" {
		t.Fatalf("only the limited host may be dialled, got %v", dialled)
	}
}

// A limit can only narrow. A plan is the authority on what it touches; a flag that could
// add a host would make the plan a suggestion.
func TestRun_LimitCannotExtendBeyondThePlan(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}, "other": {Address: "9"}},
		Groups: map[string][]string{"web": {"h1"}},
	}
	dial := func(alias string) transport.Transport {
		if alias == "other" {
			t.Fatal("a host outside the plan's target must never be dialled")
		}
		return fakeTr{resp: proto.Response{Results: []proto.StepResult{{Category: "ok"}}}}
	}
	plan := Plan{{Target: "web", Steps: []proto.Step{{Instruction: "apt.install"}}}}

	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{Limit: []string{"other"}})

	// Intersecting to nothing is #451 arriving through another door: the operator asked
	// for work on a subset and must not get a green run that touched nobody.
	if len(reports) != 1 || reports[0].Err == nil {
		t.Fatalf("a limit that selects nothing must error: %+v", reports)
	}
	var el *EmptyLimitError
	if !errors.As(reports[0].Err, &el) {
		t.Fatalf("the error must be typed so the CLI can tell it apart: %T", reports[0].Err)
	}
}

// A name the inventory does not declare is refused before any connection, exactly as a
// plan's own target is (#451).
func TestRun_LimitOnAnUndeclaredNameIsRefused(t *testing.T) {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{"h1": {Address: "1"}},
		Groups: map[string][]string{"web": {"h1"}},
	}
	dial := func(string) transport.Transport {
		t.Fatal("an undeclared limit must be refused before any dial")
		return nil
	}
	plan := Plan{{Target: "web", Steps: []proto.Step{{Instruction: "apt.install"}}}}

	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{Limit: []string{"wbe"}})

	if len(reports) != 1 || reports[0].Err == nil {
		t.Fatalf("an undeclared limit must be refused: %+v", reports)
	}
	if !strings.Contains(reports[0].Err.Error(), "wbe") {
		t.Fatalf("the error must name it: %v", reports[0].Err)
	}
	// And it must say the name came from the flag: "unknown target" alone sends the
	// operator hunting through the plan for a typo that is in their command line.
	if !strings.Contains(reports[0].Err.Error(), "--limit") {
		t.Fatalf("the error must name the flag: %v", reports[0].Err)
	}
	var ut *UnknownTargetError
	if !errors.As(reports[0].Err, &ut) {
		t.Fatalf("and stay typed through the wrap: %T", reports[0].Err)
	}
}

// A limit may name a group, in the same vocabulary a plan's `on` uses.
func TestRun_LimitAcceptsAGroup(t *testing.T) {
	inv := inventory.Inventory{
		Hosts: map[string]inventory.Host{"h1": {Address: "1"}, "h2": {Address: "2"}, "h3": {Address: "3"}},
		Groups: map[string][]string{
			"all":    {"h1", "h2", "h3"},
			"canary": {"h2"},
		},
	}
	var mu sync.Mutex
	var dialled []string
	dial := func(alias string) transport.Transport {
		mu.Lock()
		dialled = append(dialled, alias)
		mu.Unlock()
		return fakeTr{resp: proto.Response{Results: []proto.StepResult{{Category: "ok"}}}}
	}
	plan := Plan{{Target: "all", Steps: []proto.Step{{Instruction: "apt.install"}}}}

	Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, Options{Limit: []string{"canary"}})

	if len(dialled) != 1 || dialled[0] != "h2" {
		t.Fatalf("a group limit must expand like any target, got %v", dialled)
	}
}
