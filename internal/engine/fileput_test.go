package engine

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestFilePut_WritesBinarySafeAndIdempotent(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "sub", "asset.bin")
	payload := []byte{0x00, 0xff, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a} // binary, incl. NUL
	fp := FilePut{Path: dst, Content: b64(payload)}
	ex := ShellExecutor{}

	// not present yet → Guard does not skip
	if skip := fp.Guard(ex); skip != nil {
		t.Fatalf("guard should not skip a missing file: %v", skip)
	}
	if r := fp.Apply(ex); r.Category != OK {
		t.Fatalf("apply: %s", r)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("binary content not written verbatim: %v (%v)", got, err)
	}
	// idempotent: content matches → Guard skips
	if skip := fp.Guard(ex); skip == nil {
		t.Fatal("guard should skip when the content already matches")
	}
	// changed content → Guard no longer skips
	fp2 := FilePut{Path: dst, Content: b64([]byte("different"))}
	if skip := fp2.Guard(ex); skip != nil {
		t.Fatal("guard should not skip when the content differs")
	}
}

func TestFilePut_PreCheck(t *testing.T) {
	if r := (FilePut{Path: "", Content: b64([]byte("x"))}).PreCheck(); r == nil {
		t.Fatal("empty path must fail pre-check")
	}
	if r := (FilePut{Path: "/x", Content: "not!base64"}).PreCheck(); r == nil {
		t.Fatal("invalid base64 must fail pre-check")
	}
	if r := (FilePut{Path: "/x", Content: b64([]byte("ok"))}).PreCheck(); r != nil {
		t.Fatalf("valid input must pass pre-check: %s", r)
	}
}
