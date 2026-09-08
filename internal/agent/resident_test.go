package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shellf/internal/proto"
)

func TestServeResident_ProcessesThenSelfKills(t *testing.T) {
	wd := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(wd, 0o700); err != nil {
		t.Fatal(err)
	}
	// Deposit a request before the loop starts.
	req := proto.Request{Mode: "apply", Steps: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "echo hi"}}}}
	data, _ := json.Marshal(req)
	if err := os.WriteFile(filepath.Join(wd, "req-job1.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// A stub binary that the self-kill should erase (not the test's own binary).
	binPath := filepath.Join(t.TempDir(), "shellf-stub")
	if err := os.WriteFile(binPath, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	f.set("echo hi", "", 0)
	done := make(chan error, 1)
	go func() { done <- ServeResident(wd, binPath, f, 300*time.Millisecond) }()

	// Wait for the result marker.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(wd, "done-job1")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	out, err := os.ReadFile(filepath.Join(wd, "out-job1.json"))
	if err != nil {
		t.Fatalf("no result written: %v", err)
	}
	var resp proto.Response
	_ = json.Unmarshal(out, &resp)
	if len(resp.Results) != 1 || resp.Results[0].Category != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(wd, "req-job1.json")); !os.IsNotExist(err) {
		t.Fatalf("request should be consumed")
	}

	// Self-kills after the inactivity TTL and removes the workdir.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeResident returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeResident did not self-kill after the TTL")
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatalf("workdir should be removed on self-kill")
	}
	if _, err := os.Stat(binPath); !os.IsNotExist(err) {
		t.Fatalf("binary should be erased on self-kill (zero trace)")
	}
}

// #600. The result and the marker were written with their errors discarded, and the marker
// unconditionally — so a workdir that cannot take the result still got `done-<id>`. The
// control host then `cat`s a file that is not there, reads an empty answer for a job that
// ran, and reports `unexpected end of JSON input`: an error naming everything except what
// happened.
//
// Simulated by making the result path unwritable rather than by filling a disk: what the
// code must not do is write the marker after a failed write, whatever caused the failure.
func TestProcessJob_NoMarkerWhenTheResultCannotBeWritten(t *testing.T) {
	wd := t.TempDir()
	req := proto.Request{Mode: "apply", Steps: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "echo hi"}}}}
	data, _ := json.Marshal(req)
	reqPath := filepath.Join(wd, "req-job9.json.claiming")
	if err := os.WriteFile(reqPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory where the result file must go: every write to that path fails, and it
	// cannot be replaced by a rename either.
	if err := os.MkdirAll(filepath.Join(wd, "out-job9.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	f.set("echo hi", "", 0)
	processJob(wd, reqPath, f, nil)

	if _, err := os.Stat(filepath.Join(wd, "done-job9")); err == nil {
		t.Fatal("done was written while the result was not — the control host would read an empty result for a job that ran")
	}
}

// The fallback of the same fix: when the full result cannot be stored but a short one can,
// the agent says so rather than staying silent until the run times out. Here the result is
// writable, so the ordinary path applies and the marker follows the result.
func TestProcessJob_MarkerFollowsTheResult(t *testing.T) {
	wd := t.TempDir()
	req := proto.Request{Mode: "apply", Steps: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "echo hi"}}}}
	data, _ := json.Marshal(req)
	reqPath := filepath.Join(wd, "req-job8.json.claiming")
	if err := os.WriteFile(reqPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFake()
	f.set("echo hi", "", 0)
	processJob(wd, reqPath, f, nil)

	if _, err := os.Stat(filepath.Join(wd, "out-job8.json")); err != nil {
		t.Fatalf("the result must be written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, "done-job8")); err != nil {
		t.Fatalf("the marker must follow it: %v", err)
	}
}
