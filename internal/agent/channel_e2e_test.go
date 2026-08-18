package agent

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// The whole arrangement, end to end and in-process: a detached-style agent listening on
// its socket, a bridge copying to it, and the control host serving only what the plan
// declared. Each piece is tested on its own elsewhere; this asserts they fit.
func TestChannelEndToEnd(t *testing.T) {
	wd, err := os.MkdirTemp("/dev/shm", "sf")
	if err != nil {
		t.Skip("no /dev/shm: a unix socket path would risk the ~108 byte cap")
	}
	defer func() { _ = os.RemoveAll(wd) }()

	// The plan declared conf.j2 and nothing else.
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "conf.j2"), []byte("port = 8080"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "id_ed25519"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	allow := orchestrator.NewAllowed(planDir, []string{"file.read:conf.j2"})

	// Agent side: listen, as ServeResident does.
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()

	// Control side: a bridge over in-memory pipes standing in for the SSH session,
	// with the orchestrator serving on the far end.
	ctlR, bridgeW := io.Pipe()
	bridgeR, ctlW := io.Pipe()
	go func() { _ = Bridge(filepath.Join(wd, SockName), bridgeR, bridgeW) }()
	go func() {
		c := proto.NewConnRW(ctlR, ctlW)
		if err := c.Handshake(); err != nil {
			return
		}
		_ = orchestrator.Serve(c, allow)
	}()

	// A declared resource comes back.
	got, err := ch.AskWith("file.read:conf.j2", nil, nil)
	if err != nil {
		t.Fatalf("a declared resource must reach the job: %v", err)
	}
	if string(got) != "port = 8080" {
		t.Fatalf("payload: got %q", got)
	}

	// An undeclared one does not — this is the whole point of the allow-list.
	if _, err := ch.AskWith("file.read:id_ed25519", nil, nil); err == nil {
		t.Fatal("an undeclared resource must be refused end to end")
	}
}
