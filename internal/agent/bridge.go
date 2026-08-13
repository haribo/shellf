package agent

import (
	"io"
	"net"
	"time"
)

// dialWait bounds how long Bridge retries reaching the agent's socket. A var so a test
// need not sit through it.
var dialWait = 5 * time.Second

// Bridge connects the control host to a detached agent (ADR-0031).
//
// The agent is launched with no streams (`setsid … >/dev/null 2>&1 </dev/null &`), which
// is what lets it survive a dropped session — and also what leaves it unable to speak.
// It therefore listens on a Unix socket in its workdir, and the control host opens an
// SSH session running `shellf __bridge <socket>`, whose stdin/stdout this function
// copies to and from that socket.
//
// The consequence that matters: a dropped session kills the bridge, not the job. The
// agent keeps its socket, the control host reconnects, relaunches a bridge, and the
// dialogue resumes. Only a job actually waiting on an answer when the session dies
// fails — and it can, because a socket reports the peer's departure where a named pipe
// would leave it blocked forever.
func Bridge(sockPath string, in io.Reader, out io.Writer) error {
	// The control host launches this as soon as it has started the agent, so the socket
	// may not exist yet: the agent creates it while this session is being opened. Retry
	// briefly rather than dying — a failed bridge means every `~file.read` in the job
	// fails, for a race measured in milliseconds.
	var c net.Conn
	var err error
	deadline := time.Now().Add(dialWait)
	for {
		if c, err = net.Dial("unix", sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c == nil {
		return err
	}
	defer func() { _ = c.Close() }()

	// End of input is not end of session: the control host may close stdin after
	// sending and still be waiting for the answer. So that direction half-closes and
	// the bridge keeps reading — otherwise the reply is dropped on the floor.
	go func() {
		_, _ = io.Copy(c, in)
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite() // tell the agent no more asks are coming
		}
	}()

	// The outbound copy is what ends the bridge: it returns when the agent closes its
	// side, or when writing to the session fails because the session is gone. Either
	// way the process exits — a bridge outliving its session would be the third
	// process on the target that breaks ADR-0005's "no trace".
	_, _ = io.Copy(out, c)
	return nil
}
