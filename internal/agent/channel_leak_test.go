package agent

import (
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"shellf/internal/proto"
)

// attach dials the agent's socket and completes the handshake, handing back both ends of
// the client side: the raw conn (for a read deadline) and the framed one. Unlike
// `control`, it starts no reader goroutine — this test needs to observe what the agent
// does to the connection, which a goroutine consuming it would hide.
func attach(t *testing.T, sock string) (net.Conn, *proto.Conn) {
	t.Helper()
	raw, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	pc := proto.NewConn(raw)
	if err := pc.Handshake(); err != nil {
		t.Fatal(err)
	}
	return raw, pc
}

// waitConn blocks until the channel holds a connection other than `not`.
func waitConn(t *testing.T, c *Channel, not *proto.Conn) *proto.Conn {
	t.Helper()
	for i := 0; i < 200; i++ {
		c.mu.Lock()
		cur := c.conn
		c.mu.Unlock()
		if cur != nil && cur != not {
			return cur
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no bridge attached in time")
	return nil
}

// A reconnecting control host replaces the agent's connection. The replaced one must be
// closed, or the agent leaks a descriptor per reconnection — and it lives up to two hours
// (ADR-0005), so the ceiling is the number of runs in that window (#638).
//
// `drop()` closes, but only when an ask discovers the connection is dead. A run that asks
// the control host for nothing between two bridges never takes that path, which is why
// this went unseen.
func TestChannel_ReplacedBridgeIsClosed(t *testing.T) {
	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()
	sock := filepath.Join(wd, SockName)

	raw1, pc1 := attach(t, sock)
	defer func() { _ = raw1.Close() }()
	first := waitConn(t, ch, nil)

	// A second bridge attaches without the first having failed an ask.
	raw2, _ := attach(t, sock)
	defer func() { _ = raw2.Close() }()
	waitConn(t, ch, first)

	// The agent's end of the first bridge must be gone: reading from the client end
	// returns EOF rather than blocking.
	_ = raw1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := pc1.Recv(); err == nil {
		t.Fatal("the replaced bridge answered: it was not closed")
	} else {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("the replaced connection is still open — accept() overwrote c.conn without closing it (#638)")
		}
	}
}
