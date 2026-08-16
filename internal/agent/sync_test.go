package agent

import (
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// round-trip harness: a real agent Channel on one side, the real control-host Serve on
// the other, over the agent's own Unix socket. Nothing is faked, because the defect this
// exists to catch lives exactly in the seam — a manifest built by hand from the source
// compares the source with itself and proves nothing.
func syncPair(t *testing.T, assets string) *Channel {
	t.Helper()
	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	c, err := net.Dial("unix", filepath.Join(wd, SockName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	allow := orchestrator.NewAllowed(assets, []string{"dir.sync:tree"})
	go func() {
		conn := proto.NewConn(c)
		if err := conn.Handshake(); err != nil {
			return
		}
		_ = orchestrator.Serve(conn, allow)
	}()
	waitAttached(t, ch)
	return ch
}

func sourceTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "tree", "sub")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tree", "hello.txt"), []byte("bonjour"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "deep.bin"), []byte{0x00, 0xff, 0x00, 'z'}, 0o600); err != nil {
		t.Fatal(err)
	}
	// Stamp the source in the past, deliberately. Files written moments ago share the
	// transfer's own second, so a destination stamped with its write time would compare
	// equal by accident and the mtime rule would look correct while doing nothing — which
	// is exactly what the first version of this test did.
	old := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{
		filepath.Join(root, "tree", "hello.txt"),
		filepath.Join(src, "deep.bin"),
	} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// #335 / ADR-0039 §1: the property `dir.copy` never had. The second run must transfer
// **zero bytes**, not merely write nothing.
//
// The first version of this code failed exactly here and no test saw it: the destination
// was stamped with its write time instead of the source's, so `meta` found every file
// different forever. It only surfaced by running both halves against each other.
func TestSync_SecondRunTransfersNothing(t *testing.T) {
	root := sourceTree(t)
	dst := filepath.Join(t.TempDir(), "delivered")
	ch := syncPair(t, root)

	n, _, err := ch.Sync("dir.sync:tree", dst, "meta", false)
	if err != nil {
		t.Fatalf("first transfer: %v", err)
	}
	if n != 2 {
		t.Fatalf("first transfer wrote %d files, want 2", n)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "hello.txt")); err != nil || string(b) != "bonjour" {
		t.Fatalf("content: %q (%v)", b, err)
	}
	// Binary arrives byte-identical, including a NUL.
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "deep.bin")); err != nil || len(b) != 4 || b[1] != 0xff {
		t.Fatalf("binary corrupted: %v (%v)", b, err)
	}
	// The mode travels.
	info, err := os.Stat(filepath.Join(dst, "sub", "deep.bin"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode not carried: %v (%v)", info.Mode().Perm(), err)
	}

	n, _, err = ch.Sync("dir.sync:tree", dst, "meta", false)
	if err != nil {
		t.Fatalf("second transfer: %v", err)
	}
	if n != 0 {
		t.Fatalf("a converged tree must transfer nothing, got %d file(s)", n)
	}
}

// `delete = "true"` removes what the source does not have; `false` leaves it. Both on a
// converged tree, so the only difference is the flag.
func TestSync_DeleteIsAParameter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		del, wantGone bool
	}{
		{"delete false leaves it", false, false},
		{"delete true removes it", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := sourceTree(t)
			dst := filepath.Join(t.TempDir(), "delivered")
			ch := syncPair(t, root)
			if _, _, err := ch.Sync("dir.sync:tree", dst, "meta", false); err != nil {
				t.Fatal(err)
			}
			extra := filepath.Join(dst, "extra.txt")
			if err := os.WriteFile(extra, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ch.Sync("dir.sync:tree", dst, "meta", tc.del); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(extra)
			if tc.wantGone && err == nil {
				t.Fatal("delete = true must remove what the source does not have")
			}
			if !tc.wantGone && err != nil {
				t.Fatal("delete = false must leave it alone")
			}
		})
	}
}

