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
	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, nil)

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
	reports := Run(plan, inv, "/bin/agent", "apply", dial, nil, nil, nil, nil)

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
	Run(plan, inv, "/bin/agent", "apply", dial, map[string]string{}, nil, nil, nil)

	if !strings.Contains(string(reqs["web1"]), `"owner":"alice"`) {
		t.Fatalf("web1 request should resolve owner=alice: %s", reqs["web1"])
	}
	if !strings.Contains(string(reqs["web2"]), `"owner":"bob"`) {
		t.Fatalf("web2 request should resolve owner=bob: %s", reqs["web2"])
	}
}

// ADR-0024: a template renders per host, over that host's env — each host's
// request carries its own file-write, and no `file.template` reaches the wire.
func TestRun_RendersTemplatesPerHost(t *testing.T) {
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
	render := func(src string, vars map[string]string) (string, error) { return "owner=" + vars["owner"], nil }

	plan := Plan{{Target: "web", Steps: []proto.Step{
		{Instruction: "file.template", Args: map[string]string{"src": "motd.tmpl", "dst": "/etc/motd"}},
	}}}
	Run(plan, inv, "/bin/agent", "apply", dial, map[string]string{}, nil, nil, render)

	if !strings.Contains(string(reqs["web1"]), `owner=alice`) || !strings.Contains(string(reqs["web2"]), `owner=bob`) {
		t.Fatalf("templates should render per host: web1=%s web2=%s", reqs["web1"], reqs["web2"])
	}
	if strings.Contains(string(reqs["web1"]), `"instruction":"file.template"`) {
		t.Fatalf("a template must be rewritten to file-write before the wire: %s", reqs["web1"])
	}
}

func TestRenderTemplates(t *testing.T) {
	echo := func(src string, vars map[string]string) (string, error) { return src + "|role=" + vars["role"], nil }
	env := map[string]string{"role": "web", "conf": "/etc/x"}

	// template → file.write(dst, rendered); dst may be a per-host ref
	out, err := renderTemplates([]proto.Step{
		{Instruction: "file.template", Args: map[string]string{"src": "f.tmpl", "dst": "/lit"}},
		{Instruction: "file.template", Args: map[string]string{"src": "g.tmpl"}, Refs: map[string]string{"dst": "conf"}},
	}, env, echo)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Instruction != "file.write" || out[0].Args["path"] != "/lit" || out[0].Args["content"] != "f.tmpl|role=web" {
		t.Fatalf("literal-dst rewrite: %+v", out[0])
	}
	if out[1].Args["path"] != "/etc/x" || out[1].Args["content"] != "g.tmpl|role=web" {
		t.Fatalf("ref-dst rewrite: %+v", out[1])
	}

	// `with` wins over env for that call (ADR-0022)
	ow, _ := renderTemplates([]proto.Step{
		{Instruction: "file.template", Args: map[string]string{"src": "f.tmpl", "dst": "/x"}, With: map[string]string{"role": "db"}},
	}, env, echo)
	if ow[0].Args["content"] != "f.tmpl|role=db" {
		t.Fatalf("with should override env: %+v", ow[0])
	}

	// error cases: src ref, undefined dst ref, nil renderer with a template
	for name, steps := range map[string][]proto.Step{
		"src-ref":   {{Instruction: "file.template", Refs: map[string]string{"src": "x"}}},
		"undef-dst": {{Instruction: "file.template", Args: map[string]string{"src": "f"}, Refs: map[string]string{"dst": "nope"}}},
	} {
		if _, err := renderTemplates(steps, env, echo); err == nil {
			t.Fatalf("%s should error", name)
		}
	}
	if _, err := renderTemplates([]proto.Step{{Instruction: "file.template", Args: map[string]string{"src": "f", "dst": "/x"}}}, env, nil); err == nil {
		t.Fatal("a template with a nil renderer should error")
	}

	// recursion into block; input steps are never mutated (shared across hosts)
	in := []proto.Step{{Block: []proto.Step{{Instruction: "file.template", Args: map[string]string{"src": "b.tmpl", "dst": "/b"}}}}}
	on, _ := renderTemplates(in, env, echo)
	if on[0].Block[0].Instruction != "file.write" || on[0].Block[0].Args["content"] != "b.tmpl|role=web" {
		t.Fatalf("nested template not rendered: %+v", on[0].Block[0])
	}
	if in[0].Block[0].Instruction != "file.template" {
		t.Fatal("input steps must not be mutated")
	}
}

// Regression for #246: rewriting template → file-write must keep the capture
// binding and `?`, so `s = file.template(...)` then `if s.changed` resolves.
func TestRenderTemplates_PreservesBindAndCaught(t *testing.T) {
	echo := func(src string, vars map[string]string) (string, error) { return "x", nil }
	out, err := renderTemplates([]proto.Step{
		{Instruction: "file.template", Args: map[string]string{"src": "f.tmpl", "dst": "/x"}, Bind: "s", Caught: true},
	}, map[string]string{}, echo)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Instruction != "file.write" {
		t.Fatalf("not rewritten: %+v", out[0])
	}
	if out[0].Bind != "s" {
		t.Fatalf("capture binding dropped (#246): Bind=%q", out[0].Bind)
	}
	if !out[0].Caught {
		t.Fatalf("`?` dropped (#246): Caught=%v", out[0].Caught)
	}
}

// Regression for #293: renderTemplates must rewrite a `file.template` wherever it can
// appear in the step tree. Only the `If.Cond` position was missed, so a
// `if file.template(...) { … }` reached the agent verbatim and died `err.agent`.
// The five other positions lock in current behavior: whichever construct is added
// to the language next, a walker that forgets a position fails here.
func TestRenderTemplates_EveryRecursivePosition(t *testing.T) {
	echo := func(src string, vars map[string]string) (string, error) { return "rendered:" + src, nil }
	tpl := func() proto.Step {
		return proto.Step{Instruction: "file.template", Args: map[string]string{"src": "f.tmpl", "dst": "/d"}}
	}

	cases := map[string]struct {
		in  proto.Step
		get func(proto.Step) proto.Step
	}{
		"sequence": {tpl(), func(s proto.Step) proto.Step { return s }},
		"block":    {proto.Step{Block: []proto.Step{tpl()}}, func(s proto.Step) proto.Step { return s.Block[0] }},
		"parallel": {proto.Step{Parallel: []proto.Step{tpl()}}, func(s proto.Step) proto.Step { return s.Parallel[0] }},
		"if-then": {proto.Step{If: &proto.IfBlock{CondRef: &proto.ResultRef{Name: "x"}, Then: []proto.Step{tpl()}}},
			func(s proto.Step) proto.Step { return s.If.Then[0] }},
		"if-else": {proto.Step{If: &proto.IfBlock{CondRef: &proto.ResultRef{Name: "x"}, Else: []proto.Step{tpl()}}},
			func(s proto.Step) proto.Step { return s.If.Else[0] }},
		"if-cond": {proto.Step{If: &proto.IfBlock{Cond: &proto.Step{Instruction: "file.template", Args: map[string]string{"src": "f.tmpl", "dst": "/d"}}}},
			func(s proto.Step) proto.Step { return *s.If.Cond }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := renderTemplates([]proto.Step{c.in}, map[string]string{}, echo)
			if err != nil {
				t.Fatal(err)
			}
			got := c.get(out[0])
			if got.Instruction != "file.write" || got.Args["content"] != "rendered:f.tmpl" {
				t.Fatalf("template not rendered in %s position: %+v", name, got)
			}
		})
	}
}
