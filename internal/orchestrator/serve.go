package orchestrator

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shellf/internal/proto"
)

// Serving control-host resources to a running job (ADR-0031 §3).
//
// The rule that makes the channel safe: the control host answers **only** what the plan
// declared. Defs can come from third parties (ADR-0016); without this, an imported def
// could ask for ~/.ssh/id_ed25519 and be served by the machine holding every key and
// every secret. So the allow-list is derived from the plan before it is sent, and
// anything outside it is refused by name.

// Allowed is the set of control-host resources a job may request, resolved to absolute
// paths so a request cannot escape by way of `..` or a symlink-shaped string.
type Allowed struct {
	paths map[string]string // as written in the plan → absolute path on disk

	// Render substitutes `@{var}` over this host's environment (ADR-0024). It lives
	// here because rendering needs the operator's variables, which never leave the
	// control host — the agent sends content and receives the result.
	//
	// The content may come from the target (`~file.render(shell { cat … })`), so what a
	// plan hands to rendering is the plan author's business: the substituted values are
	// this host's, secrets included.
	Render func(content string) (string, error)
}

// NewAllowed builds the set from the resources a plan declared. A declaration is
// `<primitive>:<path>` — `file.read:conf.j2` — so the primitive is part of the key: a
// `dir.list` cannot be answered with a file's contents, and a path declared for reading
// does not become listable. Relative paths resolve against planDir.
func NewAllowed(planDir string, declared []string) *Allowed {
	a := &Allowed{paths: map[string]string{}}
	for _, d := range declared {
		primitive, path, ok := strings.Cut(d, ":")
		if !ok {
			continue // not a resource key: nothing to allow
		}
		p := path
		if !filepath.IsAbs(p) {
			p = filepath.Join(planDir, p)
		}
		if abs, err := filepath.Abs(p); err == nil {
			a.paths[primitive+":"+path] = abs
		}
	}
	return a
}

// resolve returns the absolute path for a requested resource, or false when the plan
// never declared it. Matching is on the string the plan carried, not on the resolved
// path: two different spellings of one file are two declarations, and a request that
// matches none of them is refused whatever it points at.
func (a *Allowed) resolve(resource string) (string, bool) {
	p, ok := a.paths[resource]
	return p, ok
}

// Serve answers one job's requests until the channel closes. It reads, it answers, and
// it never executes: a resource name is a lookup key, never a command.
func Serve(ch *proto.Conn, allow *Allowed) error {
	for {
		m, err := ch.Recv()
		if err != nil {
			return err // includes io.EOF: the peer is gone, this job is over
		}
		if m.Kind != proto.KindAsk {
			continue // hello, or something a newer peer knows and we do not
		}
		data, aerr := answer(allow, m)
		ans := proto.Msg{Kind: proto.KindAnswer, ID: m.ID}
		if aerr != nil {
			ans.Error = aerr.Error()
		} else {
			ans.Data = base64.StdEncoding.EncodeToString(data)
		}
		if err := ch.Send(ans); err != nil {
			return err
		}
	}
}

// answer dispatches an ask: `file.render` is a computation over the message's payload,
// everything else names a file to read.
func answer(allow *Allowed, m proto.Msg) ([]byte, error) {
	if strings.HasPrefix(m.Resource, "file.render:") {
		if allow.Render == nil {
			return nil, fmt.Errorf("no renderer configured on the control host")
		}
		content, err := base64.StdEncoding.DecodeString(m.Data)
		if err != nil {
			return nil, fmt.Errorf("file.render: unreadable content")
		}
		out, err := allow.Render(string(content))
		if err != nil {
			return nil, fmt.Errorf("file.render: %v", err)
		}
		return []byte(out), nil
	}
	return readResource(allow, m.Resource)
}

func readResource(allow *Allowed, resource string) ([]byte, error) {
	path, ok := allow.resolve(resource)
	if !ok {
		// Naming the resource is deliberate: a refusal the operator cannot read is a
		// support ticket. Naming it is safe — the requester already knows what it asked.
		return nil, fmt.Errorf("refused: %q was not declared by the plan", resource)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// Do not leak the absolute path of the control host into a target-visible
		// message; the plan-relative name is what the operator wrote and recognises.
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no such file on the control host")
		}
		return nil, fmt.Errorf("%s", strings.TrimPrefix(err.Error(), path+": "))
	}
	return b, nil
}
