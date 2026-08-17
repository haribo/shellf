package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"shellf/internal/engine"
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
//
// Two questions: is it ours, and can anyone else rewrite it. "Ours" is the process's own
// uid or root — an agent binary owned by root is the ordinary case on a target where the
// deployment user is not the one that installed it. Group- or world-writable fails,
// **and so does a writable directory on the way to it**: replacing the file is not the
// only way to change what a path resolves to.
func ownedAndUnwritable(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("cannot stat %s: %v", p, err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot read ownership of %s", p)
		}
		uid := uint32(os.Getuid())
		if st.Uid != uid && st.Uid != 0 {
			return fmt.Errorf("%s is owned by uid %d, neither ours (%d) nor root", p, st.Uid, uid)
		}
		// The sticky bit is what makes /tmp usable by everyone without letting anyone
		// replace someone else's entry, so a world-writable directory carrying it is not
		// the hazard this is looking for.
		if fi.Mode().Perm()&0o022 != 0 && (!fi.IsDir() || fi.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("%s is writable by another user (%s)", p, fi.Mode().Perm())
		}
		if p == filepath.Dir(p) { // reached the root
			return nil
		}
	}
}
