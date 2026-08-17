package transport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #391: the cache probe was `test -x <path>`, at a path derived from the binary's digest
// and the SSH user — both public. Any local user could create that file first and have it
// executed, often to run work under `as root`. These assert the refusal, because a guard
// that fails open is worse than none: it reads as protection while granting the same
// execution.

func TestGuard_ForeignBinaryIsRefusedNotOverwritten(t *testing.T) {
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, "sha256sum") {
			return []byte("foreign mallory:777\n"), nil
		}
		return nil, nil
	}}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	_, err := s.Run(tmpBin(t), []byte(`{}`))
	if err == nil {
		t.Fatal("a binary that is not ours must not be run")
	}
	// Named, so the operator can act: which file, and what was found there.
	for _, want := range []string{"not ours", "mallory:777", "shellf-agent-"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q, got: %v", want, err)
		}
	}
	// Not overwritten: the file is not ours to replace, and pushing over it would be a
	// race we cannot win — the owner can put theirs back between our write and our launch.
	if fc.ran("chmod 700") {
		t.Error("a foreign binary must not be overwritten")
	}
	if len(fc.starts) != 0 {
		t.Errorf("nothing must be launched: %v", fc.starts)
	}
}

// Ours, but different bytes — a partial push, or an older build whose digest prefix
// collided. That is a cache miss, not an attack: re-transfer rather than refuse.
func TestGuard_StaleBinaryIsRetransferred(t *testing.T) {
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		switch {
		case strings.Contains(cmd, "sha256sum"):
			return []byte("stale 0000000000\n"), nil
		case strings.Contains(cmd, "-type d -user"):
			return []byte("ok\n"), nil
		case strings.Contains(cmd, "agent.pid"):
			return nil, nil
		case strings.HasPrefix(cmd, "if test -f "):
			return []byte(`{"ok":true}`), nil
		}
		return nil, nil
	}}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	if _, err := s.Run(tmpBin(t), []byte(`{}`)); err != nil {
		t.Fatalf("a stale cache entry must be replaced, not refused: %v", err)
	}
	if !fc.ran("chmod 700") {
		t.Error("the binary must be pushed again")
	}
}

// The agent runs any `req-*.json` it finds, without asking who wrote it. A workdir another
// user can write to is therefore a way to have a request of their choosing executed,
// `become: root` included — so the request is never deposited there.
func TestGuard_WritableWorkdirIsRefusedBeforeDeposit(t *testing.T) {
	fc := &fakeConn{responder: func(cmd string) ([]byte, error) {
		switch {
		case strings.Contains(cmd, "sha256sum"):
			return []byte("ok\n"), nil
		case strings.Contains(cmd, "-type d -user"):
			return []byte("unsafe root:777\n"), nil
		}
		return nil, nil
	}}
	s := SSH{dialFn: func() (conn, error) { return fc, nil }}

	_, err := s.Run(tmpBin(t), []byte(`{}`))
	if err == nil {
		t.Fatal("a workdir another user can write to must not be used")
	}
	for _, want := range []string{"another user can write", "root:777"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q, got: %v", want, err)
		}
	}
	if fc.ran("req-") {
		t.Error("the request must not be deposited into an unsafe workdir")
	}
}

// The probes must ask the three questions that matter. Asserted on the script rather than
// on behaviour, because a probe that silently stops checking one of them is invisible
// otherwise — `test -x` looked fine for as long as nobody read it.
func TestGuard_ProbesAskOwnershipModeAndDigest(t *testing.T) {
	cmd := agentStateCmd("/tmp/shellf-agent-abc", "deadbeef")
	for _, want := range []string{"-user \"$(id -un)\"", "! -perm /022", "sha256sum", "deadbeef"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the agent probe must check %q", want)
		}
	}
	wd := workdirEnsureCmd("/dev/shm/shellf-abc")
	for _, want := range []string{"-type d", "-user \"$(id -un)\"", "! -perm /022"} {
		if !strings.Contains(wd, want) {
			t.Errorf("the workdir probe must check %q", want)
		}
	}
	// And it must create rather than probe-then-create: `mkdir -p` is what accepted a
	// directory another user had put there in between (#413).
	if !strings.Contains(wd, "mkdir /dev/shm/shellf-abc") || strings.Contains(wd, "mkdir -p") {
		t.Errorf("the workdir must be created exclusively, got: %s", wd)
	}
}

// #413: the workdir was *probed* and then created by a second command. `absent` was
// accepted, and `mkdir -p` on a directory somebody else had created in the meantime
// succeeds without changing its owner or its mode — demonstrated in the e2e container: a
// deposit into a root-owned 0777 directory returned 0 and left it 0777. The agent then
// runs every `req-*.json` it finds there, `as root` steps included.
//
// The fix is to create and check in one command, so the winner is whoever created it.
// Asserted by running the script, not by reading it: what matters is what `sh` does.
func TestGuard_WorkdirIsCreatedExclusivelyThenChecked(t *testing.T) {
	run := func(wd string) (string, error) {
		out, err := exec.Command("sh", "-c", workdirEnsureCmd(wd)).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	base := t.TempDir()

	// Absent: created by us, and private — the umask is part of the guarantee.
	fresh := filepath.Join(base, "shellf-fresh")
	got, err := run(fresh)
	if err != nil || got != "ok" {
		t.Fatalf("an absent workdir must be created and accepted: %q (%v)", got, err)
	}
	fi, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("a workdir we create must be private, got %v", fi.Mode().Perm())
	}

	// Already ours and private: reused, which is the whole point of a resident agent.
	if got, err := run(fresh); err != nil || got != "ok" {
		t.Fatalf("our own workdir must be reused: %q (%v)", got, err)
	}

	// Already there and writable by anyone: refused. This is the state the race produced.
	hostile := filepath.Join(base, "shellf-hostile")
	if err := os.Mkdir(hostile, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hostile, 0o777); err != nil { // umask does not get a say
		t.Fatal(err)
	}
	got, err = run(hostile)
	if err != nil {
		t.Fatalf("the check must answer rather than fail: %v", err)
	}
	if !strings.HasPrefix(got, "unsafe") {
		t.Fatalf("a world-writable workdir must be refused, got %q", got)
	}

	// Not a directory at all: also refused, rather than deposited beside.
	notDir := filepath.Join(base, "shellf-file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := run(notDir); !strings.HasPrefix(got, "unsafe") {
		t.Fatalf("a workdir path that is not a directory must be refused, got %q", got)
	}
}
