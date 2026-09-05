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
