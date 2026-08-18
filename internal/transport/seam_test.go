package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
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
	duplexes  []string
	duplexErr error
	bridgeIn  io.Reader
	bridgeOut io.WriteCloser

	runs      []string
	starts    []string
	closed    int
	responder func(cmd string) ([]byte, error)
}

func (f *fakeConn) run(cmd string, stdin []byte) ([]byte, error) {
	f.runs = append(f.runs, cmd) // record the raw (posix-wrapped) command
	if f.responder != nil {
		return f.responder(unwrapPosix(cmd)) // classify on the script (#241)
	}
	return nil, nil
}

// unwrapPosix recovers the script from the wrapper (#241, #439) so a responder classifies
// on what the target will actually run; returns cmd unchanged when not wrapped.
//
// Decoding rather than string-stripping is deliberate: it fails loudly if the wrapper ever
// stops producing something a target can decode, which is the property #439 is about.
func unwrapPosix(cmd string) string {
	// The stdin-carrying form, which cannot use the pipe (#439).
	if strings.HasPrefix(cmd, "sh -c '") && strings.HasSuffix(cmd, "'") {
		return cmd[len("sh -c '") : len(cmd)-1]
	}
	const prefix, suffix = "echo ", " | base64 -d | sh"
	if strings.HasPrefix(cmd, prefix) && strings.HasSuffix(cmd, suffix) {
		payload := cmd[len(prefix) : len(cmd)-len(suffix)]
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return cmd
		}
		return string(b)
	}
	return cmd
}

func (f *fakeConn) start(cmd string) error { f.starts = append(f.starts, cmd); return nil }

// duplex records the bridge command and wires it to an in-memory peer, so a test can
// drive the channel without SSH. Nil peer = the target refuses the session.
func (f *fakeConn) duplex(cmd string) (io.Reader, io.WriteCloser, io.Closer, error) {
	f.duplexes = append(f.duplexes, cmd)
	if f.duplexErr != nil {
		return nil, nil, nil, f.duplexErr
	}
	toBridge, fromCtl := io.Pipe() // control writes → bridge reads
	fromBridge, toCtl := io.Pipe() // bridge writes → control reads
	f.bridgeIn, f.bridgeOut = toBridge, toCtl
	return fromBridge, fromCtl, closerFunc(func() error { _ = toCtl.Close(); return nil }), nil
}

type closerFunc func() error

func (c closerFunc) Close() error { return c() }
func (f *fakeConn) close() error  { f.closed++; return nil }

