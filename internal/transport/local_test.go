package transport

import (
	"os"
	"path/filepath"
	"testing"
)

// A fake agent: ignores its args, echoes stdin to stdout — enough to prove the
// transport pipes the request in and the response out.
func fakeAgent(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocal_Run_PipesStdinToStdout(t *testing.T) {
	out, err := (Local{}).Run(fakeAgent(t, "cat"), []byte(`{"steps":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"steps":[]}` {
		t.Fatalf("the request should reach the agent's stdin and its stdout come back: %q", out)
	}
}

func TestLocal_Run_NonZeroExitErrors(t *testing.T) {
	if _, err := (Local{}).Run(fakeAgent(t, "echo boom >&2; exit 3"), nil); err == nil {
		t.Fatal("a non-zero agent exit must be an error")
	}
}
