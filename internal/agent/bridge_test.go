package agent

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shellf/internal/proto"
)

// A listener standing in for the detached agent: it answers one ask.
func fakeAgent(t *testing.T, sock string, answer string) net.Listener {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		ch := proto.NewConn(c)
		m, err := ch.Recv()
		if err != nil {
			return
		}
		_ = ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: m.ID, Data: answer})
	}()
	return l
}

func TestBridge_CarriesBothDirections(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	l := fakeAgent(t, sock, "cGF5bG9hZA==")
	defer func() { _ = l.Close() }()

	// The control side writes into the bridge's stdin and reads its stdout.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = Bridge(sock, inR, outW) }()

	ctl := proto.NewConnRW(outR, inW)
	if err := ctl.Send(proto.Msg{Kind: proto.KindAsk, ID: "7", Resource: "conf.j2"}); err != nil {
		t.Fatal(err)
	}
	got, err := ctl.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "7" || got.Data != "cGF5bG9hZA==" {
		t.Fatalf("the bridge must carry both ways intact: %+v", got)
	}
}

// The bridge exits when the agent closes its side. Session death itself needs no
// handling here: sshd kills the remote process when the session ends, which is what
// keeps a bridge from outliving it — the property ADR-0005's "no trace" depends on.
func TestBridge_EndsWhenTheAgentCloses(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		c, _ := l.Accept()
		if c != nil {
			_ = c.Close() // the job is over, the agent hangs up
		}
	}()

	inR, _ := io.Pipe()
	done := make(chan struct{})
	go func() { _ = Bridge(sock, inR, io.Discard); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the bridge must exit when the agent closes, not linger on the target")
	}
}

func TestBridge_MissingSocketIsAnError(t *testing.T) {
	old := dialWait
	dialWait = 100 * time.Millisecond
	defer func() { dialWait = old }()

	err := Bridge(filepath.Join(t.TempDir(), "absent"), os.Stdin, io.Discard)
	if err == nil {
		t.Fatal("dialing an absent socket must error")
	}
}
