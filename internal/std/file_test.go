package std

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
)

// writeFile runs the real file-write def against a real shell.
func writeFile(t *testing.T, path, content string) engine.Result {
	t.Helper()
	def, ok := Lookup("file.write")
	if !ok {
		t.Fatal("file-write not found")
	}
	res, err := lang.EvalDefFull(def, map[string]string{"path": path, "content": content}, nil, nil, engine.ShellExecutor{}, engine.Apply, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Regression for #298: file-write must not truncate the destination in place.
// A shell redirection empties the target before writing it, so every delivery has a
// window in which the file on disk is empty or partial — on the happy path, not only
// when a run is interrupted. Anything reading it in that window (a concurrent reload
// of sshd_config, a restart reading a compose file) reads a broken file.
func TestFileWrite_DoesNotTruncateInPlace(t *testing.T) {
	// observe reports drift (exit 1), so apply runs and its shell is captured.
	f := &fakeExec{observe: drift}
	eval(t, "file.write", map[string]string{"path": "/etc/x", "content": "c"}, f, engine.Apply)

	var apply string
	for _, s := range f.calls {
		if !strings.Contains(s, "cmp") { // the observe is the one comparing
			apply = s
		}
	}
	if apply == "" {
		t.Fatal("no apply shell issued")
	}
	if strings.Contains(apply, `> "$path"`) {
		t.Fatalf("apply redirects straight at the destination, truncating it:\n%s", apply)
	}
	if !strings.Contains(apply, "mv ") {
		t.Fatalf("apply must stage then rename over the destination:\n%s", apply)
	}
}

// The rename must not silently reset permissions. Today a redirection preserves the
// existing mode (it truncates, it does not recreate); a naive temp+mv would replace it
// with the umask default. Measured before the fix: 600 stays 600 across a rewrite.
func TestFileWrite_PreservesModeOnRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")

	writeFile(t, path, "v1")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "v2")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("rewrite must keep the destination's mode: got %o, want 600", got)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "v2" {
		t.Fatalf("content: got %q, want %q", b, "v2")
	}
}

// A staged write must leave nothing behind in the destination's directory.
func TestFileWrite_LeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")

	writeFile(t, path, "v1")
	writeFile(t, path, "v2")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "conf" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("staging residue left in the directory: %v", names)
	}
}

// #367: `dir.owner` never converged. Its observe read `stat -c '%U:%G'` — `covuser:deploy`
// — and compared it to the argument, `covuser`. The two can only be equal when the caller
// writes `"user:group"`, so every run reported `changed` and every `if x.changed { … }`
// gated on it fired for nothing: the exact false positive idempotence exists to prevent.
//
// Found by running the def twice against a real target (#367's coverage plan). No unit
// test had ever done that, because the fake executor answers whatever the test wants.
func TestDirOwner_ConvergesOnAMatchingOwner(t *testing.T) {
	for name, tc := range map[string]struct {
		stat, want string
		converged  bool
	}{
		"user only, matching":       {"covuser:deploy", "covuser", true},
		"user and group, matching":  {"covuser:covuser", "covuser:covuser", true},
		"user only, different":      {"root:root", "covuser", false},
		"user and group, different": {"covuser:deploy", "covuser:covuser", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := &ownerFake{stat: tc.stat}
			res := evalWith(t, "dir.owner", map[string]string{"path": "/tmp/x", "owner": tc.want}, f, engine.Apply)
			if tc.converged && res.Tag != "already" {
				t.Fatalf("owner already %q, asked for %q: want already, got %s", tc.stat, tc.want, res)
			}
			if !tc.converged && res.Tag == "already" {
				t.Fatalf("owner is %q, asked for %q: must not report already", tc.stat, tc.want)
			}
		})
	}
}

// ownerFake answers the ownership probe the way a target would, and reports whether the
// chown ran — the fakes in std_test.go key on script text, and this one needs the
// comparison done by the shell rather than asserted by the test.
type ownerFake struct {
	stat    string
	chowned bool
}

func (o *ownerFake) As(string) engine.Executor    { return o }
func (o *ownerFake) Using(string) engine.Executor { return o }

func (o *ownerFake) Shell(script string, env engine.Env) engine.ShellResult {
	if strings.Contains(script, "chown") {
		o.chowned = true
		return engine.ShellResult{Exit: 0}
	}
	// The observe: the def compares inside the shell, so answer the comparison.
	cur := o.stat
	want := env["owner"]
	if cur == want || strings.SplitN(cur, ":", 2)[0] == want {
		return engine.ShellResult{Exit: 0, Stdout: cur}
	}
	return engine.ShellResult{Exit: 1, Stdout: cur}
}
