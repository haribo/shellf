// Package module resolves remote shellf modules on the control host (ADR-0016):
// a git repo of defs, imported by `path@version`, fetched with git, pinned to an
// immutable commit SHA, and cached content-addressed by that SHA. The target
// never sees git or the network — resolution assembles the def sources here.
package module

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Spec is a parsed remote import: a repo path and a version (git tag or SHA).
type Spec struct {
	Path    string
	Version string
}

// ParseSpec splits `path@version`. A spec is remote only when it carries an
// `@version` (a local path never does, ADR-0016); ok is false otherwise.
func ParseSpec(s string) (Spec, bool) {
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return Spec{}, false
	}
	return Spec{Path: s[:at], Version: s[at+1:]}, true
}

// Resolved is a fetched module: its def sources, the pinned commit SHA, and a
// content hash over those sources (the lockfile's trust anchor).
type Resolved struct {
	Sources []string
	SHA     string
	Hash    string
}

var hexSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// gitURL turns a module path into a clone URL: an explicit scheme (https/file/
// git@) is used as-is, otherwise `https://` is assumed (github.com/alice/web →
// https://github.com/alice/web).
func gitURL(path string) string {
	if strings.Contains(path, "://") || strings.HasPrefix(path, "git@") {
		return path
	}
	return "https://" + path
}

// Resolve returns the module at spec, from the content-addressed cache when
// present, else fetched with git. A cached SHA is never re-fetched (offline
// after first lock, ADR-0016).
func Resolve(spec Spec, cacheDir string) (Resolved, error) {
	url := gitURL(spec.Path)

	sha := spec.Version
	if !hexSHA.MatchString(spec.Version) {
		resolved, err := resolveTag(url, spec.Version)
		if err != nil {
			return Resolved{}, err
		}
		sha = resolved
	}

	dir := filepath.Join(cacheDir, sha)
	if _, err := os.Stat(dir); err != nil {
		if err := fetch(url, spec.Version, sha, dir); err != nil {
			return Resolved{}, err
		}
	}
	sources, err := shellfSources(dir)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Sources: sources, SHA: sha, Hash: ContentHash(sources)}, nil
}

// resolveTag turns a git tag into its commit SHA via ls-remote. A tag that
// resolves to nothing (a branch, or a missing tag) is rejected (ADR-0016 §2).
func resolveTag(url, version string) (string, error) {
	out, err := git("", "ls-remote", url, "refs/tags/"+version)
	if err != nil {
		return "", fmt.Errorf("resolve %s@%s: %w", url, version, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("resolve %s@%s: no such tag (a version must be a tag or a full SHA)", url, version)
	}
	return fields[0], nil
}

// fetch clones the module at `version` into a temp dir, verifies its HEAD is the
// expected SHA, and moves it into the cache atomically.
func fetch(url, version, sha, dir string) error {
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "fetch-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if _, err := git("", "clone", "--depth", "1", "--branch", version, url, tmp); err != nil {
		return fmt.Errorf("clone %s@%s: %w", url, version, err)
	}
	head, err := git(tmp, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(head); got != sha {
		return fmt.Errorf("clone %s@%s: HEAD %s != resolved %s", url, version, got, sha)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// ContentHash is a sha256 over the module's `*.shellf` sources, order-independent
// (sorted). A moved tag whose content changed produces a different hash — the
// lockfile's rejection signal (ADR-0016 §5).
func ContentHash(sources []string) string {
	sorted := append([]string(nil), sources...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, s := range sorted {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// shellfSources reads every `*.shellf` file at the module's root.
func shellfSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var srcs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".shellf") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, string(b))
	}
	return srcs, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
