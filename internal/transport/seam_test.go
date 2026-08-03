package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeConn drives the push/deposit/poll sequencing without any network (#116).
// It records every run/start command in order; responder decides each run's
// reply so a test can script "not cached", "agent dead", "job done", a drop, etc.
type fakeConn struct {
	runs      []string
	starts    []string
	closed    int
	responder func(cmd string) ([]byte, error)
}

func (f *fakeConn) run(cmd string, stdin []byte) ([]byte, error) {
	f.runs = append(f.runs, cmd)
	if f.responder != nil {
		return f.responder(cmd)
	}
	return nil, nil
}

func (f *fakeConn) start(cmd string) error { f.starts = append(f.starts, cmd); return nil }
func (f *fakeConn) close() error           { f.closed++; return nil }

func (f *fakeConn) ran(sub string) bool {
	for _, c := range f.runs {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// done is a responder that replies "not cached", "agent dead" and returns the
// job payload on the first checkDone — the nominal single-pass happy path.
func doneResponder(payload string) func(string) ([]byte, error) {
	return func(cmd string) ([]byte, error) {
		switch {
		case strings.HasPrefix(cmd, "test -x "): // cache probe → miss
			return nil, errFake("absent")
		case strings.Contains(cmd, "agent.pid"): // agentAlive → dead
			return nil, errFake("dead")
		case strings.HasPrefix(cmd, "if test -f "): // checkDone → ready
			return []byte(payload), nil
		}
		return nil, nil
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

// tmpBin writes a fake agent binary and returns its path (Run reads it to hash).
func tmpBin(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(p, []byte("BIN"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun_HappyPath_PushDepositLaunchPoll(t *testing.T) {
	fc := &fakeConn{responder: doneResponder(`{"ok":true}`)}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	out, err := s.Run(tmpBin(t), []byte(`{"steps":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("job payload should pass through: %q", out)
	}
	// Full sequence: cache probe → push → deposit → agentAlive → checkDone → rmJob.
	if !fc.ran("test -x ") || !fc.ran("chmod +x") || !fc.ran("mkdir -p ") ||
		!fc.ran("agent.pid") || !fc.ran("if test -f ") || !fc.ran("rm -f ") {
		t.Fatalf("missing a step in the sequence: %v", fc.runs)
	}
	if len(fc.starts) != 1 || !strings.Contains(fc.starts[0], "__agent-resident") {
		t.Fatalf("a dead agent must be launched once: %v", fc.starts)
	}
}

func TestRun_Cached_SkipsPush(t *testing.T) {
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		switch {
		case strings.HasPrefix(cmd, "test -x "): // cache probe → HIT
			return nil, nil
		case strings.Contains(cmd, "agent.pid"): // agentAlive → alive
			return nil, nil
		case strings.HasPrefix(cmd, "if test -f "):
			return []byte(`{"ok":true}`), nil
		}
		return nil, nil
	}}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	if _, err := s.Run(tmpBin(t), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if fc.ran("chmod +x") {
		t.Fatal("a cache hit must skip the push")
	}
	if len(fc.starts) != 0 {
		t.Fatalf("a live agent must not be relaunched: %v", fc.starts)
	}
}

func TestWorkBase_PrefersTmpfsElseTmp(t *testing.T) {
	// /dev/shm writable → workdir on tmpfs (ADR-0025).
	shm := &fakeConn{responder: func(cmd string) ([]byte, error) { return nil, nil }}
	if got := workBase(shm); got != "/dev/shm" {
		t.Fatalf("writable tmpfs should be chosen: %s", got)
	}
	if !shm.ran("test -w /dev/shm") {
		t.Fatalf("workBase must probe tmpfs: %v", shm.runs)
	}
	// no tmpfs → fall back to /tmp.
	notmp := &fakeConn{responder: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, "/dev/shm") {
			return nil, errFake("no shm")
		}
		return nil, nil
	}}
	if got := workBase(notmp); got != "/tmp" {
		t.Fatalf("absent tmpfs should fall back to /tmp: %s", got)
	}
}

func TestRun_PushError_StopsBeforeDeposit(t *testing.T) {
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		if strings.HasPrefix(cmd, "test -x ") {
			return nil, errFake("absent") // not cached
		}
		if strings.HasPrefix(cmd, "cat > ") {
			return nil, errFake("disk full") // push fails
		}
		return nil, nil
	}}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	if _, err := s.Run(tmpBin(t), []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "push agent") {
		t.Fatalf("push error should propagate: %v", err)
	}
	if fc.ran("mkdir -p ") {
		t.Fatal("a failed push must stop before depositing the job")
	}
	if fc.closed == 0 {
		t.Fatal("the connection must be closed on the error path")
	}
}

func TestPoll_DropThenRecover(t *testing.T) {
	restore := pollWait
	pollWait = time.Millisecond
	defer func() { pollWait = restore }()

	dead := &fakeConn{responder: func(string) ([]byte, error) { return nil, errFake("dropped") }}
	live := &fakeConn{responder: func(cmd string) ([]byte, error) {
		if strings.HasPrefix(cmd, "if test -f ") {
			return []byte(`{"ok":true}`), nil
		}
		return nil, nil
	}}
	conns := []conn{dead, live}
	s := SSH{dialFn: func() (conn, error) {
		c := conns[0]
		conns = conns[1:]
		return c, nil
	}}

	out, err := s.poll("/w", "7", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("poll must recover on the re-dialed conn: %q", out)
	}
	if dead.closed == 0 {
		t.Fatal("the dropped conn must be closed before re-dialing")
	}
	if !live.ran("rm -f ") {
		t.Fatal("a delivered job must be cleaned up (rmJob)")
	}
}

func TestPoll_Timeout(t *testing.T) {
	fc := &fakeConn{responder: func(string) ([]byte, error) { return []byte(notDone), nil }}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	_, err := s.poll("/w", "7", time.Now().Add(-time.Second)) // deadline already passed
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("an expired deadline must time out: %v", err)
	}
}

func TestClean_RunsCleanCmdAndCloses(t *testing.T) {
	fc := &fakeConn{}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	if err := s.Clean(); err != nil {
		t.Fatal(err)
	}
	if !fc.ran("/tmp/shellf-*") || fc.closed == 0 {
		t.Fatalf("Clean must run cleanCmd and close: runs=%v closed=%d", fc.runs, fc.closed)
	}
}

func TestRun_DialError_Propagates(t *testing.T) {
	s := SSH{dialFn: func() (conn, error) { return nil, errFake("no route") }}
	if _, err := s.Run(tmpBin(t), []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("a dial failure must surface: %v", err)
	}
}

func TestSigner(t *testing.T) {
	if _, err := (SSH{}).signer(); err == nil {
		t.Fatal("no key must error")
	}
	if _, err := (SSH{Key: "/does/not/exist"}).signer(); err == nil {
		t.Fatal("an unreadable key must error")
	}
	// A real ed25519 key round-trips through signer().
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (SSH{Key: path}).signer(); err != nil {
		t.Fatalf("a valid key must parse: %v", err)
	}
}

func TestHostKeyCallback(t *testing.T) {
	// --insecure yields a working (unverified) callback, no error.
	cb, err := (SSH{Insecure: true}).hostKeyCallback()
	if err != nil || cb == nil {
		t.Fatalf("insecure must give a callback: cb=%v err=%v", cb, err)
	}
	// A missing known_hosts file is a hard error (never silently trust).
	if _, err := (SSH{KnownHosts: "/does/not/exist"}).hostKeyCallback(); err == nil {
		t.Fatal("a missing known_hosts must error")
	}
}
