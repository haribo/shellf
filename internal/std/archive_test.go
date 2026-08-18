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

// writeTGZFiles writes a gzipped tar with several members (name -> content).
func writeTGZFiles(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// #262: extract exactly one member to a destination file; other members must not
// land, and a changed member re-extracts.
func TestArchiveExtractMember(t *testing.T) {
	for _, bin := range []string{"tar", "sha256sum"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "rel.tgz")
	dst := filepath.Join(dir, "bin", "tool")
	def, ok := Lookup("archive.extract-member")
	if !ok {
		t.Fatal("archive-extract-member not found")
	}
	args := map[string]string{"src": src, "dst": dst, "member": "bin/tool"}
	run := func() engine.Result {
		res, err := lang.EvalDefFull(def, args, nil, nil, engine.ShellExecutor{}, engine.Apply, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	writeTGZFiles(t, src, map[string]string{"bin/tool": "TOOL", "LICENSE": "LIC", "README": "RD"})
	run()
	if b, _ := os.ReadFile(dst); string(b) != "TOOL" {
		t.Fatalf("member content: got %q, want TOOL", b)
	}
	// only the requested member landed — LICENSE/README are absent from dst's tree
	for _, other := range []string{filepath.Join(dir, "bin", "LICENSE"), filepath.Join(dir, "LICENSE"), filepath.Join(dir, "bin", "README")} {
		if _, err := os.Stat(other); err == nil {
			t.Fatalf("a non-requested member leaked to %s", other)
		}
	}
	// idempotent
	if res := run(); res.Category != engine.OK || res.Tag != "already" {
		t.Fatalf("unchanged re-run should skip: %s", res)
	}
	// a changed member re-extracts
	writeTGZFiles(t, src, map[string]string{"bin/tool": "TOOL2", "LICENSE": "LIC"})
	run()
	if b, _ := os.ReadFile(dst); string(b) != "TOOL2" {
		t.Fatalf("changed member must re-extract: got %q, want TOOL2", b)
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
	def, ok := Lookup("archive.extract")
	if !ok {
		t.Fatal("archive-extract not found")
	}
	args := map[string]string{"src": src, "dst": dst}
	run := func() engine.Result {
		res, err := lang.EvalDefFull(def, args, nil, nil, engine.ShellExecutor{}, engine.Apply, nil, nil, nil, nil, nil)
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
