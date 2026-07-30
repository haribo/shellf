package transport

import (
	"strings"
	"testing"
)

func TestRemotePath_DeterministicByBytes(t *testing.T) {
	a := remotePath([]byte("binary-v1"))
	b := remotePath([]byte("binary-v1"))
	c := remotePath([]byte("binary-v2"))

	if a != b {
		t.Fatalf("same bytes must give the same cache path: %s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("different bytes must give different cache paths: %s == %s", a, c)
	}
	if !strings.HasPrefix(a, "/tmp/shellf-agent-") {
		t.Fatalf("unexpected path: %s", a)
	}
}
