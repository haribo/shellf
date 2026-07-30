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
	json.Unmarshal(out, &resp)
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
