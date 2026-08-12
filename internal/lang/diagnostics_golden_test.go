package lang

import "testing"

// Golden diagnostics: the parser/lexer error messages are the language's UX
// (they cite line:col and drive the CLI's feedback), so they are locked here
// verbatim. A change to any wording or position must update this table
// deliberately — it must never regress silently. See issue #131.

func TestGoldenDiagnostics_Plan(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"undefined-binding",
			"x = nope\non s { file.write(\"/p\", x) }",
			`2:1: undefined variable "nope"`},
		{"empty-target",
			`on { }`,
			`1:4: expected group or host, got "{"`},
		{"bad-statement",
			`on s { 123 }`,
			`1:8: expected instruction or 'parallel', got "123"`},
		{"missing-comma",
			`on s { file.write("/p" "q") }`,
			`1:24: expected ), got "q"`},
		{"unterminated-interpolation",
			"owner = \"${x\non s { dir.owner(\"/o\", owner) }",
			`1:9: unterminated ${...} interpolation`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParsePlan(c.src)
			assertDiag(t, err, c.want)
		})
	}
}

func TestGoldenDiagnostics_Def(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"unless-without-brace",
			"def d() { apply { r = shell { echo } unless echo\nreturn ok } }",
			`1:45: expected '{' after unless`},
		{"bad-phase",
			`def d() { badphase { } }`,
			`1:11: expected a phase, got "badphase"`},
		{"missing-expression",
			`def d() { apply { x = } }`,
			`1:23: expected an expression, got "}"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseDefs(c.src)
			assertDiag(t, err, c.want)
		})
	}
}

func assertDiag(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error %q, got nil", want)
	}
	if err.Error() != want {
		t.Fatalf("diagnostic drift:\n got %q\nwant %q", err.Error(), want)
	}
}
