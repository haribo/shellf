package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
)

// FilePut writes bytes to Path, binary-safe. Content is base64 and rides in the
// Request — decoded in Go, never through a shell arg/env (which would hit ARG_MAX
// for a large file). To honor `as root`, the decoded bytes are staged in a temp
// file and placed by the (possibly escalated) executor. Idempotent by content
// sha256. Built by `dir-copy`'s control-side resolution (ADR-0028).
type FilePut struct {
	Path    string
	Content string // base64
}

func (f FilePut) Name() string       { return "file-put" }
func (f FilePut) ChangedTag() string { return "written" }

func (f FilePut) decode() ([]byte, error) { return base64.StdEncoding.DecodeString(f.Content) }

func (f FilePut) sum() string {
	b, _ := f.decode()
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (f FilePut) PreCheck() *Result {
	if f.Path == "" {
		r := Err("pathMustNotBeEmpty")
		return &r
	}
	if _, err := f.decode(); err != nil {
		r := Err("badContent")
		return &r
	}
	return nil
}

// Guard: the target's sha256 already equals the content's → skip.
func (f FilePut) Guard(ex Executor) *Result {
	r := ex.Shell(`sha256sum "$dst" 2>/dev/null | cut -d' ' -f1`, Env{"dst": f.Path})
	if r.OK() && strings.TrimSpace(r.Stdout) == f.sum() {
		res := Ok("already")
		return &res
	}
	return nil
}

func (f FilePut) Preview(Executor) *ShellResult { return nil } // binary — nothing to diff

func (f FilePut) Apply(ex Executor) Result {
	b, err := f.decode()
	if err != nil {
		return Err("badContent")
	}
	tmp, err := os.CreateTemp("", "shellf-put-")
	if err != nil {
		return Err("runtime")
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return Err("runtime")
	}
	if err := tmp.Close(); err != nil {
		return Err("runtime")
	}
	// Place it through the (possibly escalated) executor so `as root` is honored;
	// the bytes are in the temp file, never on a command line.
	r := ex.Shell(`mkdir -p "$(dirname "$dst")" && cp "$tmp" "$dst"`, Env{"tmp": tmp.Name(), "dst": f.Path})
	if !r.OK() {
		return ErrShell("runtime", r)
	}
	return Ok(f.ChangedTag())
}
