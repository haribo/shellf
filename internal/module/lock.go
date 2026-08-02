package module

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Lock is the parsed shellf.lock: each remote import spec (`path@version`) pinned
// to its resolved commit SHA and content hash (ADR-0016 §5). It is the trust
// anchor — a moved tag whose SHA or content changed is rejected on the next run.
type Lock map[string]Entry

// Entry pins one module.
type Entry struct {
	SHA  string
	Hash string
}

const lockFile = "shellf.lock"

// LoadLock reads `<dir>/shellf.lock`. A missing file is an empty lock (first run).
func LoadLock(dir string) (Lock, error) {
	b, err := os.ReadFile(filepath.Join(dir, lockFile))
	if os.IsNotExist(err) {
		return Lock{}, nil
	}
	if err != nil {
		return nil, err
	}
	lock := Lock{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("%s:%d: want `spec sha hash`, got %q", lockFile, i+1, line)
		}
		lock[f[0]] = Entry{SHA: f[1], Hash: f[2]}
	}
	return lock, nil
}

// SaveLock writes the lock to `<dir>/shellf.lock`, sorted for a stable diff.
func SaveLock(dir string, lock Lock) error {
	specs := make([]string, 0, len(lock))
	for spec := range lock {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	var b strings.Builder
	b.WriteString("# shellf.lock — pinned remote modules (ADR-0016). Do not edit by hand.\n")
	for _, spec := range specs {
		e := lock[spec]
		fmt.Fprintf(&b, "%s %s %s\n", spec, e.SHA, e.Hash)
	}
	return os.WriteFile(filepath.Join(dir, lockFile), []byte(b.String()), 0o644)
}

// ResolveLocked resolves spec through the lock (ADR-0016 §5). A locked module is
// served from the cache with no network when present (hash-verified); if absent
// it is refetched and both its SHA and content are checked against the lock. An
// un-locked module is resolved and recorded (changed=true → the caller writes the
// lock back).
func ResolveLocked(spec Spec, cacheDir string, lock Lock) (sources []string, changed bool, err error) {
	key := spec.Path + "@" + spec.Version
	entry, locked := lock[key]
	if !locked {
		got, err := Resolve(spec, cacheDir)
		if err != nil {
			return nil, false, err
		}
		lock[key] = Entry{SHA: got.SHA, Hash: got.Hash}
		return got.Sources, true, nil
	}

	// Locked + cached → verify offline, no network.
	if dir := filepath.Join(cacheDir, entry.SHA); statDir(dir) {
		srcs, err := shellfSources(dir)
		if err != nil {
			return nil, false, err
		}
		if ContentHash(srcs) != entry.Hash {
			return nil, false, fmt.Errorf("lock: cached content for %s (%s) does not match shellf.lock", key, entry.SHA)
		}
		return srcs, false, nil
	}

	// Locked but not cached → refetch and check against the lock.
	got, err := Resolve(spec, cacheDir)
	if err != nil {
		return nil, false, err
	}
	if got.SHA != entry.SHA {
		return nil, false, fmt.Errorf("lock: %s moved from %s to %s (a pinned tag must be immutable) — delete the shellf.lock entry to re-pin", key, entry.SHA, got.SHA)
	}
	if got.Hash != entry.Hash {
		return nil, false, fmt.Errorf("lock: content for %s changed since it was pinned", key)
	}
	return got.Sources, false, nil
}

func statDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
