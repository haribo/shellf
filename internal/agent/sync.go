package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shellf/internal/engine"
	"shellf/internal/proto"
)

// The agent's half of a tree transfer (ADR-0039): it sends what it already has, then
// writes what the control host streams back.

// Sync transfers a control-host tree into dst and reports how many files were written.
//
// The manifest goes first, so the control host can answer only the delta — that one round
// trip is what buys a converged run its zero bytes, and what keeps a large tree from
// costing one round trip per file.
func (c *Channel) Sync(ex engine.Executor, resource, dst, compare string, del bool) (written, removed int, err error) {
	n, extras, err := c.sync(ex, resource, dst, compare, del, false)
	if !del {
		extras = nil // a copy removes nothing, whatever the terminator listed
	}
	return n, len(extras), err
}

// Preview answers what a transfer *would* do without doing any of it: the delta is
// computed on the control host, no file is streamed, nothing is written and nothing is
// removed. It returns how many files would be written and which would be deleted, so a
// destructive def can name them before acting (#373).
func (c *Channel) Preview(ex engine.Executor, resource, dst, compare string) (int, []string, error) {
	return c.sync(ex, resource, dst, compare, false, true)
}

func (c *Channel) sync(ex engine.Executor, resource, dst, compare string, del, preview bool) (int, []string, error) {
	entries, err := c.scanThrough(ex, dst, compare)
	if err != nil {
		return 0, nil, err
	}
	// Two attempts, for the same reason an ask gets two (#334): the connection this agent
	// holds may belong to a session that has already ended, and it only reveals itself on
	// use. A transfer is the *first* thing a job like `dir.copy` does, so it meets the
	// stale bridge where a later `file.read` would have found the fresh one — which is
	// why this was intermittent, and why it hit the transfer and nothing else.
	//
	// Restarting is safe and cheap by construction (ADR-0039 §3): the second attempt
	// rebuilds its manifest, so whatever arrived before the drop is not sent again.
	//
	// A preview writes nothing, so it needs no staging area and no commit: it asks for the
	// delta, receives the terminator alone, and reports.
	if preview {
		n, extras, err, stale := c.streamOnce(resource, "", compare, preview, entries, resource)
		if stale {
			entries, serr := c.scanThrough(ex, dst, compare)
			if serr != nil {
				return 0, nil, serr
			}
			n, extras, err, _ = c.streamOnce(resource, "", compare, preview, entries, resource)
		}
		return n, extras, err
	}

	staging, err := os.MkdirTemp(c.workdir, "sync-")
	if err != nil {
		return 0, nil, fmt.Errorf("dir.sync: %v", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	n, extras, err, stale := c.streamOnce(resource, staging, compare, preview, entries, resource)
	if stale {
		// A retry starts from a clean staging area: the first attempt's files are still
		// there, and nothing has been placed, so the second manifest asks for them again.
		//
		// Emptied, not removed. `RemoveAll` took the directory itself with it, and a
		// transfer that delivers a file recreated it by accident — through the `MkdirAll`
		// that opens the first staged file. One that stages nothing did not, so a
		// delete-only retry then failed writing its deletion list into a directory that no
		// longer existed (#431).
		if err := os.RemoveAll(staging); err != nil {
			return 0, nil, fmt.Errorf("dir.sync: %v", err)
		}
		if err := os.Mkdir(staging, 0o700); err != nil {
			return 0, nil, fmt.Errorf("dir.sync: %v", err)
		}
		entries, serr := c.scanThrough(ex, dst, compare)
		if serr != nil {
			return 0, nil, serr
		}
		n, extras, err, _ = c.streamOnce(resource, staging, compare, preview, entries, resource)
	}
	if err != nil {
		return 0, nil, err
	}

	// Everything is staged; placing it is the escalated half (ADR-0044 §1). It runs even
	// when nothing was transferred: a delete-only sync has files to remove and none to
	// write, and #387 is what a transfer reporting "nothing happened" in that case cost.
	if del && len(extras) > 0 {
		if err := os.WriteFile(filepath.Join(staging, stagingDeletion),
			[]byte(strings.Join(extras, "\n")), 0o600); err != nil {
			return 0, nil, fmt.Errorf("dir.sync: %v", err)
		}
	}
	placed, err := c.commitThrough(ex, staging, dst, del)
	if err != nil {
		return 0, nil, err
	}
	// ADR-0039 §5: the terminator says how many files the transfer sent, so a stream that
	// ended early is detected rather than committed as a complete one.
	if placed != n {
		return 0, nil, fmt.Errorf("dir.sync: the transfer sent %d file(s) but %d landed — refusing to report a partial delivery", n, placed)
	}
	return n, extras, nil
}

// scanThrough asks the escalated child for the destination's manifest. It goes through the
// executor even with no escalation in force (ADR-0044 §5): keeping a second, in-process
// path would leave the escalated one as the one nobody exercises — and it is the one that
// touches root.
func (c *Channel) scanThrough(ex engine.Executor, dst, compare string) ([]proto.Entry, error) {
	out, err := c.child(ex, "__sync-scan", dst, compare)
	if err != nil {
		return nil, err
	}
	var entries []proto.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("dir.sync: unreadable manifest from the agent: %v", err)
	}
	return entries, nil
}

// commitThrough places the staged tree, as whoever `as <user>` named. Returns how many
// files landed.
func (c *Channel) commitThrough(ex engine.Executor, staging, dst string, del bool) (int, error) {
	args := []string{"__sync-commit", staging, dst}
	if del {
		args = append(args, "--delete")
	}
	out, err := c.child(ex, args...)
	if err != nil {
		return 0, err
	}
	var rep commitReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		return 0, fmt.Errorf("dir.sync: unreadable report from the agent: %v", err)
	}
	return rep.Written, nil
}

