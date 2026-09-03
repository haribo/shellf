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

// The chain is plan-side only since #540: `--vars` < plan binding < `--set` (ADR-0003 §3
// as amended by ADR-0053). A third assertion stood here — "host should win over base" —
// and went with the level ADR-0053 removed; the host's own values are asserted by
// TestHostEnv_BareReferenceDoesNotReadTheInventory instead.
func TestMergeEnvPrecedence(t *testing.T) {
	base := map[string]string{"owner": "base", "keep": "1"}
	set := map[string]string{"owner": "set"}
	if got := mergeEnv(base, set)["owner"]; got != "set" {
		t.Fatalf("--set should win: %q", got)
	}
	if got := mergeEnv(base, nil)["keep"]; got != "1" {
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

// Per-host resolution: web1 gets alice, web2 gets bob, from one plan. #540 changed how it
// is written, not whether it works — the step used to carry a bare `Refs` entry, which no
// longer reads the inventory (ADR-0053). `${inventory.owner}` is what the parser emits for
// the prefixed form, so this now exercises the path a plan actually takes.
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
		{Instruction: "dir.owner", Args: map[string]string{"path": "/opt"}, Templates: map[string]string{"owner": "${inventory.owner}"}},
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

// #540: a bare reference and `${name}` are the same variable, so they must resolve to the
// same value. They did not: the bare form read the inventory, the braced form read the
// plan, and adding braces silently changed which machine a plan deployed to.
//
// Reproduces the table measured on the built binary (ADR-0053): a plan binding
// `domain = "FROM-PLAN"` against a host declaring `domain: "FROM-INVENTORY"`.
//
// The braced form is substituted at parse and never reaches HostEnv, so what is asserted
// here is the half that was wrong — a bare `domain` must now carry the plan's value, and
// the host's own value must be reachable only under the `inventory.` prefix.
func TestHostEnv_BareReferenceDoesNotReadTheInventory(t *testing.T) {
	planBinding := map[string]string{"domain": "FROM-PLAN"}
	host := inventory.Host{Address: "10.0.0.1", Vars: map[string]string{"domain": "FROM-INVENTORY"}}

	// An empty inventory: this test is about the one-segment form, not the cross-host
	// reads ADR-0054 added — those have their own case.
	env := HostEnv("box", host, inventory.Inventory{}, planBinding, nil)

	if got := env["domain"]; got != "FROM-PLAN" {
		t.Errorf("a bare reference must resolve against the plan, got %q", got)
	}
	if got := env[proto.InventoryPrefix+"domain"]; got != "FROM-INVENTORY" {
		t.Errorf("the host's own value must stay reachable under the prefix, got %q", got)
	}
}

// #547, ADR-0054: a plan reads another host's declared values, so an address that exists
// once in reality is written once in the inventory.
//
// The case that made this necessary: a service on one machine reaching a database on
// another. Before, `db`'s address had to be copied into `svc`'s entry — two copies of one
// fact, with nothing keeping them equal.
func TestHostEnv_ReadsAnotherHostsValues(t *testing.T) {
	inv := inventory.Inventory{
		// `defaults` are merged before the table is built, so a field means the same thing
		// whether a host reads it or its neighbour does (ADR-0054 §2).
		Defaults: inventory.Host{User: "deploy", Vars: map[string]string{"tier": "prod"}},
		Hosts: map[string]inventory.Host{
			"db":  {Address: "10.0.0.40", Key: "/secret/id_db", Vars: map[string]string{"engine": "postgres"}},
			"svc": {Address: "10.0.0.50"},
		},
		Groups: map[string][]string{"web": {"svc"}},
	}
	env := HostEnv("svc", mustResolve(t, inv, "svc"), inv, nil, nil)

	for name, want := range map[string]string{
		"inventory.address":     "10.0.0.50", // one segment still means *this* host
		"inventory.db.address":  "10.0.0.40",
		"inventory.db.name":     "db",
		"inventory.db.engine":   "postgres",
		"inventory.db.user":     "deploy", // from defaults, not db's literal block
		"inventory.db.tier":     "prod",
		"inventory.svc.address": "10.0.0.50",
	} {
		if got := env[name]; got != want {
			t.Errorf("${%s}: got %q, want %q", name, got, want)
		}
	}

	// `key` is refused at parse and absent from the table too, so a private key's path is
	// never one lookup away from a rendered file (ADR-0054 §3).
	if _, ok := env["inventory.db.key"]; ok {
		t.Error("a host's key must not be readable from another host")
	}

	// A group has no single address, so it is not a host and must not answer as one.
	if _, ok := env["inventory.web.address"]; ok {
		t.Error("a group name must not resolve as a host")
	}
}

func mustResolve(t *testing.T, inv inventory.Inventory, alias string) inventory.Host {
	t.Helper()
	h, ok := inv.Resolve(alias)
	if !ok {
		t.Fatalf("%s does not resolve", alias)
	}
	return h
}
