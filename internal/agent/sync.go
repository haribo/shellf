package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shellf/internal/proto"
)

// The agent's half of a tree transfer (ADR-0039): it sends what it already has, then
// writes what the control host streams back.

// Sync transfers a control-host tree into dst and reports how many files were written.
//
// The manifest goes first, so the control host can answer only the delta — that one round
// trip is what buys a converged run its zero bytes, and what keeps a large tree from
// costing one round trip per file.
func (c *Channel) Sync(resource, dst, compare string, del bool) (written, removed int, err error) {
	n, extras, err := c.sync(resource, dst, compare, del, false)
	if !del {
		extras = nil // a copy removes nothing, whatever the terminator listed
	}
	return n, len(extras), err
}

// Preview answers what a transfer *would* do without doing any of it: the delta is
// computed on the control host, no file is streamed, nothing is written and nothing is
// removed. It returns how many files would be written and which would be deleted, so a
// destructive def can name them before acting (#373).
func (c *Channel) Preview(resource, dst, compare string) (int, []string, error) {
	return c.sync(resource, dst, compare, false, true)
}

func (c *Channel) sync(resource, dst, compare string, del, preview bool) (int, []string, error) {
	entries, err := scanTarget(dst, compare)
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
	n, extras, err, stale := c.streamOnce(resource, dst, compare, del, preview, entries)
	if stale {
		entries, serr := scanTarget(dst, compare)
		if serr != nil {
			return 0, nil, serr
		}
		n, extras, err, _ = c.streamOnce(resource, dst, compare, del, preview, entries)
	}
	return n, extras, err
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
		if d.IsDir() || !d.Type().IsRegular() {
			return nil // a symlink is not carried, so it is not compared either
		}
		rel, err := filepath.Rel(dst, p)
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

// streamInto sends the ask and consumes the sequence until the terminator.
//
// Each file is staged beside its destination and renamed once its last chunk lands — the
// rule #298 already imposes on `file.write`: a reader must never catch a partial file. A
// transfer cut short therefore leaves the destination as it was, and the retry's manifest
// lists what did arrive, so only the interrupted file is sent again (ADR-0039 §3).
func (c *Channel) streamOnce(resource, dst, compare string, del, preview bool, entries []proto.Entry) (n int, extras []string, err error, stale bool) {
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
				if staged, err = openStaged(dst, m.Path, m.Mode, m.MTime); err != nil {
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
			if del && !preview {
				if err := removeExtras(dst, m.Delete); err != nil {
					return 0, nil, err, false
				}
			}
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
		if err := os.Remove(victim); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("dir.sync: removing %s: %v", rel, err)
		}
	}
	return nil
}
