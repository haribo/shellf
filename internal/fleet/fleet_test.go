package fleet

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"shellf/internal/transport"
)

// fakeTransport returns a canned agent-response body per target.
type fakeTransport struct{ body []byte }

func (f fakeTransport) Run(_ string, _ []byte) ([]byte, error) { return f.body, nil }

var _ transport.Transport = fakeTransport{}

func TestRun_CollectsPerHostInOrder(t *testing.T) {
	bodies := map[string][]byte{
		"h1": []byte(`{"results":[{"label":"apt-install(nginx)","category":"would","tag":"installed"}]}`),
		"h2": []byte(`{"results":[{"label":"apt-install(nginx)","category":"ok","tag":"alreadyInstalled"}]}`),
		"h3": []byte(`{"results":[{"label":"apt-install(nginx)","category":"would","tag":"installed"}]}`),
	}
	dial := func(target string) transport.Transport {
		return fakeTransport{body: bodies[target]}
	}

	reqFor := func(string) ([]byte, error) { return []byte(`{}`), nil }
	res := Run([]string{"h1", "h2", "h3"}, "/bin/agent", reqFor, dial, 0)

	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
	if res[0].Target != "h1" || res[1].Target != "h2" || res[2].Target != "h3" {
		t.Fatalf("order not preserved: %+v", res)
	}

	tally := map[string]int{}
	for _, hr := range res {
		s := hr.Response.Results[0]
		tally[s.Category+"."+s.Tag]++
	}
	if tally["would.installed"] != 2 || tally["ok.alreadyInstalled"] != 1 {
		t.Fatalf("bad tally: %v", tally)
	}
}

// The fan-out width is the operator's call, not a constant: 16 is thirteen waves on a
// 200-host fleet, and already too many handshakes over a weak uplink or through a
// rate-limiting bastion (#462).
func TestRun_WidthOfOneSerialisesTheFanOut(t *testing.T) {
	const targets = 8
	var (
		mu             sync.Mutex
		inFlight, peak int
	)
	names := make([]string, targets)
	for i := range names {
		names[i] = fmt.Sprintf("h%d", i)
	}
	// Instrumented rather than timed: a timing assertion on concurrency is flaky by
	// construction, and this must fail for the right reason or not at all.
	dial := func(string) transport.Transport {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // hold the slot long enough to overlap if it can
		mu.Lock()
		inFlight--
		mu.Unlock()
		return okTransport{}
	}

	Run(names, "/bin/agent", func(string) ([]byte, error) { return []byte(`{}`), nil }, dial, 1)
	if peak != 1 {
		t.Fatalf("width 1 must serialise the fan-out, saw %d dials at once", peak)
	}
}

func TestRun_WidthCapsConcurrency(t *testing.T) {
	const targets, width = 12, 3
	var (
		mu             sync.Mutex
		inFlight, peak int
	)
	names := make([]string, targets)
	for i := range names {
		names[i] = fmt.Sprintf("h%d", i)
	}
	dial := func(string) transport.Transport {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return okTransport{}
	}

	Run(names, "/bin/agent", func(string) ([]byte, error) { return []byte(`{}`), nil }, dial, width)
	if peak > width {
		t.Fatalf("width %d exceeded: %d dials at once", width, peak)
	}
}

// 0 means "unset", never "unlimited": an empty flag value must not become a fork bomb
// against somebody's fleet.
func TestWidth_ZeroAndNegativeFallBackToTheDefault(t *testing.T) {
	for _, n := range []int{0, -1, -16} {
		if got := width(n); got != defaultConcurrent {
			t.Fatalf("width(%d) = %d, want the default %d", n, got, defaultConcurrent)
		}
	}
	if got := width(4); got != 4 {
		t.Fatalf("width(4) = %d", got)
	}
}

// okTransport answers every request with an empty, valid response.
type okTransport struct{}

func (okTransport) Run(_ string, _ []byte) ([]byte, error) { return []byte(`{}`), nil }
