package std

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// writeTGZ writes a gzipped tar containing a single file `name` with `content`.
func writeTGZ(t *testing.T, path, name, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// Regression for #259: a changed archive at the same src must re-extract, not be
// skipped because dst is non-empty. Drives the real def against real files.
func TestArchiveExtract_ReExtractsOnArchiveChange(t *testing.T) {
	for _, bin := range []string{"tar", "sha256sum"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tgz")
	dst := filepath.Join(dir, "out")
	def, ok := Lookup("archive-extract")
	if !ok {
		t.Fatal("archive-extract not found")
	}
	args := map[string]string{"src": src, "dst": dst}
	run := func() engine.Result {
		res, err := lang.EvalDef(def, args, nil, engine.ShellExecutor{}, engine.Apply)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	readV := func() string {
		b, err := os.ReadFile(filepath.Join(dst, "v"))
		if err != nil {
			t.Fatalf("read extracted file: %v", err)
		}
		return string(b)
	}

	writeTGZ(t, src, "v", "A")
	run()
	if readV() != "A" {
		t.Fatalf("first extract: got %q, want A", readV())
	}

	// A DIFFERENT archive at the same src path.
	writeTGZ(t, src, "v", "B")
	run()
	if got := readV(); got != "B" {
		t.Fatalf("a changed archive must re-extract (#259): got %q, want B", got)
	}

	// An unchanged re-run stays converged and does not re-extract (idempotence).
	if res := run(); res.Category != engine.OK || res.Tag != "already" {
		t.Fatalf("unchanged re-run should skip, got %s", res)
	}
}
