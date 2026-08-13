package transport

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Local runs the agent on the control host itself: it executes `agentBin __agent`
// as a subprocess with the request on stdin and reads the response from stdout —
// the same ephemeral agent, request, and protocol SSH uses for a one-shot job,
// minus push/deposit/poll (the binary is already here). Selected by a host with
// `local: "true"` in the inventory (ADR-0027).
type Local struct {
	// Channel serves the run's requests for control-host resources (ADR-0031), exactly
	// as for SSH. Nil means the plan asks for nothing and no socket is created.
	Channel func(io.Reader, io.WriteCloser) error
}

var _ Transport = Local{}

func (l Local) Run(agentBin string, req []byte) ([]byte, error) {
	args := []string{"__agent"}
	// The channel needs a workdir to put its socket in. Only when the plan asks for
	// something: a plan that does not keeps today's single-process behaviour.
	if l.Channel != nil {
		wd, err := os.MkdirTemp(sockBase(), "shellf-local")
		if err == nil {
			defer func() { _ = os.RemoveAll(wd) }()
			args = append(args, wd)
			stop := l.bridge(wd)
			defer stop()
		}
	}
	cmd := exec.Command(agentBin, args...)
	cmd.Stdin = bytes.NewReader(req)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("local agent: %v: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// sockBase prefers /dev/shm: a unix socket path is capped at ~108 bytes, and a long
// TMPDIR would push past it with an error that reads like nonsense.
func sockBase() string {
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		return "/dev/shm"
	}
	return os.TempDir()
}

// bridge dials the agent's socket and serves on it until stop() is called. The agent
// creates the socket as it starts, so this retries briefly rather than racing it.
func (l Local) bridge(wd string) (stop func()) {
	done := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(done)
		sock := filepath.Join(wd, "sock")
		var c net.Conn
		for i := 0; i < 100; i++ {
			var err error
			if c, err = net.Dial("unix", sock); err == nil {
				break
			}
			select {
			case <-closed:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		if c == nil {
			return
		}
		defer func() { _ = c.Close() }()
		go func() { <-closed; _ = c.Close() }()
		_ = l.Channel(c, c)
	}()
	return func() { close(closed); <-done }
}
