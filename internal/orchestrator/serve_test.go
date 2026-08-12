package orchestrator

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	m := ask(t, NewAllowed(dir, []string{"conf.j2"}), "conf.j2")
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
	allow := NewAllowed(dir, []string{"conf.j2"})

	for _, resource := range []string{
		"id_ed25519",           // a sibling file, never declared
		"../../../etc/passwd",  // climbing out
		"/etc/passwd",          // absolute
		"~/.ssh/id_ed25519",    // the case the ADR names
		"./conf.j2",            // another spelling of a declared file
		"conf.j2/../id_ed25519", // a declared prefix, then elsewhere
		"",                     // empty
	} {
		t.Run(resource, func(t *testing.T) {
			m := ask(t, allow, resource)
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
	m := ask(t, NewAllowed(t.TempDir(), []string{"absent.j2"}), "absent.j2")
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
	m := ask(t, NewAllowed(dir, []string{"conf.j2"}), "$(touch "+marker+")")
	if m.Error == "" {
		t.Fatal("must be refused")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a resource name must never reach a shell on the control host")
	}
}