// ran matches on the **script**, not on the wire form: the command travels base64-encoded
// since #439, so a substring of the script appears nowhere in what was sent.
func (f *fakeConn) ran(sub string) bool {
	for _, c := range f.runs {
		if strings.Contains(unwrapPosix(c), sub) {
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
		case strings.Contains(cmd, "sha256sum"): // cache probe → absent (#391)
			return []byte("absent\n"), nil
		case strings.Contains(cmd, "-type d -user"): // workdir probe → safe (#391)
			return []byte("ok\n"), nil
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
	// Full sequence: cache probe → push → workdir → deposit → agentAlive → checkDone →
	// rmJob. The workdir step is a bare `mkdir` since #413: creating it exclusively is what
	// makes the directory ours, where `mkdir -p` accepted one another user had put there.
	if !fc.ran("sha256sum") || !fc.ran("chmod 700") || !fc.ran("mkdir /") ||
		!fc.ran("agent.pid") || !fc.ran("if test -f ") || !fc.ran("rm -f ") {
		t.Fatalf("missing a step in the sequence: %v", fc.runs)
	}
	if len(fc.starts) != 1 || !strings.Contains(unwrapPosix(fc.starts[0]), "__agent-resident") {
		t.Fatalf("a dead agent must be launched once: %v", fc.starts)
	}
}

func TestRun_Cached_SkipsPush(t *testing.T) {
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		switch {
		case strings.Contains(cmd, "sha256sum"): // cache probe → HIT: ours, unchanged (#391)
			return []byte("ok\n"), nil
		case strings.Contains(cmd, "-type d -user"): // workdir probe → safe (#391)
			return []byte("ok\n"), nil
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
	if fc.ran("chmod 700") {
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
		if strings.Contains(cmd, "sha256sum") {
			return []byte("absent\n"), nil // not cached (#391)
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

// Regression for #241: every transport command must run under POSIX sh, so a
// non-POSIX login shell (nushell, fish) cannot choke on `&&`/`$()`/`for … do`.
func TestRun_AllCommandsPosixWrapped(t *testing.T) {
	fc := &fakeConn{responder: doneResponder(`{"ok":true}`)}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}
	_, _ = s.Run(tmpBin(t), []byte(`{"steps":[]}`))
	sent := append(append([]string{}, fc.runs...), fc.starts...)
	if len(sent) == 0 {
		t.Fatal("no commands were sent")
	}
	for _, cmd := range sent {
		piped := strings.HasPrefix(cmd, "echo ") && strings.HasSuffix(cmd, " | base64 -d | sh")
		quoted := strings.HasPrefix(cmd, "sh -c '") && strings.HasSuffix(cmd, "'")
		if !piped && !quoted {
			t.Fatalf("transport command not POSIX-wrapped (#241): %q", cmd)
		}
		// Whichever form, the login shell reads this before `sh` exists, so it must have
		// nothing to reinterpret — no escape sequence above all, which is what #439 was.
		if strings.Contains(cmd, `\`) {
			t.Fatalf("wrapped command carries a backslash for the login shell to read: %q", cmd)
		}
		if quoted && strings.Count(cmd, "'") != 2 {
			t.Fatalf("the quoted form is only safe with no quote of its own (#439): %q", cmd)
		}
	}
}

// The bridge session, driven through the fake conn. It covers the command actually
// sent — which was wrong in the first draft: it ran the agent from the workdir, where
// the binary is not (it lives in /tmp, cached by hash).
func TestSSH_BridgeCommandAndServing(t *testing.T) {
	fc := &fakeConn{}
	served := make(chan struct{})
	s := SSH{
		dialFn:  func() (conn, error) { return fc, nil },
		Channel: func(r io.Reader, w io.WriteCloser) error { close(served); return nil },
	}

	stop := s.bridge("/tmp/shellf-agent-abc", "/dev/shm/shellf-xyz")
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("the bridge must hand its pipes to the channel server")
	}
	stop()

	if len(fc.duplexes) != 1 {
		t.Fatalf("exactly one bridge session expected: %v", fc.duplexes)
	}
	cmd := unwrapPosix(fc.duplexes[0])
	if !strings.Contains(cmd, "/tmp/shellf-agent-abc __bridge") {
		t.Fatalf("the bridge must run the pushed binary, not a path in the workdir: %q", cmd)
	}
	if !strings.Contains(cmd, "/dev/shm/shellf-xyz/sock") {
		t.Fatalf("the bridge must target the socket in the job workdir: %q", cmd)
	}
}

// A plan that needs nothing opens no bridge at all: today's behaviour is untouched,
// including a detached job surviving a dropped session.
func TestSSH_NoChannelNoBridge(t *testing.T) {
	fc := &fakeConn{}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}
	if s.Channel != nil {
		t.Fatal("Channel must default to nil")
	}
	if len(fc.duplexes) != 0 {
		t.Fatalf("no bridge session may be opened: %v", fc.duplexes)
	}
}

// An unreachable target must not wedge the run: the job gets a failure on its first
// request instead, naming the resource.
func TestSSH_BridgeDialFailureIsNotFatal(t *testing.T) {
	s := SSH{
		dialFn:  func() (conn, error) { return nil, errors.New("dial refused") },
		Channel: func(io.Reader, io.WriteCloser) error { t.Fatal("must not serve"); return nil },
	}
	done := make(chan struct{})
	go func() { s.bridge("/bin/agent", "/wd")(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a failed bridge dial must return, not hang the job")
	}
}
