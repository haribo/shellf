package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/proto"
)

// compExec is a local executor: it records every script with the environment it was
// given, which is what §1 (scope) needs to assert. Defined here rather than extending
// the shared fake, so no existing test is touched.
type compExec struct {
	mu    sync.Mutex
	exits map[string]int
	seen  []struct {
		script string
		env    engine.Env
	}
}

func newComp() *compExec { return &compExec{exits: map[string]int{}} }

func (c *compExec) Shell(script string, env engine.Env) engine.ShellResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := engine.Env{}
	for k, v := range env {
		cp[k] = v
	}
	c.seen = append(c.seen, struct {
		script string
		env    engine.Env
	}{script, cp})
	return engine.ShellResult{Exit: c.exits[script]}
}

func (c *compExec) As(string) engine.Executor    { return c }
func (c *compExec) Using(string) engine.Executor { return c }

func (c *compExec) set(script string, exit int) { c.exits[script] = exit }

func (c *compExec) called(script string) bool {
	for _, s := range c.seen {
		if s.script == script {
			return true
		}
	}
	return false
}

func (c *compExec) envFor(script string) engine.Env {
	for _, s := range c.seen {
		if s.script == script {
			return s.env
		}
	}
	return nil
}

func serveComp(t *testing.T, c *compExec, req proto.Request) proto.Response {
	t.Helper()
	body, _ := json.Marshal(req)
	var out bytes.Buffer
	if err := Serve(bytes.NewReader(body), &out, c); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// #296 / ADR-0030: a def may call another def. One test per rule the ADR settles,
// because each has a distinct failure mode and four of them fail silently.
func TestComposition(t *testing.T) {
	t.Run("resolves and runs the callee", func(t *testing.T) {
		f := newComp()
		f.set(`touch "$path"`, 0)
		resp := serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"leaf":   `def leaf(path: str) { apply { shell { touch "$path" } } }`,
				"caller": `def caller(p: str) { apply { leaf(p) } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"p": "/tmp/x"}}},
		})
		if resp.Results[0].Category != "ok" {
			t.Fatalf("call should run: %+v", resp.Results[0])
		}
		if !f.called(`touch "$path"`) {
			t.Fatal("the callee's shell must run")
		}
	})

	// §1 — the callee sees its arguments only. `secret` exists in the caller and must
	// not leak into the callee's shell environment.
	t.Run("callee sees its arguments only", func(t *testing.T) {
		f := newComp()
		f.set(`echo "$mine" "$secret"`, 0)
		serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"leaf":   `def leaf(mine: str) { apply { shell { echo "$mine" "$secret" } } }`,
				"caller": `def caller(x: str) { apply { secret = "leaked" leaf(x) } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"x": "ok"}}},
		})
		env := f.envFor(`echo "$mine" "$secret"`)
		if env["mine"] != "ok" {
			t.Fatalf("the callee must receive its own argument: %+v", env)
		}
		if _, leaked := env["secret"]; leaked {
			t.Fatalf("a caller's let must not reach the callee (ADR-0030 §1): %+v", env)
		}
	})

	// §3 — the caller is `changed` only when something actually acted. Both directions
	// matter and both fail silently: losing the flag stops every
	// `if x.changed { restart }` downstream; inventing it fires them all for nothing.
	t.Run("changed does not appear when the callee was already converged", func(t *testing.T) {
		f := newComp()
		f.set(`test -f "$path"`, 0) // observe: already in the desired state
		resp := serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"leaf":   `def leaf(path: str) { observe { return state(there: shell { test -f "$path" }.exit == 0) } apply { shell { touch "$path" } } }`,
				"caller": `def caller(p: str) { apply { leaf(p) } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"p": "/tmp/x"}}},
		})
		if resp.Results[0].Category != "ok" {
			t.Fatalf("should succeed: %+v", resp.Results[0])
		}
		if resp.Results[0].Changed {
			t.Fatalf("nothing acted, so the caller must not report changed: %+v", resp.Results[0])
		}
	})

	t.Run("changed propagates from the callee", func(t *testing.T) {
		f := newComp()
		f.set(`touch "$path"`, 0)
		resp := serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"leaf":   `def leaf(path: str) { apply { shell { touch "$path" } return ok.written } }`,
				"caller": `def caller(p: str) { apply { leaf(p) } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"p": "/tmp/x"}}},
		})
		if !resp.Results[0].Changed {
			t.Fatalf("a caller whose callee acted must report changed (ADR-0030 §3): %+v", resp.Results[0])
		}
	})

	// §4 — an err from the callee halts the caller.
	t.Run("callee error halts the caller", func(t *testing.T) {
		f := newComp()
		f.set(`false`, 1)
		resp := serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"leaf":   `def leaf() { apply { r = shell { false } if !r { return err.runtime(r) } } }`,
				"caller": `def caller() { apply { leaf() shell { touch "/tmp/after" } } }`,
			},
			Steps: []proto.Step{{Instruction: "caller"}},
		})
		if resp.Results[0].Category != "err" {
			t.Fatalf("a callee err must surface on the caller (ADR-0030 §4): %+v", resp.Results[0])
		}
		if f.called(`touch "/tmp/after"`) {
			t.Fatal("nothing after the failing call may run")
		}
	})

	// §5 — in check mode a callee reached from `apply` must not act.
	t.Run("check does not run a callee reached from apply", func(t *testing.T) {
		f := newComp()
		f.set(`touch "$path"`, 0)
		serveComp(t, f, proto.Request{
			Mode: "check",
			Defs: map[string]string{
				"leaf":   `def leaf(path: str) { apply { shell { touch "$path" } } }`,
				"caller": `def caller(p: str) { apply { leaf(p) } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"p": "/tmp/x"}}},
		})
		if f.called(`touch "$path"`) {
			t.Fatal("check must not run an effectful callee (ADR-0030 §5)")
		}
	})

	// §6 — a cycle is refused, and the message names the chain.
	t.Run("cycle is refused with its chain", func(t *testing.T) {
		f := newComp()
		resp := serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"a": `def a() { apply { b() } }`,
				"b": `def b() { apply { a() } }`,
			},
			Steps: []proto.Step{{Instruction: "a"}},
		})
		if resp.Results[0].Category != "err" {
			t.Fatalf("a cycle must fail: %+v", resp.Results[0])
		}
		msg := resp.Results[0].Tag + " " + resp.Error
		if resp.Results[0].Shell != nil {
			msg += " " + resp.Results[0].Shell.Stderr
		}
		if !strings.Contains(msg, "cycle") {
			t.Fatalf("the error must name the cycle, got: %+v / %q", resp.Results[0], resp.Error)
		}
	})
	// Found by running the real thing, not by the unit tests above: a call inside a def
	// went through a different expression parser, where `file.write` read as a field
	// access on `file` rather than a qualified instruction name.
	t.Run("qualified callee name parses inside a def", func(t *testing.T) {
		f := newComp()
		f.set(`printf '%s' "$content" > "$tmp"`, 0)
		resp := serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"caller": `def caller(p: str) { apply { file.write(p, "x") } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"p": "/tmp/x"}}},
		})
		if resp.Results[0].Category == "err" {
			t.Fatalf("a qualified callee must parse and resolve: %+v", resp.Results[0])
		}
	})

	// Also found by running it: `${…}` in a call argument was passed through verbatim,
	// so a file was delivered under the literal name `${name}`. A plan interpolates at
	// parse time against globals; inside a def the values exist only at evaluation.
	t.Run("interpolation applies to call arguments", func(t *testing.T) {
		f := newComp()
		f.set(`echo "$got"`, 0)
		serveComp(t, f, proto.Request{
			Mode: "apply",
			Defs: map[string]string{
				"leaf":   `def leaf(got: str) { apply { shell { echo "$got" } } }`,
				"caller": `def caller(name: str) { apply { leaf("/etc/${name}.conf") } }`,
			},
			Steps: []proto.Step{{Instruction: "caller", Args: map[string]string{"name": "app"}}},
		})
		if got := f.envFor(`echo "$got"`)["got"]; got != "/etc/app.conf" {
			t.Fatalf("interpolation must resolve against the def's scope, got %q", got)
		}
	})
}
