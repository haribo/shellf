package transport

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"shellf/internal/proto"
)

// The local transport gets the same channel as SSH (ADR-0027): a plan behaves the same
// whether its target is remote or the control host itself, including having its `%`
// requests checked. Without this the primitives would work over SSH and silently fail
// on a local host.
func TestLocal_ChannelIsServed(t *testing.T) {
	bin := buildStubAgent(t)
	served := make(chan string, 1)
	l := Local{Channel: func(r io.Reader, w io.WriteCloser) error {
		c := proto.NewConnRW(r, w)
		if err := c.Handshake(); err != nil {
			return err
		}
		m, err := c.Recv()
		if err != nil {
			return err
		}
		served <- m.Resource
		return c.Send(proto.Msg{Kind: proto.KindAnswer, ID: m.ID, Data: "b2s="})
	}}

	if _, err := l.Run(bin, []byte(`{"mode":"apply","steps":[]}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	select {
	case got := <-served:
		if got != "file.read:conf.j2" {
			t.Fatalf("the served resource: %q", got)
		}
	default:
		t.Fatal("the local transport must serve the channel")
	}
}

// A plan asking nothing opens no socket: the single-process behaviour is untouched.
func TestLocal_NoChannelNoSocket(t *testing.T) {
	bin := buildStubAgent(t)
	if _, err := (Local{}).Run(bin, []byte(`{"mode":"apply","steps":[]}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestLocal_SockBasePrefersShm(t *testing.T) {
	got := sockBase()
	if _, err := os.Stat("/dev/shm"); err == nil {
		if got != "/dev/shm" {
			t.Fatalf("a RAM-backed dir keeps the socket path short and off disk: %q", got)
		}
		return
	}
	if got == "" {
		t.Fatal("sockBase must always return a directory")
	}
}

// buildStubAgent compiles a tiny agent that connects to the socket it is given, asks one
// resource, and prints an empty response — enough to drive the bridge without the real
// agent's machinery.
func buildStubAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := dir + "/main.go"
	if err := os.WriteFile(src, []byte(stubAgentSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := dir + "/stub"
	out, err := runGo(t, "build", "-o", bin, src)
	if err != nil {
		t.Skipf("cannot build the stub agent: %v: %s", err, out)
	}
	return bin
}

const stubAgentSrc = `package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"bufio"
)

func main() {
	if len(os.Args) > 2 {
		sock := os.Args[2] + "/sock"
		l, err := net.Listen("unix", sock)
		if err == nil {
			defer l.Close()
			if c, err := l.Accept(); err == nil {
				r := bufio.NewReader(c)
				_, _ = r.ReadBytes('\n') // hello
				fmt.Fprintln(c, ` + "`" + `{"kind":"hello","version":1}` + "`" + `)
				fmt.Fprintln(c, ` + "`" + `{"kind":"ask","id":"1","resource":"file.read:conf.j2"}` + "`" + `)
				_, _ = r.ReadBytes('\n') // answer
				c.Close()
			}
		}
	}
	var v any
	_ = json.NewDecoder(os.Stdin).Decode(&v)
	fmt.Println(` + "`" + `{"results":[]}` + "`" + `)
}
`

func runGo(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := execCommand("go", args...)
	return out, err
}

func execCommand(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	var b strings.Builder
	c.Stdout, c.Stderr = &b, &b
	err := c.Run()
	return b.String(), err
}
