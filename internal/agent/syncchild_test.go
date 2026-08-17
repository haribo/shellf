package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shellf/internal/engine"
)

// inProcessChild runs the escalated verbs without forking (ADR-0044 §1 runs them as a
// child of the agent, through the executor). A unit test cannot take that path: the binary
// running `go test` is the test binary, and it knows no `__sync-commit`. The verbs
// themselves are the same functions either way, so what these tests exercise — manifest,
// staging, placement, modes, mtimes, deletions — is the real thing.
//
// What only the e2e harness can prove is what it asserts: that the fork happens, that the
// escalation applies, and that the files end up owned by the right user.
func inProcessChild(_ engine.Executor, args ...string) (string, error) {
	var out strings.Builder
	switch {
	case len(args) == 3 && args[0] == "__sync-scan":
		if err := SyncScan(args[1], args[2], &out); err != nil {
			return "", err
		}
	case len(args) >= 3 && args[0] == "__sync-commit":
		del := len(args) > 3 && args[3] == "--delete"
		if err := SyncCommit(args[1], args[2], del, &out); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unexpected child invocation: %v", args)
	}
	return out.String(), nil
}

// noEscalation is the executor a unit test hands to a transfer. It is never used to run
// anything — the child is in-process here — and it stands for "no `as <user>` in force".
type noEscalation struct{}

func (noEscalation) Shell(string, engine.Env) engine.ShellResult { return engine.ShellResult{} }
func (noEscalation) As(string) engine.Executor                   { return noEscalation{} }
func (noEscalation) Using(string) engine.Executor                { return noEscalation{} }

// What the escalated verb actually does, exercised directly: it places every staged file
// with the mode and mtime the staged copy carries, and applies the deletions listed beside
// the tree. Modes and mtimes are not decoration — `meta` compares the destination's mtime
// against the source's, so a file stamped with its write time is re-sent on every run,
// which is the bug ADR-0039 §1 exists to prevent.
func TestSyncCommit_PlacesWithModesMtimesAndDeletions(t *testing.T) {
	staging, dst := t.TempDir(), t.TempDir()
	tree := filepath.Join(staging, stagingTree)
	if err := os.MkdirAll(filepath.Join(tree, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	for _, f := range []struct {
		rel  string
		mode os.FileMode
	}{{"hello.txt", 0o644}, {"sub/secret.bin", 0o600}} {
		p := filepath.Join(tree, f.rel)
		if err := os.WriteFile(p, []byte(f.rel), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil { // umask does not get a say
			t.Fatal(err)
		}
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	// Something already in the destination that the source no longer has.
	if err := os.WriteFile(filepath.Join(dst, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, stagingDeletion), []byte("gone.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := SyncCommit(staging, dst, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"written":2`) {
		t.Fatalf("the report must count what landed: %q", out.String())
	}

	for _, f := range []struct {
		rel  string
		mode os.FileMode
	}{{"hello.txt", 0o644}, {"sub/secret.bin", 0o600}} {
		fi, err := os.Stat(filepath.Join(dst, f.rel))
		if err != nil {
			t.Fatalf("%s did not land: %v", f.rel, err)
		}
		if fi.Mode().Perm() != f.mode {
			t.Fatalf("%s: mode %v, want %v", f.rel, fi.Mode().Perm(), f.mode)
		}
		if !fi.ModTime().Equal(stamp) {
			t.Fatalf("%s: mtime %v, want the source's %v", f.rel, fi.ModTime(), stamp)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("a listed deletion must be applied")
	}
}

// Without `--delete`, the same staging leaves the extra file alone: a copy is a sync that
// deletes nothing (ADR-0039 §6), and the flag is the whole difference.
func TestSyncCommit_WithoutDeleteLeavesExtrasAlone(t *testing.T) {
	staging, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(staging, stagingTree), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, stagingDeletion), []byte("gone.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := SyncCommit(staging, dst, false, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "gone.txt")); err != nil {
		t.Fatalf("a copy must remove nothing: %v", err)
	}
}

// #412: `underDst` compares cleaned path text, and a symlink is not a lexical thing. A
// link *inside* the destination therefore sent a placement outside it — measured:
// `dir.copy(%"tree2", "/tmp/probe-dst")` with `probe-dst/sub -> /tmp/probe-escape` wrote
// `/tmp/probe-escape/x.txt` and reported `ok.copied`.
//
// Since ADR-0044 this runs under `sudo` when the block is `as root`, so the ceiling moved
// from the connecting user's reach to the machine.
func TestSyncCommit_RefusesToWriteThroughALinkOutOfTheDestination(t *testing.T) {
	staging, dst := t.TempDir(), t.TempDir()
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(dst, "sub")); err != nil {
		t.Fatal(err)
	}
	writeStagedFile(t, staging, "sub/x.txt", "PAYLOAD")

	var out strings.Builder
	err := SyncCommit(staging, dst, false, &out)
	if err == nil {
		t.Fatal("writing through a link out of the destination must be refused")
	}
	if !strings.Contains(err.Error(), "sub") {
		t.Fatalf("the refusal must name the component that leaves: %v", err)
	}
	// Naming the link's target would hand the caller a path from the other side of the
	// deployment — the rule #393 set for the control host.
	if strings.Contains(err.Error(), escape) {
		t.Fatalf("the refusal must not name what the link pointed at: %v", err)
	}
	if _, err := os.Stat(filepath.Join(escape, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("a refused placement still wrote outside the destination")
	}
}

// The legitimate case, which must keep working: the destination *is* a link. `/var/www`
// pointing at `/srv/www` is ordinary, and the operator asked for `/var/www`.
func TestSyncCommit_AllowsADestinationThatIsItselfALink(t *testing.T) {
	staging, real := t.TempDir(), t.TempDir()
	dst := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, dst); err != nil {
		t.Fatal(err)
	}
	writeStagedFile(t, staging, "sub/x.txt", "PAYLOAD")

	var out strings.Builder
	if err := SyncCommit(staging, dst, false, &out); err != nil {
		t.Fatalf("a destination that is a link must be delivered into: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(real, "sub", "x.txt")); err != nil || string(b) != "PAYLOAD" {
		t.Fatalf("content: %q (%v)", b, err)
	}
}

// With `delete`, the link is what the source does not have, so it goes the way every other
// extra goes — and the placement that it blocked then succeeds. Deletions run before the
// placement for exactly this reason.
func TestSyncCommit_DeleteClearsALinkInTheWay(t *testing.T) {
	staging, dst := t.TempDir(), t.TempDir()
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(dst, "sub")); err != nil {
		t.Fatal(err)
	}
	writeStagedFile(t, staging, "sub/x.txt", "PAYLOAD")
	if err := os.WriteFile(filepath.Join(staging, stagingDeletion), []byte("sub\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := SyncCommit(staging, dst, true, &out); err != nil {
		t.Fatalf("a sync that may delete must clear the link and deliver: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "x.txt")); err != nil || string(b) != "PAYLOAD" {
		t.Fatalf("the file must land inside the destination: %q (%v)", b, err)
	}
	if _, err := os.Stat(filepath.Join(escape, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("nothing must have been written through the link")
	}
}

// writeStagedFile puts one file in the staging tree, as the unprivileged agent does.
func writeStagedFile(t *testing.T, staging, rel, content string) {
	t.Helper()
	p := filepath.Join(staging, stagingTree, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
