package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/lang"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// wire stands up the whole path: a listening agent, a bridge, and the control host
// serving what the plan declared. Returns the channel the agent evaluates against.
func wire(t *testing.T, planDir string, declared []string, hostVars map[string]string) *Channel {
	t.Helper()
	wd, err := os.MkdirTemp("/dev/shm", "sf")
	if err != nil {
		t.Skip("no /dev/shm: a unix socket path would risk the ~108 byte cap")
	}
	t.Cleanup(func() { _ = os.RemoveAll(wd) })

	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	ctlR, brW := io.Pipe()
	brR, ctlW := io.Pipe()
	go func() { _ = Bridge(filepath.Join(wd, SockName), brR, brW) }()
	go func() {
		c := proto.NewConnRW(ctlR, ctlW)
		if err := c.Handshake(); err != nil {
			return
		}
		allow := orchestrator.NewAllowed(planDir, declared)
		// Rendering happens on the control host, over this host's variables.
		allow.Render = func(content string, _ map[string]string) (string, error) {
			return lang.Template(content, func(n string) (string, bool) {
				v, ok := hostVars[n]
				return v, ok
			})
		}
		_ = orchestrator.Serve(c, allow)
	}()
	return ch
}

// #317 end to end: a def reads a file from the control host and writes it on the
// target — which is what `file.template` becomes once it is an ordinary def.
func TestPrimitives_ReadReachesTheControlHost(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "conf.j2"), []byte("port = @{port}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := wire(t, planDir, []string{"file.render:" + filepath.Join(planDir, "conf.j2")}, map[string]string{"port": "8080"})

	f := newComp()
	f.set(`printf '%s' "$out"`, 0)
	resp := serveCompCh(t, f, ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"deliver": `def deliver(src: str, port: str) { apply { out = ~file.render(src) shell { printf '%s' "$out" } return ok.done } }`,
		},
		Steps: []proto.Step{{Instruction: "deliver", Args: map[string]string{
			"src": filepath.Join(planDir, "conf.j2"), "port": "8080",
		}, Control: []string{"src"}}}, // the plan wrote %"conf.j2"
	})
	if resp.Results[0].Category != "ok" {
		t.Fatalf("the primitive chain must run: %+v", resp.Results[0])
	}
	if got := f.envFor(`printf '%s' "$out"`)["out"]; got != "port = 8080" {
		t.Fatalf("the declared template must come back rendered, got %q", got)
	}
}

// The allow-list holds at the language level too: a def asking for something the plan
// never declared is refused, and the failure names it.
func TestPrimitives_UndeclaredIsRefused(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "id_ed25519"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := wire(t, planDir, []string{"file.read:" + filepath.Join(planDir, "conf.j2")}, nil)

	f := newComp()
	resp := serveCompCh(t, f, ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"steal": `def steal(src: str) { apply { x = ~file.read(src) return ok.done } }`,
		},
		Steps: []proto.Step{{Instruction: "steal", Args: map[string]string{
			"src": filepath.Join(planDir, "id_ed25519"),
		}, Control: []string{"src"}}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("an undeclared resource must fail the step: %+v", resp.Results[0])
	}
	msg := ""
	if resp.Results[0].Shell != nil {
		msg = resp.Results[0].Shell.Stderr
	}
	if !strings.Contains(msg, "refused") {
		t.Fatalf("the failure must say it was refused: %q", msg)
	}
}

// With no channel at all, a `%` fails naming the resource rather than yielding empty
// content — which would deliver a truncated file and report success.
func TestPrimitives_NoChannelFailsLoudly(t *testing.T) {
	f := newComp()
	resp := serveComp(t, f, proto.Request{
		Mode:  "apply",
		Defs:  map[string]string{"r": `def r(src: str) { apply { x = ~file.read(src) return ok.done } }`},
		Steps: []proto.Step{{Instruction: "r", Args: map[string]string{"src": "conf.j2"}, Control: []string{"src"}}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("a %% with no channel must fail: %+v", resp.Results[0])
	}
}

// serveCompCh is serveComp with a channel attached, as the resident agent has.
func serveCompCh(t *testing.T, c *compExec, ch *Channel, req proto.Request) proto.Response {
	t.Helper()
	return runRequest(req, c, ch)
}

// TestPrimitives_RenderContentFromTheTarget stood here: `~file.render(shell { cat … })`
// rendered a template living on the target. That case is gone with the content form of
// the primitive — text the target composed was substituted over the control host's
// variables, secrets included, with no allow-list entry to refuse it (#392). ADR-0042
// records that it returns as its own decision if it is ever wanted, with a bound on the
// text it accepts.

// Bytes are opaque (ADR-0034 §4): interpolating them would turn binary into text and
// corrupt a delivered file without a word.
func TestPrimitives_BytesCannotBeInterpolated(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	ch := wire(t, planDir, []string{"file.read:" + filepath.Join(planDir, "logo.png")}, nil)

	resp := serveCompCh(t, newComp(), ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"b": `def b(src: str) { apply { raw = ~file.read(src) shell { echo "${raw}" } return ok.done } }`,
		},
		Steps: []proto.Step{{Instruction: "b", Args: map[string]string{
			"src": filepath.Join(planDir, "logo.png"),
		}, Control: []string{"src"}}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("interpolating bytes must fail rather than mangle them: %+v", resp.Results[0])
	}
}

// A primitive takes exactly one argument; anything else is a mistake worth naming.
func TestPrimitives_ArityIsChecked(t *testing.T) {
	resp := serveCompCh(t, newComp(), wire(t, t.TempDir(), nil, map[string]string{"h": "web1"}), proto.Request{
		Mode:  "apply",
		Defs:  map[string]string{"a": `def a() { apply { x = ~file.read() return ok.done } }`},
		Steps: []proto.Step{{Instruction: "a"}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("a primitive called with no argument must fail: %+v", resp.Results[0])
	}
}