// A tree far above the old 32 MB ceiling transfers, which is the failure mode `dir.copy`
// had no answer for.
func TestSync_PastTheOldCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~40 MB")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 40<<20) // above the 32 MB dir.copy refused
	for i := range big {
		big[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(root, "tree", "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "delivered")
	ch := syncPair(t, root)

	if _, _, err := ch.Sync("dir.sync:tree", dst, "meta", false); err != nil {
		t.Fatalf("a tree above the old ceiling must transfer: %v", err)
	}
	got, err := os.Stat(filepath.Join(dst, "big.bin"))
	if err != nil || got.Size() != int64(len(big)) {
		t.Fatalf("size %v (%v), want %d", got, err, len(big))
	}
}

// `compare = "sha256"` through both halves. The agent builds its manifest with digests,
// so a file whose size and mtime were preserved but whose content changed is re-sent —
// the case `meta` cannot see, and the whole reason the option exists (ADR-0039 §4).
func TestSync_Sha256SeesWhatMetaCannot(t *testing.T) {
	root := sourceTree(t)
	dst := filepath.Join(t.TempDir(), "delivered")
	ch := syncPair(t, root)

	if _, _, err := ch.Sync("dir.sync:tree", dst, "sha256", false); err != nil {
		t.Fatal(err)
	}
	// Corrupt the destination in place, preserving size and mtime exactly.
	victim := filepath.Join(dst, "hello.txt")
	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("BONJOUR"), 0o644); err != nil { // same length
		t.Fatal(err)
	}
	if err := os.Chtimes(victim, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	n, _, err := ch.Sync("dir.sync:tree", dst, "meta", false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("meta compares size+mtime: it cannot see this change, got %d re-sent", n)
	}

	n, _, err = ch.Sync("dir.sync:tree", dst, "sha256", false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sha256 must re-send the corrupted file, got %d", n)
	}
	if b, _ := os.ReadFile(victim); string(b) != "bonjour" {
		t.Fatalf("content not restored: %q", b)
	}
}

// ADR-0039 §3: a transfer cut short leaves the destination as it was, plus nothing. The
// staged file is removed, so a failed run does not litter the target with temporaries.
func TestSync_InterruptedLeavesNothingBehind(t *testing.T) {
	// The transfer retries once against a bridge that never comes back, so this would
	// otherwise sit through the production attach wait.
	old := attachWait
	attachWait = 200 * time.Millisecond
	defer func() { attachWait = old }()

	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()

	c, err := net.Dial("unix", filepath.Join(wd, SockName))
	if err != nil {
		t.Fatal(err)
	}
	// A control host that opens a file, sends one chunk, then goes away mid-transfer.
	go func() {
		conn := proto.NewConn(c)
		if err := conn.Handshake(); err != nil {
			return
		}
		m, err := conn.Recv()
		if err != nil {
			return
		}
		_ = conn.Send(proto.Msg{Kind: proto.KindFile, ID: m.ID, Path: "big.bin", Mode: "644"})
		_ = conn.Send(proto.Msg{Kind: proto.KindFile, ID: m.ID, Path: "big.bin",
			Data: base64.StdEncoding.EncodeToString([]byte("half a file"))})
		_ = c.Close() // no terminator: the bridge died
	}()
	waitAttached(t, ch)

	dst := filepath.Join(t.TempDir(), "delivered")
	if _, _, err := ch.Sync("dir.sync:tree", dst, "meta", false); err == nil {
		t.Fatal("a transfer that never terminates must fail, not report success")
	}
	if _, err := os.Stat(filepath.Join(dst, "big.bin")); err == nil {
		t.Fatal("a half-written file must never reach its destination")
	}
	// And no staging file is left behind.
	entries, err := os.ReadDir(dst)
	if err != nil {
		return // the destination was never created: nothing to leak
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".shellf-sync-") {
			t.Fatalf("a staged file was left behind: %s", e.Name())
		}
	}
}

// #335: a transfer meets the stale bridge that an ask learned to survive in #334. A
// resident agent outlives the command that created it, so the first channel user of a job
// — which `dir.copy` is — finds the previous session's bridge and gets an EOF.
//
// Found by looping the SSH harness until it failed: `dir.sync:tree: control host went away
// mid-transfer: EOF`, about one run in three. Nothing in memory showed it, because a pipe
// pair has no previous session.
func TestSync_RetriesAfterAStaleBridge(t *testing.T) {
	old := attachWait
	attachWait = 2 * time.Second
	defer func() { attachWait = old }()

	root := sourceTree(t)
	wd := shortDir(t)
	ch, err := Listen(wd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()
	sock := filepath.Join(wd, SockName)

	// A bridge from a session that has already ended: it greets, then dies.
	dead, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn := proto.NewConn(dead)
		_ = conn.Handshake()
	}()
	waitAttached(t, ch)
	_ = dead.Close()

	// The live one attaches a moment later, as the control host reconnects (#347).
	allow := orchestrator.NewAllowed(root, []string{"dir.sync:tree"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		c, derr := net.Dial("unix", sock)
		if derr != nil {
			return
		}
		conn := proto.NewConn(c)
		if conn.Handshake() != nil {
			return
		}
		_ = orchestrator.Serve(conn, allow)
	}()

	dst := filepath.Join(t.TempDir(), "delivered")
	n, _, err := ch.Sync("dir.sync:tree", dst, "meta", false)
	if err != nil {
		t.Fatalf("a transfer must survive a bridge left over from a previous session: %v", err)
	}
	if n != 2 {
		t.Fatalf("the retry must deliver the tree, got %d file(s)", n)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "hello.txt")); err != nil || string(b) != "bonjour" {
		t.Fatalf("content: %q (%v)", b, err)
	}
}

// #373: a preview answers what a transfer would do and does none of it. It is what lets
// `dir.sync` name the files it would delete *before* deleting them — a destructive
// instruction whose `--dry-run` says nothing is a design defect, not a missing nicety.
func TestSync_PreviewTouchesNothing(t *testing.T) {
	root := sourceTree(t)
	dst := filepath.Join(t.TempDir(), "delivered")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dst, "stale.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := syncPair(t, root)

	n, extras, err := ch.Preview("dir.sync:tree", dst, "meta")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("the preview must count what it would write, got %d", n)
	}
	if len(extras) != 1 || extras[0] != "stale.txt" {
		t.Fatalf("the preview must name what it would remove, got %v", extras)
	}
	// And it did none of it.
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("a preview must not remove anything")
	}
	if _, err := os.Stat(filepath.Join(dst, "hello.txt")); err == nil {
		t.Fatal("a preview must not write anything")
	}
}

// The acting half, so the two cannot drift: `delete = true` removes what the source does
// not have, and the destination ends up matching.
func TestSync_DeleteMakesTheTargetMatch(t *testing.T) {
	root := sourceTree(t)
	dst := filepath.Join(t.TempDir(), "delivered")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := syncPair(t, root)

	if _, _, err := ch.Sync("dir.sync:tree", dst, "meta", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); err == nil {
		t.Fatal("delete = true must remove what the source does not have")
	}
	if _, err := os.Stat(filepath.Join(dst, "hello.txt")); err != nil {
		t.Fatal("delete = true must still deliver the source")
	}
}
