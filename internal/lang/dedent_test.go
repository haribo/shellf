package lang

import "testing"

func TestDedentTriple(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"single line", "foo", "foo"},
		// flush-left, closing """ at col 0: only the trailing delimiter newline is dropped
		{"flush left", "services:\n  fleetd:\n", "services:\n  fleetd:"},
		// content on next lines, uniformly indented, closing """ indented 4
		{"uniform indent", "\n    FROM x\n    COPY y\n    ", "FROM x\nCOPY y"},
		// first line shares the opening """ line (no margin), rest indented: still correct
		{"first line inline", "FROM x\n    COPY y\n    ", "FROM x\nCOPY y"},
		// relative indentation preserved (YAML structure) after removing the common margin
		{"nested indent", "\n    a:\n      b: 1\n    ", "a:\n  b: 1"},
	}
	for _, c := range cases {
		if got := dedentTriple(c.in); got != c.want {
			t.Errorf("%s: dedentTriple(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
