package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"shellf/internal/proto"
)

// The escalated half of a tree transfer (ADR-0044).
//
// A transfer used to write from the agent's own process, so `as <user>` — which lives in
// the executor, around a shell — never reached it: a tree delivered inside `as root`
// landed owned by the connecting user, and the run said `ok.copied` (#390).
//
// These two functions are what the agent re-invokes itself to run, through the executor,
// so the escalation applies to them and to nothing else. They take **paths and flags
// only**: no socket, no control host, no plan, no def. The unprivileged agent keeps the
// dialogue and stages what arrives; this side reads a destination and puts files in place.

// SyncScan writes the destination's manifest to w as JSON — what `dir.sync` compares
// against to decide the delta. Split out so the scan escalates too: a destination the
// connecting user cannot *read* would otherwise fail before a byte is written, and that
// is the case a shell-based placement could never cover.
func SyncScan(dst, compare string, w io.Writer) error {
	entries, err := scanTarget(dst, compare)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []proto.Entry{} // `null` and `[]` mean the same here; say the same thing
	}
	return json.NewEncoder(w).Encode(entries)
}

// SyncCommit places the staged tree into dst and applies the deletions listed in the
// staging directory, then reports what it did as JSON on w.
//
// The staging directory is written by the unprivileged agent and only read here: it holds
// `tree/`, whose files already carry the mode and mtime they must land with, and an
// optional `delete` file, one destination-relative path per line.
//
// Each file is placed the way `~file.write` places one (internal/engine/fileput.go): a
// temporary **beside the destination**, then a rename. Beside, because a rename across
// filesystems is not atomic, and #389 exists because a non-atomic replacement is
// observable by a daemon reloading its config mid-write.
func SyncCommit(staging, dst string, del bool, w io.Writer) error {
	tree := filepath.Join(staging, stagingTree)
	written := 0
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == tree {
				return filepath.SkipAll // nothing was staged: a converged transfer
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(tree, p)
		if rerr != nil {
			return rerr
		}
		if err := placeStaged(p, dst, rel, d); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return fmt.Errorf("dir.sync: %v", err)
	}

	removed := 0
	if del {
		victims, rerr := stagedDeletions(staging)
		if rerr != nil {
			return rerr
		}
		if err := removeExtras(dst, victims); err != nil {
			return err
		}
		removed = len(victims)
	}
	return json.NewEncoder(w).Encode(commitReport{Written: written, Removed: removed})
}

// commitReport is what the child hands back on stdout. Counts only: the unprivileged side
// already knows which files it staged, and a report is not a place to learn new paths.
type commitReport struct {
	Written int `json:"written"`
	Removed int `json:"removed"`
}

const (
	// stagingTree is the subdirectory holding what will land under the destination. The
	// deletions file sits beside it rather than inside, so a path a transfer legitimately
	// delivers can never be mistaken for metadata.
	stagingTree     = "tree"
	stagingDeletion = "delete"
)

func placeStaged(src, dst, rel string, d fs.DirEntry) error {
	final := filepath.Join(dst, rel)
	if !underDst(dst, final) {
		return fmt.Errorf("refusing to write outside the destination: %s", rel)
	}
	info, err := d.Info()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".shellf-sync-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op once the rename succeeded

	in, err := os.Open(src)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	_, cerr := io.Copy(tmp, in)
	_ = in.Close()
	if cerr != nil {
		_ = tmp.Close()
		return cerr
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The staged file carries the mode and mtime the destination must end up with: the
	// unprivileged side applied them when it wrote it, so there is no second manifest to
	// keep in step with the files it describes.
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return err
	}
	// `meta` compares the destination's mtime against the source's, so a file stamped with
	// its own write time would never match and would be re-sent on every run.
	t := info.ModTime()
	return os.Chtimes(final, t, t)
}

// stagedDeletions reads the destination-relative paths the transfer's terminator listed.
// A missing file means "nothing to delete", which is the ordinary case.
func stagedDeletions(staging string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(staging, stagingDeletion))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("dir.sync: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}
