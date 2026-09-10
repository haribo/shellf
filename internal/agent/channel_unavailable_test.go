package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shellf/internal/proto"
)

// An agent that could not listen answers every ask with the reason (#638).
//
// Before, `ServeOn` and the resident loop both dropped the `Listen` error and ran with a
// nil channel: a job that did ask waited out `attachWait` and failed with `no control host
// attached` — the symptom, never the cause. The job still runs, because a plan may declare
// a primitive it never reaches.
func TestChannel_UnavailableCarriesTheCause(t *testing.T) {
	cause := errors.New("bind: permission denied")
	ch := Unavailable(cause)
	defer func() { _ = ch.Close() }() // must not panic: it never listened

	start := time.Now()
	_, err := ch.AskWith("app.conf.j2", nil, nil)
	if err == nil {
		t.Fatal("an ask on an unavailable channel must fail")
	}
	if !strings.Contains(err.Error(), "app.conf.j2") {
		t.Fatalf("the failure must name the resource: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("the failure must carry why the channel could not open: %v", err)
	}
	// And it fails at once rather than sitting out the attach wait: nobody is coming.
	if d := time.Since(start); d > time.Second {
		t.Fatalf("an unavailable channel waited %v for a bridge that cannot attach", d)
	}
}

// The wiring, not just the mechanism: `ServeOn` given a workdir it cannot listen in runs
// the job anyway, and a step that asks the control host fails naming why (#638).
//
// The listen fails for a real reason here — the workdir is a *file*, so `net.Listen` on a
// path inside it cannot bind — rather than by injecting an error, which would prove only
// that the injection works.
func TestServeOn_ListenFailureReachesTheStep(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "workdir")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := newComp()
	body, _ := json.Marshal(proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"deliver": `def deliver(src: str) { apply { out = ~file.read(src) shell { printf '%s' "$out" } return ok.done } }`,
		},
		Steps: []proto.Step{{Instruction: "deliver",
			Args: map[string]string{"src": "/plan/conf.j2"}, Control: []string{"src"}}},
	})
	var out bytes.Buffer
	if err := ServeOn(bytes.NewReader(body), &out, f, notADir); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("the job must still run and report")
	}
	got := resp.Results[0].Shell
	if resp.Results[0].Category != "err" || got == nil {
		t.Fatalf("the step must fail: %+v", resp.Results[0])
	}
	if !strings.Contains(got.Stderr, "no control channel") {
		t.Fatalf("the failure must say the agent has no channel, got: %q", got.Stderr)
	}
}
