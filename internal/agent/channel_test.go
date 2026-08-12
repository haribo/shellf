package agent

import (
	"os"
	"encoding/base64"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/proto"
)

// shortDir gives a workdir under /dev/shm: a Unix socket path is capped at ~108 bytes,
// and a t.TempDir() under a long CI path can exceed it — failing at bind() with an
// error that looks unrelated.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/dev/shm", "sf")
	if err != nil {
		d = t.TempDir() // no /dev/shm: fall back and hope the path is short
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(d) })
	}
	return d
}

// control attaches to the agent's socket and answers one ask.
func control(t *testing.T, sock, payload string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		ch := proto.NewConn(c)
		if err := ch.Handshake(); err != nil {
			return
		}
		for {
			m, err := ch.Recv()
			if err != nil {
				return
			}
			_ = ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: m.ID,
				Data: base64.StdEncoding.EncodeToString([]byte(payload))})
		}
	}()
	return c
}

func TestChannel_AskGetsAnAnswer(t *testing.T) {
	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()

	c := control(t, filepath.Join(wd, SockName), "rendered")
	defer func() { _ = c.Close() }()

	got, err := ch.Ask("conf.j2")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "rendered" {
		t.Fatalf("payload: got %q", got)
	}
}

// The property the whole arrangement exists for (ADR-0031 §2): a dropped session kills
// the bridge, not the job. The agent keeps its socket, a new bridge attaches, and the
// dialogue resumes.
func TestChannel_SurvivesADroppedSession(t *testing.T) {
	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()
	sock := filepath.Join(wd, SockName)

	c1 := control(t, sock, "first")
	if _, err := ch.Ask("a"); err != nil {
		t.Fatal(err)
	}
	_ = c1.Close() // the session drops

	// The next ask fails — it has nobody to answer it — and says which resource.
	_, err = ch.Ask("b")
	if err == nil {
		t.Fatal("an ask with no control host must fail, not hang")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Fatalf("the failure must name the pending resource: %v", err)
	}

	// A new bridge attaches and the agent is usable again: it never died.
	c2 := control(t, sock, "second")
	defer func() { _ = c2.Close() }()
	got, err := ch.Ask("c")
	if err != nil {
		t.Fatalf("the agent must be usable after a reconnect: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("payload after reconnect: got %q", got)
	}
}

// A refusal from the control host reaches the job as an error naming the resource.
func TestChannel_RefusalSurfaces(t *testing.T) {
	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()

	c, err := net.Dial("unix", filepath.Join(wd, SockName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	go func() {
		k := proto.NewConn(c)
		if err := k.Handshake(); err != nil {
			return
		}
		m, err := k.Recv()
		if err != nil {
			return
		}
		_ = k.Send(proto.Msg{Kind: proto.KindAnswer, ID: m.ID, Error: `refused: "secret" was not declared by the plan`})
	}()

	_, err = ch.Ask("secret")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refusal must surface: %v", err)
	}
}
