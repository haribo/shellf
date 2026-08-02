package module

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in            string
		path, version string
		remote        bool
	}{
		{"github.com/alice/web@v1.2.0", "github.com/alice/web", "v1.2.0", true},
		{"git@github.com:alice/web@v1.0.0", "git@github.com:alice/web", "v1.0.0", true},
		{"../shared", "", "", false},         // local path, no @
		{"github.com/alice/web@", "", "", false}, // empty version
		{"@v1", "", "", false},                    // empty path
	}
	for _, c := range cases {
		spec, ok := ParseSpec(c.in)
		if ok != c.remote {
			t.Fatalf("ParseSpec(%q) remote=%v, want %v", c.in, ok, c.remote)
		}
		if ok && (spec.Path != c.path || spec.Version != c.version) {
			t.Fatalf("ParseSpec(%q) = %+v", c.in, spec)
		}
	}
}

func TestGitURL(t *testing.T) {
	if got := gitURL("github.com/alice/web"); got != "https://github.com/alice/web" {
		t.Fatalf("plain path: %q", got)
	}
	for _, u := range []string{"https://x/y", "file:///tmp/r", "git@github.com:a/b"} {
		if got := gitURL(u); got != u {
			t.Fatalf("explicit scheme changed: %q → %q", u, got)
		}
	}
}

func TestContentHash(t *testing.T) {
	a := ContentHash([]string{"def a() {}", "def b() {}"})
	b := ContentHash([]string{"def b() {}", "def a() {}"}) // order-independent
	if a != b {
		t.Fatal("ContentHash must be order-independent")
	}
	if a == ContentHash([]string{"def a() {}", "def c() {}"}) {
		t.Fatal("ContentHash must change with content")
	}
}

// git makes a helper for the integration test.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestResolve_LocalGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// A throwaway git repo of defs, tagged v1.0.0.
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "web.shellf"),
		[]byte(`def deploy(port: str) { apply { shell { echo "$port" } } }`), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "init")
	run(t, repo, "tag", "v1.0.0")

	cache := t.TempDir()
	spec := Spec{Path: "file://" + repo, Version: "v1.0.0"}
	got, err := Resolve(spec, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || !strings.Contains(got.Sources[0], "def deploy") {
		t.Fatalf("sources: %v", got.Sources)
	}
	if len(got.SHA) != 40 || got.Hash == "" {
		t.Fatalf("sha/hash: %q %q", got.SHA, got.Hash)
	}
	// The SHA is cached; a second resolve reads it back identically.
	if _, err := os.Stat(filepath.Join(cache, got.SHA)); err != nil {
		t.Fatalf("module not cached under its SHA: %v", err)
	}
	again, err := Resolve(spec, cache)
	if err != nil || again.SHA != got.SHA || again.Hash != got.Hash {
		t.Fatalf("cache re-resolve differs: %+v vs %+v (%v)", again, got, err)
	}
}

func TestResolve_MissingTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "x.shellf"), []byte("def a() { apply { shell { echo hi } } }"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "init")

	_, err := Resolve(Spec{Path: "file://" + repo, Version: "v9.9.9"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no such tag") {
		t.Fatalf("a missing tag must error: %v", err)
	}
}
