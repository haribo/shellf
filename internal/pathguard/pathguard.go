// Package pathguard answers one question about a path on the local filesystem: could
// somebody else have decided what it holds.
//
// It lives on its own because two callers need the identical answer and neither is a
// sensible home for the other. `internal/agent` asks it of the agent binary it is about to
// execute under an escalation (#391); `internal/project` asks it of `shellf.conf`, which
// decides how a run behaves (ADR-0057 §5). A second copy of a check like this is how the
// two drift, and the one that drifts is the one nobody re-reads.
package pathguard

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OwnedAndUnwritable reports why path is not safe to trust, or nil.
//
// Two questions: is it ours, and can anyone else rewrite it. "Ours" is the process's own
// uid or root — a file owned by root is the ordinary case where the user running shellf is
// not the one that installed it. Group- or world-writable fails, **and so does a writable
// directory on the way to it**: replacing the file is not the only way to change what a
// path resolves to.
func OwnedAndUnwritable(path string) error {
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
