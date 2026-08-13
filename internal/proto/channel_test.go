package proto

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pipePair returns two endpoints wired to each other.
func pipePair() (*Conn, *Conn, func()) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	a := NewConnRW(ar, aw)
	b := NewConnRW(br, bw)
	return a, b, func() { _ = aw.Close(); _ = bw.Close() }
}

func TestChannel_RoundTrip(t *testing.T) {
	a, b, done := pipePair()
	defer done()

	go func() {
		m, err := b.Recv()
		if err != nil {
			return
		}
		_ = b.Send(Msg{Kind: KindAnswer, ID: m.ID, Data: "aGk="})
	}()

	if err := a.Send(Msg{Kind: KindAsk, ID: "1", Resource: "conf.j2"}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindAnswer || got.ID != "1" || got.Data != "aGk=" {
		t.Fatalf("answer must carry the ask's id and its payload: %+v", got)
	}
}

// The property that decided a socket over a named pipe (ADR-0031 §2): a waiting peer
// must learn that nobody will answer, so the job can fail naming its pending request
// instead of hanging.
func TestChannel_PeerGoneIsReported(t *testing.T) {
	a, _, done := pipePair()
	done() // the other side goes away

	if _, err := a.Recv(); err == nil {
		t.Fatal("a departed peer must surface as an error, not block")
	}
}

func TestChannel_HandshakeRejectsOtherVersion(t *testing.T) {
	a, b, done := pipePair()
	defer done()

	// io.Pipe is synchronous, so the peer must read before it writes or both block.
	go func() {
		if _, err := b.Recv(); err != nil {
			return
		}
		_ = b.Send(Msg{Kind: KindHello, Version: ChannelVersion + 1})
	}()

	err := a.Handshake()
	if err == nil {
		t.Fatal("a peer on another wire version must be refused")
	}
	// The message has to say what to do about it: a resident agent from an earlier
	// session is the expected cause, and it is fixed by replacing it.
	if !strings.Contains(err.Error(), "clean") {
		t.Fatalf("the error must tell the operator how to recover, got: %v", err)
	}
}

func TestChannel_MalformedLineIsAnError(t *testing.T) {
	c := NewConnRW(strings.NewReader("not json\n"), io.Discard)
	if _, err := c.Recv(); err == nil {
		t.Fatal("a malformed line must be an error, not a zero message")
	}
}

func TestChannel_HandshakeSucceedsOnSameVersion(t *testing.T) {
	a, b, done := pipePair()
	defer done()
	go func() {
		if _, err := b.Recv(); err != nil {
			return
		}
		_ = b.Send(Msg{Kind: KindHello, Version: ChannelVersion})
	}()
	if err := a.Handshake(); err != nil {
		t.Fatalf("matching versions must handshake: %v", err)
	}
}

// A peer that answers something other than a hello is refused: a stream that is not
// this protocol must fail at the greeting, not halfway through a job.
func TestChannel_HandshakeRejectsNonHello(t *testing.T) {
	a, b, done := pipePair()
	defer done()
	go func() {
		if _, err := b.Recv(); err != nil {
			return
		}
		_ = b.Send(Msg{Kind: KindAnswer, ID: "1"})
	}()
	if err := a.Handshake(); err == nil {
		t.Fatal("a non-hello greeting must be refused")
	}
}

// NewConn/Close over a real duplex stream, the shape the agent uses.
func TestChannel_ConnOverReadWriteCloser(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(shortTmp(t), "s"))
	if err != nil {
		t.Skip("no unix socket available")
	}
	defer func() { _ = l.Close() }()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		k := NewConn(c)
		m, err := k.Recv()
		if err != nil {
			return
		}
		_ = k.Send(Msg{Kind: KindAnswer, ID: m.ID, Data: "eA=="})
		_ = k.Close()
	}()

	c, err := net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	k := NewConn(c)
	if err := k.Send(Msg{Kind: KindAsk, ID: "9", Resource: "x"}); err != nil {
		t.Fatal(err)
	}
	m, err := k.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if m.Data != "eA==" {
		t.Fatalf("payload: %+v", m)
	}
	if err := k.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func shortTmp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/dev/shm", "sf")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