// scanTarget lists what is already under dst, in the same shape the control host walks
// its source. A missing destination is not an error: it is the first run, and an empty
// manifest asks for everything.
func scanTarget(dst, compare string) ([]proto.Entry, error) {
	var out []proto.Entry
	err := filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dst {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil // directories are not carried; the agent creates what it needs
		}
		rel, rerr := filepath.Rel(dst, p)
		if rerr != nil {
			return rerr
		}
		if !d.Type().IsRegular() {
			// Not carried, so never compared as content — but named, so the source can
			// see it has no such thing and call it extra. Left out of the manifest, a link
			// in the way could not be removed and the delivery wrote through it (#412).
			out = append(out, proto.Entry{Path: filepath.ToSlash(rel), Kind: "irregular"})
			return nil
		}
		rel, err = filepath.Rel(dst, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := proto.Entry{Path: filepath.ToSlash(rel), Size: info.Size(), MTime: info.ModTime().Unix()}
		if compare == "sha256" {
			sum, err := fileSum(p)
			if err != nil {
				return err
			}
			e.SHA = sum
		}
		out = append(out, e)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("dir.sync: reading %s: %v", dst, err)
	}
	return out, nil
}

// Each file is written into the staging tree and renamed into place there once its last
// chunk lands — the rule #298 imposes on `file.write`: a reader must never catch a partial
// file. Nothing reaches the destination until the whole transfer terminates and the
// escalated commit runs, so an interrupted transfer leaves the destination exactly as it
// was, and the retry's manifest lists what is already there (ADR-0039 §3).
// streamOnce consumes one transfer. `staging` is where files land — a directory this
// agent owns, never the destination: placing them is the escalated half (ADR-0044). It is
// empty for a preview, which receives no file at all.
func (c *Channel) streamOnce(resource, staging, compare string, preview bool, entries []proto.Entry, label string) (n int, extras []string, err error, stale bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, err := c.attached(resource)
	if err != nil {
		return 0, nil, err, false
	}
	c.next++
	id := strconv.Itoa(c.next)
	if err := conn.Send(proto.Msg{Kind: proto.KindAsk, ID: id, Resource: resource,
		Entries: entries, Vars: syncVars(compare, preview)}); err != nil {
		c.drop()
		return 0, nil, fmt.Errorf("%s: control host went away while asking: %v", resource, err), true
	}

	var staged *stagedFile
	defer func() {
		if staged != nil {
			staged.abandon() // a transfer that never terminated leaves nothing behind
		}
	}()

	for {
		m, err := conn.Recv()
		if err != nil {
			c.drop()
			return 0, nil, fmt.Errorf("%s: control host went away mid-transfer: %v", resource, err), true
		}
		if m.ID != id {
			continue // another exchange on the same socket
		}
		switch m.Kind {
		case proto.KindAnswer: // only ever an error for this resource
			if m.Error != "" {
				return 0, nil, fmt.Errorf("%s: %s", resource, m.Error), false
			}
			return 0, nil, fmt.Errorf("%s: unexpected answer mid-transfer", resource), false

		case proto.KindFile:
			if m.Mode != "" && staged == nil || (staged != nil && staged.rel != m.Path) {
				if staged != nil {
					if err := staged.commit(); err != nil {
						return 0, nil, err, false
					}
				}
				if staged, err = openStaged(filepath.Join(staging, stagingTree), m.Path, m.Mode, m.MTime); err != nil {
					return 0, nil, err, false
				}
			}
			if m.Data != "" {
				b, derr := base64.StdEncoding.DecodeString(m.Data)
				if derr != nil {
					return 0, nil, fmt.Errorf("%s: unreadable chunk for %s", resource, m.Path), false
				}
				if err := staged.write(b); err != nil {
					return 0, nil, err, false
				}
			}
			if m.Last {
				if err := staged.commit(); err != nil {
					return 0, nil, err, false
				}
				staged = nil
			}

		case proto.KindDone:
			if staged != nil {
				// A file left open when the terminator arrives means the stream lost a
				// chunk: refuse rather than commit a truncated file.
				return 0, nil, fmt.Errorf("%s: transfer ended with %s unfinished", resource, staged.rel), false
			}
			// Deletions are not applied here: they are the escalated commit's, applied
			// after the transfer terminated and only when the caller asked for them
			// (ADR-0039 §5).
			return m.Written, m.Delete, nil, false
		}
	}
}

