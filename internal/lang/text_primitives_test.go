package lang

import (
	"strings"
	"testing"
)

// ADR-0055. The primitives exist so a def can ask something of a value other than
// "is it equal to that one?" — the gap that left `file.replace` unable to refuse a key
// holding a `=` (#487, #492) and made `sudo` shell out to the target to inspect a string
// the control host had passed in.

// Purity is the property under test: `fetch` is nil, so a run reaching for the control
// host would fail here. A def must be able to check its arguments on any run.
func TestText_MatchesNeedsNoControlHost(t *testing.T) {
	src := `def t(k: str) {
	check {
		if ~text.matches(k, "=") { return err.hasEquals }
		return ok.clean
	}
}`
	for _, c := range []struct{ key, want string }{
		{"PORT", "ok.clean"},
		{"a=b", "err.hasEquals"},
	} {
		res, err := evalWithFetch(t, src, "t", map[string]string{"k": c.key}, nil)
		if err != nil {
			t.Fatalf("%q: %v", c.key, err)
		}
		if got := res.String(); got != c.want {
			t.Fatalf("key %q: got %s, want %s", c.key, got, c.want)
		}
	}
}

func TestText_ReplaceRewritesEveryOccurrence(t *testing.T) {
	src := `def t(s: str) {
	check {
		x = ~text.replace(s, "[0-9]+", "N")
		if x == "aN-bN" { return ok.rewritten }
		return err.got
	}
}`
	res, err := evalWithFetch(t, src, "t", map[string]string{"s": "a12-b345"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.String(); got != "ok.rewritten" {
		t.Fatalf("got %s — every occurrence must be replaced", got)
	}
}

// The decision of ADR-0055 §3, and the reason it is a decision: `file.replace` used to
// build a `sed` expression from its arguments, where `&` means "the whole match", so
// `URL=https://a&b` landed as `URL=https://aURL=oldb` (#487). A replacement with its own
// expansion syntax is that defect in a new spelling.
//
// `${…}` is not in the case: that one is shellf's own interpolation, applied to the
// string literal before the primitive ever sees it. What is asserted here is what the
// two regex engines would have expanded — `&` in sed, `$1` in Go.
func TestText_ReplacementIsLiteral(t *testing.T) {
	src := `def t(s: str) {
	check {
		x = ~text.replace(s, "b", "&$1")
		if x == "a&$1c" { return ok.literal }
		return err.expanded
	}
}`
	res, err := evalWithFetch(t, src, "t", map[string]string{"s": "abc"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.String(); got != "ok.literal" {
		t.Fatalf("got %s — the replacement must be substituted verbatim", got)
	}
}

// A pattern written in the source cannot compile: that is a fault in the def, reported
// where it is written rather than on a target mid-deploy.
func TestText_LiteralPatternIsCompiledAtParse(t *testing.T) {
	err := parseDef(`def t(s: str) { check { if ~text.matches(s, "a(") { return err.x } return ok.y } }`)
	if err == nil {
		t.Fatal("an uncompilable literal pattern must be refused at parse")
	}
	if !strings.Contains(err.Error(), "text.matches") {
		t.Fatalf("the refusal must name the primitive: %v", err)
	}
}

// A pattern that arrives as a parameter is only knowable when the def runs, so it fails
// there — naming the primitive, not the transport.
func TestText_PatternFromAParameterFailsAtEval(t *testing.T) {
	src := `def t(s: str, p: str) { check { if ~text.matches(s, p) { return err.x } return ok.y } }`
	_, err := evalWithFetch(t, src, "t", map[string]string{"s": "abc", "p": "a("}, nil)
	if err == nil {
		t.Fatal("an uncompilable pattern must fail the evaluation")
	}
	if !strings.Contains(err.Error(), "text.matches") {
		t.Fatalf("the failure must name the primitive: %v", err)
	}
}

func TestText_Arity(t *testing.T) {
	for _, c := range []struct{ what, src string }{
		{"matches with one argument", `def t(s: str) { check { if ~text.matches(s) { return err.x } return ok.y } }`},
		{"replace with two arguments", `def t(s: str) { check { x = ~text.replace(s, "a") return ok.y } }`},
	} {
		t.Run(c.what, func(t *testing.T) {
			if _, err := evalWithFetch(t, c.src, "t", map[string]string{"s": "abc"}, nil); err == nil {
				t.Fatal("wrong arity must be refused")
			}
		})
	}
}
