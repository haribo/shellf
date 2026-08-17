package orchestrator

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shellf/internal/proto"
)

func chanPair() (*proto.Conn, *proto.Conn, func()) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return proto.NewConnRW(ar, aw), proto.NewConnRW(br, bw), func() { _ = aw.Close(); _ = bw.Close() }
}

// ask sends one request and returns the answer.
func ask(t *testing.T, allow *Allowed, resource string) proto.Msg {
	t.Helper()
	agent, control, done := chanPair()
	defer done()
	go func() { _ = Serve(control, allow) }()
	if err := agent.Send(proto.Msg{Kind: proto.KindAsk, ID: "1", Resource: resource}); err != nil {
		t.Fatal(err)
	}
	m, err := agent.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestServe_AnswersADeclaredResource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.j2"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := ask(t, NewAllowed(dir, []string{"file.read:conf.j2"}), "file.read:conf.j2")
	if m.Error != "" {
		t.Fatalf("a declared resource must be served: %s", m.Error)
	}
	b, _ := base64.StdEncoding.DecodeString(m.Data)
	if string(b) != "hello" {
		t.Fatalf("payload: got %q", b)
	}
}

// ADR-0031 §3, and the reason the channel is safe at all. Each of these is something an
// imported def could plausibly try.
func TestServe_RefusesAnythingNotDeclared(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.j2"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	allow := NewAllowed(dir, []string{"file.read:conf.j2"})

	for _, resource := range []string{
		"id_ed25519",            // a sibling file, never declared
		"../../../etc/passwd",   // climbing out
		"/etc/passwd",           // absolute
		"~/.ssh/id_ed25519",     // the case the ADR names
		"./conf.j2",             // another spelling of a declared file
		"conf.j2/../id_ed25519", // a declared prefix, then elsewhere
		"",                      // empty
	} {
		t.Run(resource, func(t *testing.T) {
			m := ask(t, allow, "file.read:"+resource)
			if m.Error == "" {
				t.Fatalf("must be refused, got %d bytes of payload", len(m.Data))
			}
			if m.Data != "" {
				t.Fatal("a refusal must carry no payload")
			}
			if !strings.Contains(m.Error, "refused") {
				t.Fatalf("the refusal must say so plainly: %q", m.Error)
			}
		})
	}
}

// A declared file that is missing is a different answer from a refused one: the
// operator must be able to tell "you did not declare this" from "you declared it but it
// is not there".
func TestServe_DeclaredButMissingIsNotARefusal(t *testing.T) {
	m := ask(t, NewAllowed(t.TempDir(), []string{"file.read:absent.j2"}), "file.read:absent.j2")
	if m.Error == "" {
		t.Fatal("a missing file must error")
	}
	if strings.Contains(m.Error, "refused") {
		t.Fatalf("a declared-but-missing file is not a refusal: %q", m.Error)
	}
	if !strings.Contains(m.Error, "no such file") {
		t.Fatalf("the error must say what happened: %q", m.Error)
	}
}

// The control host never runs anything a peer sends: a resource name is a lookup key.
func TestServe_ResourceNameIsNeverExecuted(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	m := ask(t, NewAllowed(dir, []string{"file.read:conf.j2"}), "file.read:$(touch "+marker+")")
	if m.Error == "" {
		t.Fatal("must be refused")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a resource name must never reach a shell on the control host")
	}
}

// Serve returns when the peer goes: a job is over, and its serving goroutine must not
// leak for the lifetime of the run.
func TestServe_ReturnsWhenThePeerGoes(t *testing.T) {
	agent, control, done := chanPair()
	errc := make(chan error, 1)
	go func() { errc <- Serve(control, NewAllowed(t.TempDir(), nil)) }()
	_ = agent.Send(proto.Msg{Kind: proto.KindHello, Version: proto.ChannelVersion}) // ignored kind
	done()
	select {
	case <-errc:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve must return once the peer is gone")
	}
}