// syncVars carries the primitive's options to the control host. `preview` makes the
// answer a terminator with no file behind it.
func syncVars(compare string, preview bool) map[string]string {
	v := map[string]string{"compare": compare}
	if preview {
		v["preview"] = "true"
	}
	return v
}

// stagedFile is one destination being written: a temporary beside it, renamed on commit.
type stagedFile struct {
	rel, final, tmp string
	f               *os.File
	mode            os.FileMode
	mtime           int64
}

// underDst reports whether a manifest-relative path stays inside the destination.
// `filepath.Join` *cleans* what it joins, so `../../etc/x` produces a path outside `dst`
// rather than an error. The control host is the one composing these paths and is the
// trusted party, so this is not reachable today — it is the invariant that keeps it that
// way the day a transfer accepts a manifest from anywhere else (#393).
func underDst(dst, final string) bool {
	rel, err := filepath.Rel(dst, final)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func openStaged(dst, rel, mode string, mtime int64) (*stagedFile, error) {
	final := filepath.Join(dst, filepath.FromSlash(rel))
	if !underDst(dst, final) {
		return nil, fmt.Errorf("dir.sync: refusing to write outside the destination: %s", rel)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return nil, fmt.Errorf("dir.sync: %v", err)
	}
	f, err := os.CreateTemp(filepath.Dir(final), ".shellf-sync-*")
	if err != nil {
		return nil, fmt.Errorf("dir.sync: %v", err)
	}
	perm := os.FileMode(0o644)
	if m, err := strconv.ParseUint(mode, 8, 32); err == nil && m != 0 {
		perm = os.FileMode(m)
	}
	return &stagedFile{rel: rel, final: final, tmp: f.Name(), f: f, mode: perm, mtime: mtime}, nil
}

func (s *stagedFile) write(b []byte) error {
	if _, err := s.f.Write(b); err != nil {
		return fmt.Errorf("dir.sync: writing %s: %v", s.rel, err)
	}
	return nil
}

func (s *stagedFile) commit() error {
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("dir.sync: %v", err)
	}
	if err := os.Chmod(s.tmp, s.mode); err != nil {
		return fmt.Errorf("dir.sync: %v", err)
	}
	if err := os.Rename(s.tmp, s.final); err != nil {
		return fmt.Errorf("dir.sync: %v", err)
	}
	// The source's mtime is applied here, and it is not decoration: `meta` compares the
	// destination's mtime against the source's, so a file stamped with its own write time
	// would never match and would be re-sent on every run.
	if s.mtime != 0 {
		t := time.Unix(s.mtime, 0)
		if err := os.Chtimes(s.final, t, t); err != nil {
			return fmt.Errorf("dir.sync: %v", err)
		}
	}
	return nil
}

func (s *stagedFile) abandon() {
	_ = s.f.Close()
	_ = os.Remove(s.tmp)
}

// fileSum is the digest `compare = "sha256"` builds a manifest with. Streamed, so a
// large file is not held in memory to be compared.
func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// removeExtras deletes what the terminator listed. Applied last and only on a terminated
// transfer, so an interrupted one never removes anything (ADR-0039 §5).
func removeExtras(dst string, extras []string) error {
	for _, rel := range extras {
		victim := filepath.Join(dst, filepath.FromSlash(rel))
		if !underDst(dst, victim) {
			return fmt.Errorf("dir.sync: refusing to remove outside the destination: %s", rel)
		}
		// The victim itself may be a link, and removing a link removes the link — that is
		// what clears one standing in a delivery's way. What must not happen is *reaching*
		// it through another: a link in an intermediate directory would delete a file on
		// the far side of it (#412).
		if err := staysInside(dst, filepath.Dir(victim), rel); err != nil {
			return fmt.Errorf("dir.sync: %v", err)
		}
		if err := os.Remove(victim); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("dir.sync: removing %s: %v", rel, err)
		}
	}
	return nil
}
