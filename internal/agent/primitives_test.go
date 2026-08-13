package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// wire stands up the whole path: a listening agent, a bridge, and the control host
// serving what the plan declared. Returns the channel the agent evaluates against.
func wire(t *testing.T, planDir string, declared []string) *Channel {
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
		_ = orchestrator.Serve(c, orchestrator.NewAllowed(planDir, declared))
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
	ch := wire(t, planDir, []string{"file.read:" + filepath.Join(planDir, "conf.j2")})

	f := newComp()
	f.set(`printf '%s' "$out"`, 0)
	resp := serveCompCh(t, f, ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"deliver": `def deliver(src: str, port: str) { apply { out = %file.render(%file.read(src)) shell { printf '%s' "$out" } } }`,
		},
		Steps: []proto.Step{{Instruction: "deliver", Args: map[string]string{
			"src": filepath.Join(planDir, "conf.j2"), "port": "8080",
		}}},
	})
	if resp.Results[0].Category != "ok" {
		t.Fatalf("the primitive chain must run: %+v", resp.Results[0])
	}
	if got := f.envFor(`printf '%s' "$out"`)["out"]; got != "port = 8080" {
		t.Fatalf("read + render must yield the rendered template, got %q", got)
	}
}

// The allow-list holds at the language level too: a def asking for something the plan
// never declared is refused, and the failure names it.
func TestPrimitives_UndeclaredIsRefused(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "id_ed25519"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	ch := wire(t, planDir, []string{"file.read:" + filepath.Join(planDir, "conf.j2")})

	f := newComp()
	resp := serveCompCh(t, f, ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"steal": `def steal(src: str) { apply { x = %file.read(src) } }`,
		},
		Steps: []proto.Step{{Instruction: "steal", Args: map[string]string{
			"src": filepath.Join(planDir, "id_ed25519"),
		}}},
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
		Defs:  map[string]string{"r": `def r(src: str) { apply { x = %file.read(src) } }`},
		Steps: []proto.Step{{Instruction: "r", Args: map[string]string{"src": "conf.j2"}}},
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

// %file.render takes content, not a path — which is what lets a template whose source
// lives on the *target* be rendered. Neither Go transformation could do this.
func TestPrimitives_RenderContentFromTheTarget(t *testing.T) {
	f := newComp()
	f.set(`cat /etc/model.j2`, 0)
	f.stdout(`cat /etc/model.j2`, "host = @{h}")
	resp := serveCompCh(t, f, wire(t, t.TempDir(), nil), proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"r": `def r(h: str) { apply { out = %file.render(shell { cat /etc/model.j2 }) shell { echo "$out" } } }`,
		},
		Steps: []proto.Step{{Instruction: "r", Args: map[string]string{"h": "web1"}}},
	})
	if resp.Results[0].Category != "ok" {
		t.Fatalf("rendering target-side content must work: %+v", resp.Results[0])
	}
	if got := f.envFor(`echo "$out"`)["out"]; got != "host = web1" {
		t.Fatalf("got %q", got)
	}
}

// Bytes are opaque (ADR-0034 §4): interpolating them would turn binary into text and
// corrupt a delivered file without a word.
func TestPrimitives_BytesCannotBeInterpolated(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	ch := wire(t, planDir, []string{"file.read:" + filepath.Join(planDir, "logo.png")})

	resp := serveCompCh(t, newComp(), ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"b": `def b(src: str) { apply { raw = %file.read(src) shell { echo "${raw}" } } }`,
		},
		Steps: []proto.Step{{Instruction: "b", Args: map[string]string{
			"src": filepath.Join(planDir, "logo.png"),
		}}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("interpolating bytes must fail rather than mangle them: %+v", resp.Results[0])
	}
}

// A primitive takes exactly one argument; anything else is a mistake worth naming.
func TestPrimitives_ArityIsChecked(t *testing.T) {
	resp := serveCompCh(t, newComp(), wire(t, t.TempDir(), nil), proto.Request{
		Mode:  "apply",
		Defs:  map[string]string{"a": `def a() { apply { x = %file.read() } }`},
		Steps: []proto.Step{{Instruction: "a"}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("a primitive called with no argument must fail: %+v", resp.Results[0])
	}
}
