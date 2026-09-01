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

// #487: `file.replace` built a sed expression out of its arguments, so a value carrying a
// sed metacharacter was not written — it was interpreted. Measured on debian:stable-slim
// before the fix, with the def's own shell:
//
//	file.replace(path, "URL", "https://a&b")  ->  URL=https://aURL=oldb
//
// `&` in a sed replacement means "the whole matched line", so the old content was spliced
// back in. The result never satisfies the observe, so apply re-ran and re-corrupted the
// file on every run: silent corruption *and* a def that cannot converge. A value with `|`
// (the delimiter) failed the run outright.
//
// The key had the mirror defect: `^$key=` is a regex in the apply but `grep -qxF` is
// literal in the observe, so a key holding `.` matched neighbouring lines it was never
// meant to touch.
//
// This runs the real def against a real shell — a fake executor answers whatever the test
// asks and would have passed against the broken version.
func TestFileReplace_SurvivesSedMetacharacters(t *testing.T) {
	for name, tc := range map[string]struct {
		key, value, initial, untouched string
	}{
		"ampersand splices the matched line back in": {
			key: "URL", value: "https://x.test/a?b=1&c=2", initial: "URL=old\n",
		},
		"pipe is the sed delimiter": {
			key: "OPTS", value: "a|b", initial: "OPTS=old\n",
		},
		"slash in a path value": {
			key: "BIN", value: "/usr/local/bin", initial: "BIN=old\n",
		},
		"backslash and a group reference": {
			key: "RE", value: `a\1b`, initial: "RE=old\n",
		},
		"a dot in the key is literal, not any-char": {
			key: "log.level", value: "debug", initial: "logxlevel=keep\nlog.level=old\n",
			untouched: "logxlevel=keep",
		},
		"appending a value with a metacharacter": {
			key: "NEW", value: "a&b", initial: "OTHER=1\n",
			untouched: "OTHER=1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "env")
			if err := os.WriteFile(path, []byte(tc.initial), 0o644); err != nil {
				t.Fatal(err)
			}
			args := map[string]string{"path": path, "key": tc.key, "value": tc.value}

			evalWith(t, "file.replace", args, engine.ShellExecutor{}, engine.Apply)

			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.key + "=" + tc.value
			if !hasLine(string(body), want) {
				t.Fatalf("the value was interpreted, not written.\nwant line: %q\ngot file:\n%s", want, body)
			}
			if tc.untouched != "" && !hasLine(string(body), tc.untouched) {
				t.Fatalf("a neighbouring line was rewritten.\nwant kept: %q\ngot file:\n%s", tc.untouched, body)
			}

			// The observe is `grep -qxF`, so a file the apply did not write exactly as asked
			// never converges: the def would rewrite — and re-corrupt — on every run.
			res := evalWith(t, "file.replace", args, engine.ShellExecutor{}, engine.Apply)
			if res.Tag != "already" {
				t.Fatalf("second run must converge, got %s\nfile:\n%s", res, body)
			}
		})
	}
}

// hasLine reports whether body carries want as a whole line, which is what the def's
// `grep -qxF` observe demands — a substring match would call the corrupted
// `URL=https://aURL=oldb` a success.
func hasLine(body, want string) bool {
	for _, l := range strings.Split(body, "\n") {
		if l == want {
			return true
		}
	}
	return false
}

// The rest of the #487 contract: what the def refuses, what it must not lose, and the
// duplicate lines sed used to leave behind (`s|^K=.*|…|` rewrote *every* matching line).
func TestFileReplace_Contract(t *testing.T) {
	replace := func(t *testing.T, path, key, value string) engine.Result {
		t.Helper()
		return evalWith(t, "file.replace",
			map[string]string{"path": path, "key": key, "value": value},
			engine.ShellExecutor{}, engine.Apply)
	}

	t.Run("creates the file when it is absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		replace(t, path, "K", "v")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the append branch used to create the file: %v", err)
		}
		if string(b) != "K=v\n" {
			t.Fatalf("got %q, want %q", b, "K=v\n")
		}
	})

	t.Run("keeps one line, not the duplicates sed left", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		if err := os.WriteFile(path, []byte("K=a\nOTHER=1\nK=b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		replace(t, path, "K", "v")
		if got, want := string(mustRead(t, path)), "K=v\nOTHER=1\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("preserves the mode of an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		if err := os.WriteFile(path, []byte("K=a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		replace(t, path, "K", "v")
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Fatalf("the rename must inherit the destination's mode: got %o, want 600", got)
		}
	})

	t.Run("leaves no staging file behind", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "env")
		replace(t, path, "K", "v")
		replace(t, path, "K", "w")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "env" {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("staging residue: %v", names)
		}
	})

	// The one argument shape `check` can express with `==` alone. The others (`=` in the
	// key, a newline in either) need a value predicate the language does not have; see the
	// note on the def.
	for name, tc := range map[string]struct{ key, value string }{
		"empty key": {"", "v"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "env")
			if err := os.WriteFile(path, []byte("KEEP=1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			res := replace(t, path, tc.key, tc.value)
			if res.Category != engine.ERR {
				t.Fatalf("want an err verdict, got %s", res)
			}
			if got := string(mustRead(t, path)); got != "KEEP=1\n" {
				t.Fatalf("a refused call must not touch the file: got %q", got)
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// #543: `chmod` accepts `0640` and `640` for the same mode, and `stat -c '%a'` answers
// `640` for both — it strips a leading zero. Comparing its output to the argument verbatim
// made the def apply the mode correctly and then judge itself to have failed, every run:
//
//	file.mode(mode=0640, …) err.unconfirmed
//	  ! the apply ran and the state did not follow — mode: observed "640", desired "0640"
//
// Found by the two-host dogfood (#542) against a real target. No existing plan wrote a
// leading zero — every mode across the e2e and example plans is three digits — so the
// adverse cases (#489) and the hostile arguments (#527) both missed it: they vary the path
// and the starting state, never the *spelling* of the value.
//
// Run against a real shell and a real file, like the archive tests. A fake executor cannot
// prove this one: the defect lives in the observe's script, and a fake answers whatever the
// test hands it — it would pass on the broken def just as readily.
func TestMode_ConvergesOnALeadingZero(t *testing.T) {
	for _, tc := range []struct{ name, mode, want string }{
		{"leading zero", "0640", "640"},
		{"three digits", "640", "640"},
		{"setuid keeps four digits", "4755", "4755"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			def, ok := Lookup("file.mode")
			if !ok {
				t.Fatal("file.mode not found")
			}
			run := func() engine.Result {
				res, err := lang.EvalDefFull(def, map[string]string{"path": path, "mode": tc.mode},
					nil, nil, engine.ShellExecutor{}, engine.Apply, nil, nil, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}

			if got := run().String(); got != "ok.changed" {
				t.Fatalf("first apply of %q: got %s", tc.mode, got)
			}
			// The second run is the assertion this issue is about: the mode is already
			// right, so the def must say so instead of acting and disowning its own work.
			// It also proves the mode landed — `ok.already` is the observe reading it back.
			if got := run().String(); got != "ok.already" {
				t.Fatalf("second apply of %q: got %s, want ok.already", tc.mode, got)
			}
		})
	}
}
