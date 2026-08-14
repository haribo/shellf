package transport

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// #347: ADR-0031 §2 says a dropped session kills the bridge, not the job — "the control
// host reconnects, relaunches the bridge, and the dialogue resumes". It did not: bridge()
// dialled once, and when serving returned the goroutine ended. Every remaining ask in
// that job then failed, which is the failure the socket was chosen to survive.
//
// Serving returning is the observable signal that the bridge is gone, so that is what
// this drives.
func TestSSH_BridgeRelaunchesAfterADrop(t *testing.T) {
	restore := shrinkBridgeWait(t)
	defer restore()

	var served atomic.Int32
	twice := make(chan struct{})
	s := SSH{
		dialFn: func() (conn, error) { return &fakeConn{}, nil },
		Channel: func(io.Reader, io.WriteCloser) error {
			if served.Add(1) == 2 {
				close(twice)
			}
			return nil // the session dropped
		},
	}

	stop := s.bridge("/tmp/shellf-agent-abc", "/dev/shm/shellf-xyz")
	select {
	case <-twice:
	case <-time.After(2 * time.Second):
		t.Fatalf("a dropped bridge must be relaunched: served %d time(s)", served.Load())
	}
	stop()
}

// The other half, and the one a naive loop gets wrong: stop() closes the session too, so
// the shutdown must not read as a drop. Reopening a session on a host we are done with
// would leave a `shellf __bridge` behind on every run.
func TestSSH_BridgeDoesNotRelaunchAfterStop(t *testing.T) {
	restore := shrinkBridgeWait(t)
	defer restore()

	var served atomic.Int32
	first := make(chan struct{})
	var once bool
	s := SSH{
		dialFn: func() (conn, error) { return &fakeConn{}, nil },
		Channel: func(io.Reader, io.WriteCloser) error {
			served.Add(1)
			if !once {
				once = true
				close(first)
			}
			// Hold the session open until stop() closes it, as a real bridge does.
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}

	stop := s.bridge("/tmp/shellf-agent-abc", "/dev/shm/shellf-xyz")
	<-first
	stop()

	// Asserted after stop() returns, and on the total: stop() waits for the serving
	// goroutine, so by then every reconnection it was going to make has been made.
	// Sampling the counter before and after a sleep would prove nothing — the first
	// draft of this test did exactly that and survived the guard being deleted.
	if got := served.Load(); got != 1 {
		t.Fatalf("stop() must end the bridge, not read as a drop: served %d time(s), want 1", got)
	}
}

// shrinkBridgeWait makes the retry cadence test-sized. A test must not sit through the
// production backoff, and a test that shortens it globally must put it back.
func shrinkBridgeWait(t *testing.T) func() {
	t.Helper()
	oldWait, oldRetries := bridgeRetryWait, bridgeRetries
	bridgeRetryWait = 5 * time.Millisecond
	bridgeRetries = 20
	return func() { bridgeRetryWait, bridgeRetries = oldWait, oldRetries }
}