// An absolute path declared by the plan is served as-is, not joined to the plan dir.
// ADR-0038 §3: a control-host path resolves under `assets/`, and one that lands outside
// it is refused. This test asserted the opposite — an absolute path anywhere on the disk
// resolved — which is the behaviour the layout removes: the answer to "where does this
// file come from" is one directory, not the whole filesystem.
//
// The test is on containment after resolution, not on how the path was written: an
// absolute path *inside* assets/ still works, since it names a file the project owns.
func TestAllowed_PathOutsideAssetsIsRefused(t *testing.T) {
	assets := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.conf")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := ask(t, NewAllowed(assets, []string{"file.read:" + outside}), "file.read:"+outside)
	if m.Error == "" {
		t.Fatal("a path outside assets/ must be refused, whatever the project declared")
	}

	inside := filepath.Join(assets, "conf.j2")
	if err := os.WriteFile(inside, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = ask(t, NewAllowed(assets, []string{"file.read:" + inside}), "file.read:"+inside)
	if m.Error != "" {
		t.Fatalf("an absolute path inside assets/ names a file the project owns: %s", m.Error)
	}
}

// The `../` form of the same escape: written relative, resolving outside.
func TestAllowed_ClimbingOutOfAssetsIsRefused(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.conf")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := ask(t, NewAllowed(assets, []string{"file.read:../secret.conf"}), "file.read:../secret.conf")
	if m.Error == "" {
		t.Fatal("`../` out of assets/ must be refused")
	}
}

// #334: rendering runs here, on the control host, because the host's variables live here
// and never travel. Since #392 the template is read here too — the agent names a declared
// file and gets the substituted result (ADR-0042 §1).
func TestServe_Render(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.j2"), []byte("host = @{who}"), 0o600); err != nil {
		t.Fatal(err)
	}
	allow := NewAllowed(dir, []string{"file.render:conf.j2"})
	allow.Render = func(content string, _ map[string]string) (string, error) {
		return strings.ReplaceAll(content, "@{who}", "web1"), nil
	}

	m := ask(t, allow, "file.render:conf.j2")
	if m.Error != "" {
		t.Fatalf("render must answer: %s", m.Error)
	}
	got, _ := base64.StdEncoding.DecodeString(m.Data)
	if string(got) != "host = web1" {
		t.Fatalf("got %q", got)
	}
}

// #334: an ask carries the scope of the call site, and Serve must hand it to the
// renderer. Dropping it here loses every `with { }` override, which the control host has
// no other way to learn. The scope is the one thing an ask still brings with it (#392).
func TestServe_RenderReceivesTheAskScope(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.j2"), []byte("host = @{who}"), 0o600); err != nil {
		t.Fatal(err)
	}
	allow := NewAllowed(dir, []string{"file.render:conf.j2"})
	allow.Render = func(content string, scope map[string]string) (string, error) {
		return strings.ReplaceAll(content, "@{who}", scope["who"]), nil
	}

	agent, control, done := chanPair()
	defer done()
	go func() { _ = Serve(control, allow) }()

	if err := agent.Send(proto.Msg{Kind: proto.KindAsk, ID: "1", Resource: "file.render:conf.j2",
		Vars: map[string]string{"who": "from-the-call-site"}}); err != nil {
		t.Fatal(err)
	}
	m, err := agent.Recv()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := base64.StdEncoding.DecodeString(m.Data)
	if string(got) != "host = from-the-call-site" {
		t.Fatalf("the ask's scope must reach the renderer, got %q", got)
	}
}

// A run whose plan never renders has no renderer; asking anyway must say so rather than
// answer empty content, which would deliver a blank file and report success.
func TestServe_RenderWithoutRendererFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.j2"), []byte("host = @{who}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Declared, so the refusal can only come from the missing renderer.
	m := ask(t, NewAllowed(dir, []string{"file.render:conf.j2"}), "file.render:conf.j2")
	if m.Error == "" {
		t.Fatal("a render with no renderer must fail, not answer empty")
	}
	if !strings.Contains(m.Error, "renderer") {
		t.Fatalf("the failure must name what is missing: %q", m.Error)
	}
}

// A renderer that fails — an undefined variable — surfaces as an error, so the job
// halts instead of writing a file with a hole in it.
func TestServe_RenderErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "conf.j2"), []byte("v = @{nope}"), 0o600); err != nil {
		t.Fatal(err)
	}
	allow := NewAllowed(dir, []string{"file.render:conf.j2"})
	allow.Render = func(string, map[string]string) (string, error) { return "", errors.New(`undefined variable "nope"`) }

	m := ask(t, allow, "file.render:conf.j2")
	if m.Error == "" || !strings.Contains(m.Error, "nope") {
		t.Fatalf("the failure must name the variable: %q", m.Error)
	}
}

// #392 — the regression test for the hole ADR-0042 closes. A render whose resource the
// plan never declared must be refused like every other ask. Before the fix the control
// host substituted whatever text the target sent, so an imported def asked for
// `@{db_password}` and was answered by the machine holding it.
func TestServe_RefusesAnUndeclaredRender(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.tmpl"), []byte("p = @{db_password}"), 0o600); err != nil {
		t.Fatal(err)
	}
	allow := NewAllowed(dir, []string{"file.render:conf.j2"})
	allow.Render = func(content string, _ map[string]string) (string, error) {
		return strings.ReplaceAll(content, "@{db_password}", "s3cr3t"), nil
	}

	m := ask(t, allow, "file.render:secret.tmpl")
	if m.Error == "" {
		t.Fatal("an undeclared template must be refused, not rendered")
	}
	if !strings.Contains(m.Error, "secret.tmpl") {
		t.Fatalf("the refusal must name the resource: %q", m.Error)
	}
	if strings.Contains(m.Data, base64.StdEncoding.EncodeToString([]byte("s3cr3t"))) {
		t.Fatal("a refused render must carry no substituted value")
	}
}

// The served half of the same question lives in TestServe_Render, which now reads a
// declared file: the two together say what the allow-list means for this primitive.
