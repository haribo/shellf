package transport

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #573: the cadence used to be a flat second, so a job that finished in tens of
// milliseconds was not observed for a whole second — the control host was slower
// than the work. These guard the ramp that replaced it; do not relax the bound
// back to a second.

func TestNextPollWait_RampsFromFloorToCeiling(t *testing.T) {
	restore := func(w, m time.Duration) func() {
		return func() { pollWait, pollWaitMax = w, m }
	}(pollWait, pollWaitMax)
	defer restore()
	pollWait, pollWaitMax = 10*time.Millisecond, 40*time.Millisecond

	var got []time.Duration
	d := time.Duration(0)
	for range 5 {
		d = nextPollWait(d)
		got = append(got, d)
	}

	want := []time.Duration{10, 20, 40, 40, 40}
	for i, w := range want {
		if got[i] != w*time.Millisecond {
			t.Fatalf("wait %d = %s, want %s (ramp: %v)", i, got[i], w*time.Millisecond, got)
		}
	}
}

func TestNextPollWait_FloorAboveCeilingIsClamped(t *testing.T) {
	restore := func(w, m time.Duration) func() {
		return func() { pollWait, pollWaitMax = w, m }
	}(pollWait, pollWaitMax)
	defer restore()
	pollWait, pollWaitMax = time.Minute, time.Second

	if d := nextPollWait(0); d != time.Second {
		t.Fatalf("first wait = %s, want the ceiling %s", d, time.Second)
	}
}

// A job answered on the second ask must return in the ramp's first interval, not
// in a second — this is the regression #573 fixed, run at the shipped defaults.
func TestPoll_SecondAskIsNotBilledAWholeSecond(t *testing.T) {
	if pollWait >= 500*time.Millisecond {
		t.Fatalf("pollWait = %s: the first ask is billed a full second again (#573)", pollWait)
	}

	var asks atomic.Int32
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		if strings.HasPrefix(cmd, "if test -f ") {
			if asks.Add(1) == 1 {
				return []byte(notDone), nil
			}
			return []byte(`{"ok":true}`), nil
		}
		return nil, nil
	}}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	start := time.Now()
	out, err := s.poll("/w", "7", time.Now().Add(5*time.Second))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("poll returned %q", out)
	}
	if asks.Load() != 2 {
		t.Fatalf("job asked %d times, want 2", asks.Load())
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("poll took %s for one wait — the cadence is back to a second (#573)", elapsed)
	}
}
