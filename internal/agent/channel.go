package agent

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"shellf/internal/engine"
	"shellf/internal/proto"
)

// SockName is the channel socket inside the agent workdir. Kept short on purpose: a
// Unix socket path is capped at ~108 bytes by the kernel, and the workdir is already
// `/dev/shm/shellf-<id>` — about 37 bytes with the socket, so there is room, but a
// longer layout would fail at bind() with an error that reads like nonsense.
const SockName = "sock"

// attachWait bounds how long a job waits for a bridge before failing. The control host
// opens it while the job is already running, so some wait is normal; failing eventually
// is what keeps a job from hanging on an answer nobody will give (ADR-0031 §2). A var so
// tests need not sit through it.
var attachWait = 30 * time.Second

// Channel is the agent's end of the control channel (ADR-0031). The agent is detached
// and holds no stream, so it listens here; the control host runs `shellf __bridge` in an
// SSH session, which copies that session to this socket.
//
// A dropped session kills the bridge, not the agent: the listener stays, the control
// host reconnects and a new bridge attaches. Only a job waiting on an answer at that
// moment fails — and it can, because a closed socket is reported where a named pipe
// would leave it blocked.
type Channel struct {
	ln net.Listener

	// workdir is the agent's own directory, where a transfer stages what it received
	// before the escalated commit places it (ADR-0044 §3). It belongs to this agent: a
	// staging area the escalated side could write would be a way to hand root a file
	// somebody else chose.
	workdir string

	// child runs one of the escalated verbs. It is a field for the same reason Executor is
	// an interface: a unit test cannot re-invoke the agent binary, since the binary running
	// a `go test` is the test binary and knows no `__sync-commit`. Production never
	// replaces it, and the real path — fork, escalate, place — is what the e2e harness
	// exercises against a container, which is the only place it can be proven.
	child func(ex engine.Executor, args ...string) (string, error)

	mu    sync.Mutex
	conn  *proto.Conn
	next  int
	ready chan struct{} // closed once a bridge has attached and been greeted
}

// Listen creates the channel socket in workdir. A stale socket from a previous agent is
// removed first: bind() refuses an existing path, and an agent that cannot listen would
// fail every request for a reason the operator cannot see.
func Listen(workdir string) (*Channel, error) {
	path := filepath.Join(workdir, SockName)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil { // same reach as the workdir (ADR-0018)
		_ = ln.Close()
		return nil, err
	}
	c := &Channel{ln: ln, workdir: workdir, child: childVerb, ready: make(chan struct{})}
	// Greet as soon as a bridge attaches, not at the first request. The control host
	// handshakes when it opens the bridge — if the agent only answered on demand, a run
	// that asks for nothing (a dry-run, or any plan without a primitive) would leave
	// both sides waiting for each other.
	go c.accept()
	return c, nil
}

// accept takes one bridge at a time, greets it, and keeps it until it goes. A dropped
// session is not fatal: the next bridge is accepted and the job continues.
func (c *Channel) accept() {
	for {
		cn, err := c.ln.Accept()
		if err != nil {
			return // listener closed: the agent is done
		}
		conn := proto.NewConn(cn)
		if err := conn.Handshake(); err != nil {
			_ = conn.Close()
			continue
		}
		c.mu.Lock()
		c.conn = conn
		select {
		case <-c.ready: // already armed by a previous bridge
		default:
			close(c.ready)
		}
		c.mu.Unlock()
		// Loop back to Accept: a reconnecting control host simply replaces the
		// connection, which is what makes a dropped session recoverable.
	}
}

func (c *Channel) Close() error { return c.ln.Close() }

// AskWith requests a resource from the control host and blocks until it answers.
//
// It returns an error when nobody is there: no bridge attached, or the session died while
// waiting. That error names the resource, because "the connection dropped" sends the
// operator looking at the target when the missing piece is on their own machine.
//
// It carries the variables in scope at the call site, which `file.render` needs
// so a `with { }` override reaches the renderer; `file.read` sends none. Both name a
// declared resource and send no content — the text a render substitutes is read on the
// control host, never submitted from here (#392, ADR-0042).
func (c *Channel) AskWith(resource string, payload []byte, vars map[string]string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Two attempts, because the connection this agent holds may belong to a session that
	// has already ended. A resident agent outlives the command that created it
	// (ADR-0005), so `shellf status` and the `shellf run` that follows are two different
	// bridges; the dead one reveals itself only on use, as an EOF while waiting for the
	// answer. Asking again, against the bridge that has since attached, is safe: the
	// channel is data-only (ADR-0031), so a second ask reads or renders again and
	// changes nothing on either side.
	b, err, stale := c.askOnce(resource, payload, vars)
	if stale {
		b, err, _ = c.askOnce(resource, payload, vars)
	}
	return b, err
}

// attached returns the live connection, waiting for a bridge if none has arrived yet.
// Assumes c.mu is held, and releases it around the wait so accept() can install one.
func (c *Channel) attached(resource string) (*proto.Conn, error) {
	if c.conn != nil {
		return c.conn, nil
	}
	// A bridge may still be attaching — the control host opens it while the job is
	// already running. Wait, but not forever: a job blocked on an answer nobody will
	// give must fail naming what it waited for (ADR-0031 §2).
	ready := c.ready
	c.mu.Unlock()
	select {
	case <-ready:
	case <-time.After(attachWait):
	}
	c.mu.Lock()
	if c.conn == nil {
		return nil, fmt.Errorf("%s: no control host attached", resource)
	}
	return c.conn, nil
}

// askOnce performs one exchange, assuming c.mu is held. Its third result says the failure
// was a connection that went away mid-ask — the one case worth retrying, as opposed to
// "nobody attached" or a refusal from the control host, which a second ask would only
// repeat.
func (c *Channel) askOnce(resource string, payload []byte, vars map[string]string) ([]byte, error, bool) {
	// The attach wait lives in attached(), which sync.go already uses. It used to be
	// open-coded here as well, and two copies of a lock/wait/relock sequence do not stay
	// identical: a timing fix applied to one would have left the other behind (#473).
	//
	// Its failure is deliberately not stale — nobody is attached, so a second ask would
	// only wait and fail again.
	if _, err := c.attached(resource); err != nil {
		return nil, err, false
	}

	c.next++
	id := fmt.Sprintf("%d", c.next)
	msg := proto.Msg{Kind: proto.KindAsk, ID: id, Resource: resource, Vars: vars}
	if payload != nil {
		msg.Data = base64.StdEncoding.EncodeToString(payload)
	}
	if err := c.conn.Send(msg); err != nil {
		c.drop()
		return nil, fmt.Errorf("%s: control host went away while asking: %v", resource, err), true
	}
	for {
		m, err := c.conn.Recv()
		if err != nil {
			c.drop()
			return nil, fmt.Errorf("%s: control host went away before answering: %v", resource, err), true
		}
		if m.Kind != proto.KindAnswer || m.ID != id {
			continue // not ours, or a kind this build does not know
		}
		if m.Error != "" {
			return nil, fmt.Errorf("%s: %s", resource, m.Error), false
		}
		b, err := base64.StdEncoding.DecodeString(m.Data)
		return b, err, false
	}
}

// drop forgets the current connection so the next Ask accepts a fresh bridge — this is
// what makes a dropped session recoverable rather than fatal to the agent.
func (c *Channel) drop() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	// Arm the wait again: the next Ask should block until a new bridge attaches, not
	// fail instantly on a channel that a previous connection already closed.
	select {
	case <-c.ready:
		c.ready = make(chan struct{})
	default:
	}
}
