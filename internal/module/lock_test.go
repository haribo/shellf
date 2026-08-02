package module

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLock_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	lock := Lock{
		"github.com/a/b@v1.0.0": {SHA: "abc", Hash: "h1"},
		"github.com/c/d@v2.0.0": {SHA: "def", Hash: "h2"},
	}
	if err := SaveLock(dir, lock); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["github.com/a/b@v1.0.0"].SHA != "abc" || got["github.com/c/d@v2.0.0"].Hash != "h2" {
		t.Fatalf("round-trip: %+v", got)
	}
}

func TestLoadLock_MissingIsEmpty(t *testing.T) {
	lock, err := LoadLock(t.TempDir())
	if err != nil || len(lock) != 0 {
		t.Fatalf("missing lock should be empty: %v %v", lock, err)
	}
}

func TestLoadLock_Malformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shellf.lock"), []byte("spec-and-sha-only deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(dir); err == nil {
		t.Fatal("a malformed lock line must error")
	}
}

// taggedRepo makes a throwaway git repo of one def, tagged v1.0.0.
func taggedRepo(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "m.shellf"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "init")
	run(t, repo, "tag", "v1.0.0")
	return repo
}

func TestResolveLocked_RecordsThenVerifies(t *testing.T) {
	repo := taggedRepo(t, `def deploy(port: str) { apply { shell { echo "$port" } } }`)
	cache := t.TempDir()
	spec := Spec{Path: "file://" + repo, Version: "v1.0.0"}
	lock := Lock{}

	// First resolve: not locked → records, changed=true.
	srcs, changed, err := ResolveLocked(spec, cache, lock)
	if err != nil || !changed || len(srcs) != 1 {
		t.Fatalf("first resolve: srcs=%v changed=%v err=%v", srcs, changed, err)
	}
	key := "file://" + repo + "@v1.0.0"
	if _, ok := lock[key]; !ok {
		t.Fatal("lock not recorded")
	}
	// Second resolve: locked + cached → verified offline, changed=false.
	if _, changed, err := ResolveLocked(spec, cache, lock); err != nil || changed {
		t.Fatalf("second resolve should be a no-op verify: changed=%v err=%v", changed, err)
	}
}

func TestResolveLocked_ContentMismatch(t *testing.T) {
	repo := taggedRepo(t, `def deploy(port: str) { apply { shell { echo "$port" } } }`)
	cache := t.TempDir()
	spec := Spec{Path: "file://" + repo, Version: "v1.0.0"}
	lock := Lock{}
	if _, _, err := ResolveLocked(spec, cache, lock); err != nil {
		t.Fatal(err)
	}
	// Tamper the lock's content hash → the cached content no longer matches.
	key := "file://" + repo + "@v1.0.0"
	e := lock[key]
	e.Hash = "tampered"
	lock[key] = e
	if _, _, err := ResolveLocked(spec, cache, lock); err == nil || !strings.Contains(err.Error(), "does not match shellf.lock") {
		t.Fatalf("a content mismatch must error: %v", err)
	}
}

func TestResolveLocked_MovedTag(t *testing.T) {
	repo := taggedRepo(t, `def deploy(port: str) { apply { shell { echo "$port" } } }`)
	spec := Spec{Path: "file://" + repo, Version: "v1.0.0"}
	// Lock a wrong SHA that is NOT in the cache → refetch, and the real tag
	// resolves to a different SHA → moved-tag rejection.
	lock := Lock{"file://" + repo + "@v1.0.0": {SHA: "0000000000000000000000000000000000000000", Hash: "x"}}
	if _, _, err := ResolveLocked(spec, t.TempDir(), lock); err == nil || !strings.Contains(err.Error(), "moved from") {
		t.Fatalf("a moved tag must error: %v", err)
	}
}
