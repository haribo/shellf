package agentbin

import (
	"runtime"
	"strings"
	"testing"
)

func TestArchFromUname(t *testing.T) {
	for uname, want := range map[string]string{
		"x86_64":  "amd64",
		"aarch64": "arm64",
		"arm64":   "arm64",
	} {
		got, err := ArchFromUname(uname + "\n") // as it arrives, with its newline
		if err != nil {
			t.Fatalf("%s: %v", uname, err)
		}
		if got != want {
			t.Fatalf("%s → %s, want %s", uname, got, want)
		}
	}
}

// An architecture shellf does not know must be named, not guessed. A guess is how the
// wrong binary lands and the target answers `exec format error` (#453).
func TestArchFromUname_UnknownIsNamed(t *testing.T) {
	_, err := ArchFromUname("riscv64")
	if err == nil {
		t.Fatal("an unknown architecture must be an error")
	}
	if !strings.Contains(err.Error(), "riscv64") {
		t.Fatalf("the error must name what the target reported: %v", err)
	}
}

// The control host's own architecture always resolves, with no embedded peer needed —
// that is the path every build takes today and it must not regress.
func TestFor_SelfNeedsNoPeer(t *testing.T) {
	b, err := For(runtime.GOARCH, []byte("self-bytes"))
	if err != nil {
		t.Fatalf("the running architecture must always resolve: %v", err)
	}
	if string(b) != "self-bytes" {
		t.Fatalf("it must be the running binary's own bytes, got %q", b)
	}
}

// Without the `bundled` tag there is no peer, and asking for one is a refusal naming
// both architectures — never a push of bytes the target cannot exec.
func TestFor_ForeignArchWithoutPeerIsRefused(t *testing.T) {
	foreign := "arm64"
	if runtime.GOARCH == "arm64" {
		foreign = "amd64"
	}
	_, err := For(foreign, []byte("self-bytes"))
	if hasPeer {
		if err != nil {
			t.Fatalf("a build carrying a peer must resolve the foreign architecture: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("a bare build must refuse a foreign architecture, not push its own bytes")
	}
	if !strings.Contains(err.Error(), foreign) {
		t.Fatalf("the refusal must name the architecture asked for: %v", err)
	}
}
