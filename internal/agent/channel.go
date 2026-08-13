package agent

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"shellf/internal/proto"
)

// SockName is the channel socket inside the agent workdir. Kept short on purpose: a
// Unix socket path is capped at ~108 bytes by the kernel, and the workdir is already
// `/dev/shm/shellf-<id>` — about 37 bytes with the socket, so there is room, but a
// longer layout would fail at bind() with an error that reads like nonsense.
const SockName = "sock"

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

	mu   sync.Mutex
	conn *proto.Conn
	next int
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
	return &Channel{ln: ln}, nil
}

func (c *Channel) Close() error { return c.ln.Close() }

// Ask requests a resource from the control host and blocks until it answers.
//
// It returns an error when nobody is there: no bridge attached, or the session died
// while waiting. That error names the resource, because "the connection dropped" sends
// the operator looking at the target when the missing piece is on their own machine.
func (c *Channel) Ask(resource string) ([]byte, error) { return c.AskWith(resource, nil) }

// AskWith is Ask with an input for the primitive — `file.render` sends the content to
// substitute, where `file.read` sends nothing and names a path.
func (c *Channel) AskWith(resource string, payload []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		cn, err := c.ln.Accept()
		if err != nil {
			return nil, fmt.Errorf("%s: no control host attached: %v", resource, err)
		}
		c.conn = proto.NewConn(cn)
		if err := c.conn.Handshake(); err != nil {
			c.drop()
			return nil, fmt.Errorf("%s: %v", resource, err)
		}
	}

	c.next++
	id := fmt.Sprintf("%d", c.next)
	msg := proto.Msg{Kind: proto.KindAsk, ID: id, Resource: resource}
	if payload != nil {
		msg.Data = base64.StdEncoding.EncodeToString(payload)
	}
	if err := c.conn.Send(msg); err != nil {
		c.drop()
		return nil, fmt.Errorf("%s: control host went away while asking: %v", resource, err)
	}
	for {
		m, err := c.conn.Recv()
		if err != nil {
			c.drop()
			return nil, fmt.Errorf("%s: control host went away before answering: %v", resource, err)
		}
		if m.Kind != proto.KindAnswer || m.ID != id {
			continue // not ours, or a kind this build does not know
		}
		if m.Error != "" {
			return nil, fmt.Errorf("%s: %s", resource, m.Error)
		}
		return base64.StdEncoding.DecodeString(m.Data)
	}
}

// drop forgets the current connection so the next Ask accepts a fresh bridge — this is
// what makes a dropped session recoverable rather than fatal to the agent.
func (c *Channel) drop() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}
