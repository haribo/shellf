package transport

import (
	"strings"
	"testing"
)

func TestRemotePath_DeterministicByBytesAndUser(t *testing.T) {
	s := SSH{User: "deploy"}
	a := s.remotePath([]byte("binary-v1"))
	b := s.remotePath([]byte("binary-v1"))
	c := s.remotePath([]byte("binary-v2"))

	if a != b {
		t.Fatalf("same bytes+user must give the same cache path: %s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("different bytes must give different cache paths: %s == %s", a, c)
	}
	if !strings.HasPrefix(a, "/tmp/shellf-agent-") {
		t.Fatalf("unexpected path: %s", a)
	}
	// Different SSH user → different agent path (issue #114): no cross-user reuse.
	if root := (SSH{User: "root"}).remotePath([]byte("binary-v1")); root == a {
		t.Fatalf("different users must not share an agent path: %s", root)
	}
}
