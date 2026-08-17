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
	// The destination is resolved once, and everything is measured against the result: a
	// destination that *is* a link is ordinary (`/var/www` → `/srv/www`), and the operator
	// asked for that path. What is refused is a link **inside** it that leads out (#412).
	root, err := resolvedRoot(dst)
	if err != nil {
		return fmt.Errorf("dir.sync: %v", err)
	}

	// Deletions run first, before anything is placed. A link the source does not have is
	// an extra like any other, and it is often the very thing standing between a file and
	// its destination — removing it after the placement would mean refusing the placement
	// it was blocking. A transfer that never terminated still deletes nothing: this whole
	// function runs only once the terminator arrived (ADR-0039 §5).
	removed := 0
	if del {
		victims, rerr := stagedDeletions(staging)
		if rerr != nil {
			return rerr
		}
		if err := removeExtras(root, victims); err != nil {
			return err
		}
		removed = len(victims)
	}

	tree := filepath.Join(staging, stagingTree)
	written := 0
	err = filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
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
		if err := placeStaged(p, root, rel, d); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return fmt.Errorf("dir.sync: %v", err)
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
	// Checked before MkdirAll, not after: creating the directories would already have
	// followed the link, leaving a tree wherever it pointed (#412).
	if err := staysInside(dst, filepath.Dir(final), rel); err != nil {
		return err
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

// resolvedRoot is the destination with its own links followed. A destination that *is* a
// link is ordinary — `/var/www` pointing at `/srv/www` — and the operator named that path,
// so it is resolved once and everything below is measured against the result.
//
// A destination that does not exist yet resolves to its nearest existing ancestor plus the
// part still to be created, which is enough: what does not exist cannot be a link, and this
// process is the one about to create it.
func resolvedRoot(dst string) (string, error) {
	real, err := resolveExisting(dst)
	if err != nil {
		return "", err
	}
	return real, nil
}

// staysInside refuses a directory that leaves root once its links are followed. `rel` names
// the offending path the way the transfer knows it; the link's target is deliberately not
// in the message — the caller must not learn a path from the other side of the deployment,
// which is the rule #393 set for the control host.
func staysInside(root, dir, rel string) error {
	real, err := resolveExisting(dir)
	if err != nil {
		return fmt.Errorf("cannot resolve %s under the destination: %v", rel, err)
	}
	if underDst(root, real) {
		return nil
	}
	return fmt.Errorf("refusing to write %s: a link inside the destination leads out of it — "+
		"remove it, or let dir.sync delete what the source does not have", rel)
}

// resolveExisting resolves the longest existing prefix of path and re-appends the rest.
// `filepath.EvalSymlinks` fails outright on a path whose tail does not exist yet, which is
// the ordinary case for a first delivery.
func resolveExisting(path string) (string, error) {
	rest := ""
	p := filepath.Clean(path)
	for {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if rest == "" {
		return real, nil
	}
	return filepath.Join(real, rest), nil
}
