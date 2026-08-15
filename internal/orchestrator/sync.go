package orchestrator

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"shellf/internal/proto"
)

// The control host's half of a tree transfer (ADR-0039): it receives what the agent
// already has, and sends only what differs. The side that owns the files decides what to
// push, so there is no per-file negotiation and no round trip beyond the first.

// chunkSize bounds one `file` message's payload before base64. 256 KiB keeps a message
// well under any line-length assumption while making the per-message overhead
// negligible; a var so a test can force chunking without a large fixture.
var chunkSize = 256 * 1024

// serveSync answers a `dir.sync:<path>` ask by streaming the delta, then a terminator.
// It writes directly to the connection because a transfer is a sequence, unlike every
// other primitive whose answer is one message.
func serveSync(ch *proto.Conn, allow *Allowed, m proto.Msg) error {
	src, ok := allow.resolve(m.Resource)
	if !ok {
		return ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: m.ID,
			Error: fmt.Sprintf("refused: %q was not declared by the plan", m.Resource)})
	}
	compare := m.Vars["compare"]
	if compare == "" {
		compare = "meta"
	}

	have := make(map[string]proto.Entry, len(m.Entries))
	for _, e := range m.Entries {
		have[e.Path] = e
	}

	source, err := walkSource(src, compare)
	if err != nil {
		return ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: m.ID, Error: err.Error()})
	}

	// Sorted, so a transfer of the same tree is byte-identical run to run: a diff that
	// moves with directory order is a diff nobody can read.
	paths := make([]string, 0, len(source))
	for p := range source {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	written := 0
	for _, rel := range paths {
		if same(have[rel], source[rel], compare) {
			continue
		}
		if err := sendFile(ch, m.ID, src, rel, source[rel]); err != nil {
			return err
		}
		written++
	}

	// The extras are computed here, from the manifest already received, and ride the
	// terminator: a transfer that never terminates deletes nothing (ADR-0039 §5).
	var extra []string
	for p := range have {
		if _, inSource := source[p]; !inSource {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)

	return ch.Send(proto.Msg{Kind: proto.KindDone, ID: m.ID, Written: written, Delete: extra})
}

// walkSource lists the regular files under root, keyed by their path relative to it.
//
// A symlink is skipped rather than followed: following one would let a link inside
// `assets/` deliver a file from anywhere on the operator's disk, which is what the
// allow-list exists to prevent (ADR-0031 §3). Directories are not carried — the agent
// creates what it needs to write a file, so an empty directory does not survive a
// transfer. Stated because it is a real limit, not an oversight.
func walkSource(root, compare string) (map[string]proto.Entry, error) {
	out := map[string]proto.Entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
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
		out[e.Path] = e
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dir.sync: %v", err)
	}
	return out, nil
}

// same reports whether the target's entry already matches the source's, under the
// requested comparison. A missing entry (zero value) never matches: Size 0 and MTime 0
// cannot collide with a real file's, and an empty SHA is only equal to another empty one,
// which walkSource never produces under `sha256`.
func same(have, want proto.Entry, compare string) bool {
	if have.Path == "" {
		return false
	}
	if compare == "sha256" {
		return have.SHA != "" && have.SHA == want.SHA
	}
	return have.Size == want.Size && have.MTime == want.MTime
}

// sendFile streams one file: an opening message carrying its path and mode, then its
// chunks, the last of which is flagged. Order is the framing — the socket cannot
// reorder — so no sequence number is needed.
//
// An empty file still gets one chunk, flagged last: without it the agent would see a
// `file` never closed and would be right to call the transfer truncated.
func sendFile(ch *proto.Conn, id, root, rel string, e proto.Entry) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	f, err := os.Open(full)
	if err != nil {
		return ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: id, Error: fmt.Sprintf("dir.sync: %v", err)})
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: id, Error: fmt.Sprintf("dir.sync: %v", err)})
	}
	mode := strconv.FormatUint(uint64(info.Mode().Perm()), 8)
	// The source's mtime travels with the file and is applied on commit: `meta` compares
	// the destination's mtime against the source's, so a destination stamped with its own
	// write time would differ from the source on every run and be re-sent forever. Caught
	// by a round-trip test, not by reasoning — the first version of this code did exactly
	// that, and a manifest built from the source hid it.
	if err := ch.Send(proto.Msg{Kind: proto.KindFile, ID: id, Path: rel, Mode: mode,
		MTime: e.MTime}); err != nil {
		return err
	}

	buf := make([]byte, chunkSize)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			last := rerr == io.EOF
			if err := ch.Send(proto.Msg{Kind: proto.KindFile, ID: id, Path: rel, Last: last,
				Data: base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
				return err
			}
			if last {
				return nil
			}
		}
		if rerr == io.EOF {
			// Empty file, or a read that ended exactly on a chunk boundary: close it.
			return ch.Send(proto.Msg{Kind: proto.KindFile, ID: id, Path: rel, Last: true})
		}
		if rerr != nil {
			return ch.Send(proto.Msg{Kind: proto.KindAnswer, ID: id, Error: fmt.Sprintf("dir.sync: %v", rerr)})
		}
	}
}

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
