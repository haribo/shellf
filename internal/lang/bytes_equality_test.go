package lang

import (
	"strings"
	"testing"
)

// #578. ADR-0034 §4 decided bytes are opaque: they travel from a primitive to an
// instruction and "cannot be interpolated into `${…}`, compared, or printed in a report".
// The refusal was enforced at the argument boundary (refuseBytes) and nowhere else, so
// `==` had two ways to be wrong instead of one refusal:
//
//   - two Bytes  → `comparing uncomparable type lang.Bytes`, a panic through the evaluator
//   - Bytes vs a string → silently false, whatever the content, which is the shape of #411
//
// Both must now say what is wrong. Do not relax these into a content comparison without
// amending ADR-0034 §4 first: a def comparing a file it read against a literal is treating
// binary as text, which is the decision's whole subject.

func bytesFetch(string, []byte, map[string]string) ([]byte, error) { return []byte("abc"), nil }

func TestBytes_ComparingTwoOfThemIsRefused(t *testing.T) {
	src := `def t(p: str) {
	check {
		x = ~file.read(p)
		y = ~file.read(p)
		if x == y { return ok.same }
		return err.diff
	}
}`
	_, err := evalWithFetchControl(t, src, "t", map[string]string{"p": "c.j2"}, []string{"p"}, bytesFetch)
	if err == nil {
		t.Fatal("comparing two byte values must be refused (ADR-0034 §4), not answered")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("the refusal must name what cannot be compared: %v", err)
	}
}

// The silent half. `x == "abc"` used to be false because Go compares dynamic types first —
// an accident of the host language, not an answer, and one a def author reads as "the
// contents differ".
func TestBytes_ComparingWithAStringIsRefused(t *testing.T) {
	src := `def t(p: str) {
	check {
		x = ~file.read(p)
		if x == "abc" { return ok.same }
		return err.diff
	}
}`
	_, err := evalWithFetchControl(t, src, "t", map[string]string{"p": "c.j2"}, []string{"p"}, bytesFetch)
	if err == nil {
		t.Fatal("comparing bytes with a string must be refused, not silently false")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("the refusal must name what cannot be compared: %v", err)
	}
}

// `!=` is the same comparison; refusing one and answering the other would leave the hole
// open under a different spelling.
func TestBytes_InequalityIsRefusedToo(t *testing.T) {
	src := `def t(p: str) {
	check {
		x = ~file.read(p)
		if x != "abc" { return ok.differ }
		return err.same
	}
}`
	if _, err := evalWithFetchControl(t, src, "t", map[string]string{"p": "c.j2"}, []string{"p"}, bytesFetch); err == nil {
		t.Fatal("`!=` on bytes must be refused as `==` is")
	}
}

// #616. #578 enumerated one uncomparable kind and left the others, which is why this came
// back: `engine.ShellResult` and `engine.Result` both carry slices or maps, so `a == b`
// over two of either **panicked the evaluator** rather than answering.
//
// The rule is now the other way round — `==` accepts the scalar kinds and refuses the rest
// by name — so a kind added later is refused, not fatal. Do not turn this back into a list
// of what is forbidden.

func TestEquality_RefusesEveryUncomparableKind(t *testing.T) {
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return []byte("abc"), nil }
	cases := map[string]struct{ src, want string }{
		"two shell results": {
			src:  `def t() { check { a = shell { true } b = shell { true } if a == b { return ok.same } return err.diff } }`,
			want: "shell result",
		},
		"two def results": {
			src: `def helper() { check { return ok.done } }
def t() { check { a = helper() b = helper() if a == b { return ok.same } return err.diff } }`,
			want: "outcome",
		},
		// Answered false in silence before: a def author writing this is testing success and
		// getting a condition that never fires. ADR-0010 says `if r`, and the message says so.
		"a shell result against ok": {
			src:  `def t() { check { a = shell { true } if a == ok { return ok.same } return err.diff } }`,
			want: "if r",
		},
		"a shell result against a string": {
			src:  `def t() { check { a = shell { true } if a == "x" { return ok.same } return err.diff } }`,
			want: "shell result",
		},
	}
	for what, c := range cases {
		t.Run(what, func(t *testing.T) {
			_, err := evalWithFetch(t, c.src, "t", map[string]string{}, fetch)
			if err == nil {
				t.Fatal("an uncomparable operand must be refused, not answered or crashed")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal must say what to do instead, got: %v", err)
			}
		})
	}
}

// The scalars keep working — this is a whitelist, and it has to let the ordinary case
// through or every def stops parsing.
func TestEquality_ScalarsStillCompare(t *testing.T) {
	src := `def t(k: str, n: str) {
	check {
		if k == "" { return err.empty }
		if k == n { return ok.same }
		if k != n { return ok.differ }
		return err.unreachable
	}
}`
	for _, c := range []struct{ k, n, want string }{
		{"", "x", "err.empty"},
		{"a", "a", "ok.same"},
		{"a", "b", "ok.differ"},
	} {
		res, err := evalWithFetch(t, src, "t", map[string]string{"k": c.k, "n": c.n}, nil)
		if err != nil {
			t.Fatalf("%q vs %q: %v", c.k, c.n, err)
		}
		if got := res.String(); got != c.want {
			t.Fatalf("%q vs %q: got %s, want %s", c.k, c.n, got, c.want)
		}
	}
}
