package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #393: the allow-list's containment check is *lexical* — a path that reads as
// `assets/x` passes it — and a symlink is not a lexical thing. A link at `assets/x`
// pointing at a file outside the project was declared, allow-listed, and served. The tree
// transfer has always skipped non-regular files, so the two halves of the same question
// answered differently.
func TestAllowed_SymlinkOutOfAssetsIsRefused(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	allow := NewAllowed(assets, []string{"file.read:leak.txt"})
	_, err := readResource(allow, "file.read:leak.txt")
	if err == nil {
		t.Fatal("a link out of assets/ must not be served")
	}
	if !strings.Contains(err.Error(), "leaves the project's assets") {
		t.Errorf("the refusal must say why: %v", err)
	}
	// The refusal must not teach the target a path on the operator's machine — that is
	// what refusing is for.
	if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), root) {
		t.Errorf("the refusal leaked a control-host path: %v", err)
	}
}

// The false-positive floor: a link *within* assets/ is an ordinary way to share content
// between two names, and must keep working.
func TestAllowed_SymlinkInsideAssetsIsServed(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "real.txt"), []byte("INSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(assets, "alias.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	allow := NewAllowed(assets, []string{"file.read:alias.txt"})
	b, err := readResource(allow, "file.read:alias.txt")
	if err != nil {
		t.Fatalf("a link inside assets/ must be served: %v", err)
	}
	if string(b) != "INSIDE" {
		t.Fatalf("wrong content: %q", b)
	}
}
