package agent

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	old := attachWait
	attachWait = 200 * time.Millisecond
	defer func() { attachWait = old }()

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

// A socket left by a previous agent must not block the new one: bind() refuses an
// existing path, and an agent that cannot listen would fail every request for a reason
// the operator cannot see.
func TestChannel_ReplacesAStaleSocket(t *testing.T) {
	wd := shortDir(t)
	first, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close() // the socket file stays behind

	second, err := Listen(wd)
	if err != nil {
		t.Fatalf("a stale socket must be replaced, not fatal: %v", err)
	}
	defer func() { _ = second.Close() }()

	fi, err := os.Stat(filepath.Join(wd, SockName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the socket must not be reachable by other users: %o", fi.Mode().Perm())
	}
}

// A peer speaking another wire version is refused at the handshake, and the failure
// names the resource so the operator knows which request died.
func TestChannel_AskFailsOnVersionSkew(t *testing.T) {
	old := attachWait
	attachWait = 200 * time.Millisecond
	defer func() { attachWait = old }()

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
		_, _ = k.Recv()
		_ = k.Send(proto.Msg{Kind: proto.KindHello, Version: proto.ChannelVersion + 99})
	}()

	_, err = ch.Ask("conf.j2")
	if err == nil {
		t.Fatal("a version skew must fail the ask")
	}
	if !strings.Contains(err.Error(), "conf.j2") {
		t.Fatalf("the failure must name the resource: %v", err)
	}
}

// #334: a resident agent outlives the command that created it (ADR-0005), so it can be
// holding the bridge of a session that has already ended — `shellf status` then
// `shellf run`. The dead connection looks alive until it is used, and then answers EOF.
// Observed as an intermittent `err.agent` in the SSH harness:
//
//	file.read:motd.tmpl: control host went away before answering: EOF
//
// The ask must survive it by asking the bridge that has since attached.
func TestChannel_AskRetriesAfterAStaleBridge(t *testing.T) {
	old := attachWait
	attachWait = 2 * time.Second
	defer func() { attachWait = old }()

	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()

	sock := filepath.Join(wd, SockName)
	// The first session attaches, then dies without the agent noticing.
	dead := control(t, sock, "never")
	waitAttached(t, ch)
	_ = dead.Close()

	// The next command opens its own bridge a moment later, as the control host does.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = control(t, sock, "rendered")
	}()

	got, err := ch.Ask("conf.j2")
	if err != nil {
		t.Fatalf("an ask must survive a bridge left over from a previous session: %v", err)
	}
	if string(got) != "rendered" {
		t.Fatalf("the answer must come from the live bridge, got %q", got)
	}
}

// waitAttached blocks until the channel has greeted a bridge, so a test can act on the
// connection the agent actually holds rather than on a race.
func waitAttached(t *testing.T, ch *Channel) {
	t.Helper()
	for i := 0; i < 200; i++ {
		ch.mu.Lock()
		attached := ch.conn != nil
		ch.mu.Unlock()
		if attached {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no bridge attached")
}
