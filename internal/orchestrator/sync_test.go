package orchestrator

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shellf/internal/proto"
)

// syncTo drives one transfer and collects everything the control host sent, so a test can
// assert on the whole sequence rather than on one message.
func syncTo(t *testing.T, assets string, entries []proto.Entry, vars map[string]string) (files map[string][]byte, modes map[string]string, done proto.Msg, failed proto.Msg) {
	t.Helper()
	agent, control, stop := chanPair()
	defer stop()
	allow := NewAllowed(assets, []string{"dir.sync:tree"})
	go func() { _ = Serve(control, allow) }()

	if err := agent.Send(proto.Msg{Kind: proto.KindAsk, ID: "1", Resource: "dir.sync:tree",
		Entries: entries, Vars: vars}); err != nil {
		t.Fatal(err)
	}

	files, modes = map[string][]byte{}, map[string]string{}
	deadline := time.After(5 * time.Second)
	for {
		type recv struct {
			m   proto.Msg
			err error
		}
		got := make(chan recv, 1)
		go func() { m, err := agent.Recv(); got <- recv{m, err} }()
		select {
		case r := <-got:
			if r.err != nil {
				t.Fatalf("recv: %v", r.err)
			}
			switch r.m.Kind {
			case proto.KindFile:
				if r.m.Mode != "" {
					modes[r.m.Path] = r.m.Mode
					if _, seen := files[r.m.Path]; !seen {
						files[r.m.Path] = nil
					}
				}
				if r.m.Data != "" {
					b, err := base64.StdEncoding.DecodeString(r.m.Data)
					if err != nil {
						t.Fatal(err)
					}
					files[r.m.Path] = append(files[r.m.Path], b...)
				}
			case proto.KindDone:
				return files, modes, r.m, proto.Msg{}
			case proto.KindAnswer:
				return files, modes, proto.Msg{}, r.m
			}
		case <-deadline:
			t.Fatal("the transfer never terminated")
		}
	}
}

func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	assets := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(assets, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(assets, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hello.txt", "bonjour")
	write("sub/deep.txt", "profond")
	write("empty.txt", "")
	return root
}

// ADR-0039 §1: an empty target gets the whole tree, and the terminator says how many
// files it carried — the count is what makes a truncated stream detectable.
func TestSync_EmptyTargetGetsEverything(t *testing.T) {
	root := tree(t)
	files, modes, done, failed := syncTo(t, root, nil, nil)
	if failed.Error != "" {
		t.Fatalf("transfer failed: %s", failed.Error)
	}
	if done.Written != 3 {
		t.Fatalf("written=%d, want 3", done.Written)
	}
	if string(files["hello.txt"]) != "bonjour" || string(files["sub/deep.txt"]) != "profond" {
		t.Fatalf("content not delivered: %q", files)
	}
	// An empty file still arrives, closed by a flagged chunk: without it the agent would
	// see a file that never ends and be right to call the transfer truncated.
	if _, ok := files["empty.txt"]; !ok {
		t.Fatal("an empty file must still be sent")
	}
	if modes["hello.txt"] != "644" {
		t.Fatalf("mode not carried: %q", modes["hello.txt"])
	}
}

// The property `dir.copy` never had: a converged target receives **zero bytes**, not
// merely zero writes (ADR-0039 §1).
func TestSync_ConvergedTargetTransfersNothing(t *testing.T) {
	root := tree(t)
	full, _, _, _ := syncTo(t, root, nil, nil)

	// The manifest the agent would send after that first transfer.
	var have []proto.Entry
	for rel := range full {
		info, err := os.Stat(filepath.Join(root, "tree", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		have = append(have, proto.Entry{Path: rel, Size: info.Size(), MTime: info.ModTime().Unix()})
	}

	files, _, done, failed := syncTo(t, root, have, nil)
	if failed.Error != "" {
		t.Fatalf("transfer failed: %s", failed.Error)
	}
	if done.Written != 0 || len(files) != 0 {
		t.Fatalf("a converged target must receive nothing: written=%d files=%v", done.Written, files)
	}
}

// `meta` is size+mtime, and its limit is the whole reason `sha256` exists: a file whose
// content changed while both were preserved. Built deliberately here — it is not an edge
// case, it is a restored backup.
func TestSync_Sha256CatchesWhatMetaMisses(t *testing.T) {
	root := tree(t)
	path := filepath.Join(root, "tree", "hello.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same size, same mtime, different content.
	stale := proto.Entry{Path: "hello.txt", Size: info.Size(), MTime: info.ModTime().Unix()}

	files, _, done, _ := syncTo(t, root, []proto.Entry{stale}, map[string]string{"compare": "meta"})
	if _, sent := files["hello.txt"]; sent {
		t.Fatal("meta compares size+mtime only: it cannot see this change (that is its documented limit)")
	}
	if done.Written != 2 {
		t.Fatalf("written=%d, want 2 (the other two files)", done.Written)
	}

	staleSHA := stale
	staleSHA.SHA = "0000000000000000000000000000000000000000000000000000000000000000"
	files, _, _, _ = syncTo(t, root, []proto.Entry{staleSHA}, map[string]string{"compare": "sha256"})
	if string(files["hello.txt"]) != "bonjour" {
		t.Fatal("sha256 must catch a change meta misses")
	}
}

// Extras ride the terminator, so a transfer that never terminates deletes nothing.
func TestSync_ExtrasAreListedOnTheTerminator(t *testing.T) {
	root := tree(t)
	have := []proto.Entry{{Path: "gone.txt", Size: 1, MTime: 1}}
	_, _, done, _ := syncTo(t, root, have, nil)
	if len(done.Delete) != 1 || done.Delete[0] != "gone.txt" {
		t.Fatalf("the terminator must list what the source does not have: %v", done.Delete)
	}
}

// A path the plan never declared is refused, as for any other resource (ADR-0031 §3).
func TestSync_UndeclaredIsRefused(t *testing.T) {
	agent, control, stop := chanPair()
	defer stop()
	go func() { _ = Serve(control, NewAllowed(t.TempDir(), nil)) }()
	if err := agent.Send(proto.Msg{Kind: proto.KindAsk, ID: "1", Resource: "dir.sync:tree"}); err != nil {
		t.Fatal(err)
	}
	m, err := agent.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if m.Error == "" {
		t.Fatal("an undeclared tree must be refused")
	}
}
