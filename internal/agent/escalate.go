package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"shellf/internal/engine"
	"shellf/internal/pathguard"
)

// Running part of a transfer as the user `as <user>` named (ADR-0044).
//
// The agent re-invokes its own binary through the executor, which is what carries the
// escalation — `sudo -n`, `doas`, a non-root user, or nothing at all (ADR-0011). Nothing
// here builds a sudo command: the one place that knows how to escalate keeps knowing it,
// and this code works unchanged the day a target uses doas.

// childVerb runs one of the escalated child's verbs and returns its stdout.
//
// The verb and its arguments are passed through the environment, never concatenated into
// the script: a destination path is target data, and this is the same rule every `shell`
// block already follows.
func childVerb(ex engine.Executor, args ...string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("dir.sync: cannot locate the agent binary: %v", err)
	}
	return childVerbAt(self, ex, args...)
}

// childVerbAt is childVerb with the binary named, so the refusal below can be exercised
// against a path a test controls — os.Executable() during `go test` is the test binary,
// which is exactly the thing that cannot be swapped for an unsafe one.
func childVerbAt(self string, ex engine.Executor, args ...string) (string, error) {
	// ADR-0044 §4. The control host checked this binary before launching it
	// (internal/transport/ssh.go), when the agent was going to run unprivileged. About to
	// hand it to sudo, the same weakness stops being a foothold and becomes the machine:
	// whoever can rewrite that file chooses what root executes. So it is checked again,
	// here, against the state on disk now rather than at push time.
	if err := ownedAndUnwritable(self); err != nil {
		return "", fmt.Errorf("dir.sync: refusing to escalate: %v", err)
	}

	env := engine.Env{"shellf_self": self}
	script := `"$shellf_self"`
	for i, a := range args {
		k := fmt.Sprintf("shellf_a%d", i)
		env[k] = a
		script += ` "$` + k + `"`
	}
	r := ex.Shell(script, env)
	if !r.OK() {
		msg := strings.TrimSpace(r.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(r.Stdout)
		}
		if msg == "" {
			msg = fmt.Sprintf("exit %d", r.Exit)
		}
		// The child names the primitive itself when it fails on its own terms; prefixing
		// again would print `dir.sync: dir.sync: …`, which reads like two failures.
		if strings.HasPrefix(msg, "dir.sync:") {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("dir.sync: %s", msg)
	}
	return r.Stdout, nil
}

// ownedAndUnwritable reports why path is not safe to execute under an escalation, or nil.
// The check itself lives in `internal/pathguard`: `shellf.conf` needs the identical answer
// (ADR-0057 §5), and two copies of it would not stay identical.
func ownedAndUnwritable(path string) error { return pathguard.OwnedAndUnwritable(path) }
