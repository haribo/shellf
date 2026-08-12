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

	// answer: exactly one of Data or Error. Data is base64 so the channel carries
	// bytes (a template is text, a delivered file may not be) without a second framing.
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
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
