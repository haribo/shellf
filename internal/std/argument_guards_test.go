package std

import (
	"testing"

	"shellf/internal/engine"
)

// ADR-0055 / #575. Both guards below used to be impossible to write: the language could
// only compare a value for equality, so the only way to ask "does this hold a `=`" was a
// shell on the target — which also made them untestable here, since the fake answers every
// non-apply shell identically.
//
// That the fake is used at all is the point: no shell reaches it during these checks.

func TestGuards_FileReplaceRefusesAKeyItCannotWrite(t *testing.T) {
	base := map[string]string{"path": "/etc/app.env", "key": "PORT", "value": "8080"}
	with := func(field, v string) map[string]string {
		m := map[string]string{}
		for k, x := range base {
			m[k] = x
		}
		m[field] = v
		return m
	}

	cases := []struct {
		what, field, value, want string
	}{
		// `replace(f, "a=b", "v")` matches no line, appends `a=b=v`, and the file ends
		// up with two lines starting with `a=` — so `a` silently changes value (#487).
		{"a key holding the separator", "key", "a=b", "err.keyMustNotContainEquals"},
		{"a key holding a newline", "key", "a\nb", "err.keyMustNotContainNewline"},
		{"a value holding a newline", "value", "a\nb", "err.valueMustNotContainNewline"},
		{"an empty key", "key", "", "err.keyMustNotBeEmpty"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			got := eval(t, "file.replace", with(c.field, c.value), &fakeExec{observe: converged, applyMatch: "awk"}, engine.Apply).String()
			if got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}

	// The ordinary case still runs: a value may hold a `=`, only a key may not.
	if got := eval(t, "file.replace", with("value", "a=b"), &fakeExec{observe: converged, applyMatch: "awk"}, engine.Apply).String(); got != "ok.already" {
		t.Fatalf("a value holding `=` is ordinary input: got %s", got)
	}
}

func TestGuards_SudoWriteRefusesANameItCannotFile(t *testing.T) {
	args := func(name string) map[string]string {
		return map[string]string{"name": name, "content": "deploy ALL=(ALL) NOPASSWD: ALL"}
	}
	for _, name := range []string{"", "web app", "../../etc/passwd", "a.b"} {
		t.Run(name, func(t *testing.T) {
			if got := eval(t, "sudo.write", args(name), &fakeExec{observe: drift, apply: converged}, engine.Apply).String(); got != "err.badName" {
				t.Fatalf("name %q: got %s, want err.badName", name, got)
			}
		})
	}
}
