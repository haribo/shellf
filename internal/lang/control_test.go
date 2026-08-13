package lang

import (
	"strings"
	"testing"
)

// parseDef is a thin helper: parse one def and hand back the error, if any.
func parseDef(src string) error {
	_, err := ParseDefs(src)
	return err
}

// ADR-0034 §2: `%` is valid only before a primitive of the closed set. This is the rule
// that keeps shell off the operator's machine — if `%` could prefix a def, a def could
// run shell, and that shell would run where every SSH key lives. So the refusals matter
// more than the acceptances.
func TestControl_PercentOnlyBeforeAPrimitive(t *testing.T) {
	ok := []string{
		`def t(p: str) { apply { x = %file.read(p) } }`,
		`def t(c: str) { apply { x = %file.render(c) } }`,
		`def t(d: str) { apply { x = %dir.list(d) } }`,
		`def t() { apply { x = %"conf.j2" } }`,
	}
	for _, src := range ok {
		if err := parseDef(src); err != nil {
			t.Errorf("must parse: %s\n%v", src, err)
		}
	}

	refused := map[string]string{
		"a def":            `def t() { apply { x = %my-def() } }`,
		"a qualified def":  `def t() { apply { x = %docker.compose-up("/opt") } }`,
		"shell":            `def t() { apply { x = %shell { rm -rf / } } }`,
		"an unknown name":  `def t() { apply { x = %file.write("/etc/x", "y") } }`,
		"nothing":          `def t() { apply { x = % } }`,
		"a bare primitive": `def t(p: str) { apply { x = %file.read } }`,
	}
	for what, src := range refused {
		t.Run(what, func(t *testing.T) {
			err := parseDef(src)
			if err == nil {
				t.Fatalf("%% before %s must be refused: %s", what, src)
			}
		})
	}
}

// The refusal has to name what was written, or the author cannot see what to fix.
func TestControl_RefusalNamesTheOffender(t *testing.T) {
	err := parseDef(`def t() { apply { x = %docker.compose-up("/opt") } }`)
	if err == nil {
		t.Fatal("must be refused")
	}
	if !strings.Contains(err.Error(), "docker.compose-up") {
		t.Fatalf("the error must name the offending call: %v", err)
	}
	if !strings.Contains(err.Error(), "%file.read") {
		t.Fatalf("the error must list what is allowed: %v", err)
	}
}

// `%` belongs outside the quotes: a path that legitimately starts with one is ordinary
// data, and a marker inside a string would vanish when the value comes from a variable.
func TestControl_PercentInsideAStringIsNotAMarker(t *testing.T) {
	defs, err := ParseDefs(`def t() { apply { x = "%/etc/x" } }`)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected one def, got %d", len(defs))
	}
	// It parsed as a plain string: no ControlPath node anywhere.
	if strings.Contains(defs[0].Source, "ControlPath") {
		t.Fatal("a percent inside a string must stay data")
	}
}
