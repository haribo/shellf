package transport

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// A workdir that cannot be created is reported, not skipped (#638).
//
// It used to be `if err == nil { … }` with no else: the agent then started with no
// workdir argument, opened no channel, and the first primitive failed with `no control
// host channel available for this run` — a message about the target, for a failure on the
// control host. The agent is another process and reads its workdir from argv, so unlike
// the agent's own `Unavailable` there is no way to hand it the cause.
func TestLocal_WorkdirThatCannotBeCreatedIsReported(t *testing.T) {
	old := sockBase
	sockBase = func() string { return filepath.Join(t.TempDir(), "no-such-parent") }
	defer func() { sockBase = old }()

	l := Local{Channel: func(io.Reader, io.WriteCloser) error { return nil }}
	_, err := l.Run("/nonexistent/shellf", []byte(`{}`))
	if err == nil {
		t.Fatal("a workdir that cannot be created must fail the run")
	}
	if !strings.Contains(err.Error(), "control channel workdir") {
		t.Fatalf("the failure must name what could not be made, got: %v", err)
	}
}
