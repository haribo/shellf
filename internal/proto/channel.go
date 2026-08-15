package proto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// The control channel (ADR-0031): while a job runs, the agent may ask the control host
// for a resource the plan declared. One JSON object per line — a tolerant framing, so a
// resident agent that predates the client can ignore a field it does not know, where a
// strict binary encoding would turn a version skew into a crash.
//
// Data only. A message never carries something to execute: the control host reads and
// answers, it never runs a shell on the operator's machine.
const (
	KindHello  = "hello"  // version handshake, first message each way
	KindAsk    = "ask"    // agent -> control: give me this resource
	KindAnswer = "answer" // control -> agent: here it is, or why not

	// A tree transfer (ADR-0039): the answer to a `dir.sync` ask is a *sequence*, not one
	// message. `KindAnswer` stays terminal for every other primitive.
	KindFile = "file" // control -> agent: this file, then its chunks
	KindDone = "done" // control -> agent: the transfer is complete
)

// ChannelVersion is the wire version. Bumped when a change is not backward compatible;
// a peer announcing a different one is refused rather than misread.
const ChannelVersion = 1

// Msg is one line of the channel.
type Msg struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"` // an answer repeats its ask's id

	Version int `json:"version,omitempty"` // hello

	Resource string `json:"resource,omitempty"` // ask: what is wanted

	// Data is base64 both ways: on an ask it is the primitive's input (the content
	// `file.render` must substitute); on an answer it is the result. Base64 so the
	// channel carries bytes — a template is text, a delivered file may not be — without
	// a second framing. An answer carries exactly one of Data or Error.
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`

	// Path names the file a `file` message opens, relative to the transfer's destination.
	// Its chunks are the `data` of the messages that follow, until the next `file` or the
	// `done` — order is the framing, so no sequence number is needed on a stream that
	// cannot reorder.
	Path  string `json:"path,omitempty"`
	Mode  string `json:"mode,omitempty"`  // octal, as `stat -c %a` prints it
	MTime int64  `json:"mtime,omitempty"` // the source's, applied on commit — see below
	Last  bool   `json:"last,omitempty"`  // this message closes Path

	// Entries is the manifest an agent sends with a `dir.sync` ask: what it already has
	// under the destination. The control host answers only what differs (ADR-0039 §1).
	Entries []Entry `json:"entries,omitempty"`

	// Written and Delete ride the terminator. Written is what the transfer sent, so a
	// truncated stream is detected rather than mistaken for a finished one; Delete is the
	// target paths absent from the source, applied only when the caller asked for it —
	// carried here, so a transfer that never terminates deletes nothing (ADR-0039 §5).
	Written int      `json:"written,omitempty"`
	Delete  []string `json:"delete,omitempty"`

	// Vars carries the variables in scope where the primitive was called, for an ask
	// that substitutes (`file.render`). The control host owns the host environment and
	// the secrets; the caller owns its params and its `with` override (ADR-0022), and a
	// template needs both. These values came from the plan in the first place, so
	// sending them back is a return trip, not a disclosure.
	Vars map[string]string `json:"vars,omitempty"`
}

// Conn is a channel endpoint: line-delimited JSON over any duplex stream.
type Conn struct {
	r *bufio.Reader
	w io.Writer
	c io.Closer
}

func NewConn(rw io.ReadWriteCloser) *Conn {
	return &Conn{r: bufio.NewReader(rw), w: rw, c: rw}
}

// NewConnRW builds an endpoint from separate halves, for a bridge whose input and
// output are different streams (stdin/stdout).
func NewConnRW(r io.Reader, w io.Writer) *Conn {
	return &Conn{r: bufio.NewReader(r), w: w}
}

func (c *Conn) Send(m Msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// Recv reads the next message. It returns io.EOF when the peer is gone — which is the
// reason this is a socket and not a named pipe: a job waiting on an answer must be able
// to learn that nobody will answer, and fail naming what it waited for (ADR-0031 §2).
func (c *Conn) Recv() (Msg, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return Msg{}, err
	}
	var m Msg
	if err := json.Unmarshal(line, &m); err != nil {
		return Msg{}, fmt.Errorf("channel: malformed message: %v", err)
	}
	return m, nil
}

func (c *Conn) Close() error {
	if c.c != nil {
		return c.c.Close()
	}
	return nil
}

// Handshake exchanges hellos and refuses a peer announcing another wire version.
func (c *Conn) Handshake() error {
	if err := c.Send(Msg{Kind: KindHello, Version: ChannelVersion}); err != nil {
		return err
	}
	m, err := c.Recv()
	if err != nil {
		return err
	}
	if m.Kind != KindHello {
		return fmt.Errorf("channel: expected %s, got %q", KindHello, m.Kind)
	}
	if m.Version != ChannelVersion {
		return fmt.Errorf("channel: peer speaks version %d, this build speaks %d — the agent predates this client, run `shellf clean` to replace it", m.Version, ChannelVersion)
	}
	return nil
}

// Entry is one file in a transfer manifest (ADR-0039 §1). Metadata only: the agent says
// what it has, never what it holds. SHA is empty unless the caller asked to compare by
// digest — computing one over a tree that only needs size and mtime is the cost the
// default exists to avoid.
type Entry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // Unix seconds; second precision is what a tar or a copy preserves
	SHA   string `json:"sha,omitempty"`
}
