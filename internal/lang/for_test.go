package lang

import (
	"strings"
	"testing"
)

func TestFor_Unrolls(t *testing.T) {
	plan, err := parsePlan(`on host { for port in ["80", "443"] { ufw.open("${port}", "tcp") } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(steps))
	}
	if steps[0].Args["port"] != "80" || steps[0].Args["proto"] != "tcp" {
		t.Fatalf("iter 0: %+v", steps[0].Args)
	}
	if steps[1].Args["port"] != "443" {
		t.Fatalf("iter 1: %+v", steps[1].Args)
	}
}

func TestFor_VarInsideString(t *testing.T) {
	plan, err := parsePlan(`on host { for svc in ["traefik", "app"] { dir.ensure("/opt/${svc}/run") } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Steps[0].Args["path"] != "/opt/traefik/run" || plan[0].Steps[1].Args["path"] != "/opt/app/run" {
		t.Fatalf("var not substituted inside the string: %+v %+v", plan[0].Steps[0].Args, plan[0].Steps[1].Args)
	}
}

func TestFor_EmptyList(t *testing.T) {
	plan, err := parsePlan(`on host { for x in [] { dir.ensure("/x") } dir.ensure("/after") }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan[0].Steps) != 1 || plan[0].Steps[0].Args["path"] != "/after" {
		t.Fatalf("an empty list should unroll to nothing: %+v", plan[0].Steps)
	}
}

func TestFor_Nested(t *testing.T) {
	plan, err := parsePlan(`on host { for a in ["1", "2"] { for b in ["x", "y"] { dir.ensure("/${a}/${b}") } } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if len(steps) != 4 {
		t.Fatalf("nested loop should give 4 steps, got %d", len(steps))
	}
	got := []string{steps[0].Args["path"], steps[1].Args["path"], steps[2].Args["path"], steps[3].Args["path"]}
	want := []string{"/1/x", "/1/y", "/2/x", "/2/y"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nested unroll: got %v want %v", got, want)
		}
	}
}

func TestFor_WithIf(t *testing.T) {
	// The body may hold control flow; it is re-parsed per item.
	plan, err := parsePlan(`on host { for p in ["a"] { if !dir.exists("/${p}") { dir.ensure("/${p}") } } }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan[0].Steps) != 1 || plan[0].Steps[0].If == nil {
		t.Fatalf("if inside for not unrolled: %+v", plan[0].Steps)
	}
}

func TestFor_MissingBrace(t *testing.T) {
	_, err := parsePlan(`on host { for x in ["a"] dir.ensure("/x") }`, map[string]string{}, nil, defaultSig, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "expected '{'") {
		t.Fatalf("a for without a body block must error: %v", err)
	}
}

func TestInterpolate(t *testing.T) {
	look := func(n string) (string, bool) { v := map[string]string{"a": "1", "b": "two"}[n]; return v, v != "" }
	got, err := Interpolate("x=${a}, y=${b}", look)
	if err != nil || got != "x=1, y=two" {
		t.Fatalf("Interpolate: %q %v", got, err)
	}
	if _, err := Interpolate("${missing}", look); err == nil {
		t.Fatal("undefined var must error")
	}
	if _, err := Interpolate("${unclosed", look); err == nil {
		t.Fatal("unterminated must error")
	}
}

func TestTemplate(t *testing.T) {
	look := func(n string) (string, bool) { v := map[string]string{"a": "1"}[n]; return v, v != "" }
	// ~{var} interpolates; ${x} and {{y}} pass through untouched. The escape clause this
	// assertion used to carry — `@@` yielding a literal `@{` — is gone with the sigil it
	// protected (ADR-0049); a literal now uses a verbatim region, asserted below.
	got, err := Template("a=~{a} b=${x} c={{y}}", look)
	if err != nil || got != "a=1 b=${x} c={{y}}" {
		t.Fatalf("Template: %q %v", got, err)
	}
	if _, err := Template("~{missing}", look); err == nil {
		t.Fatal("undefined must error")
	}
	if _, err := Template("~{unclosed", look); err == nil {
		t.Fatal("unterminated must error")
	}
}

// The delimiter is `~{}` (ADR-0049), and a config file's own `${…}` and `{{ … }}` pass
// through untouched — which is what makes shellf able to render a compose file or a
// Traefik config without mangling it.
func TestTemplate_TildeDelimiter(t *testing.T) {
	look := func(n string) (string, bool) {
		v, ok := map[string]string{"domain": "example.com"}[n]
		return v, ok
	}
	got, err := Template("admin@~{domain} ${SHELL} {{ .Go }}", look)
	if err != nil {
		t.Fatal(err)
	}
	// The case that motivated the change: an at-sign immediately before a variable, which
	// `@{}` could not express without reading as an escape (#481).
	if got != "admin@example.com ${SHELL} {{ .Go }}" {
		t.Fatalf("got %q", got)
	}
}

// The sigil has no escape: `~~` is two literal tildes, exactly as a lone `{` is literal in
// Jinja and Go. ADR-0021's `@@` rule is gone with the sigil it protected.
func TestTemplate_SigilIsNotEscaped(t *testing.T) {
	look := func(string) (string, bool) { return "", false }
	got, err := Template("~ ~~ ~x", look)
	if err != nil {
		t.Fatalf("a lone tilde must not be special: %v", err)
	}
	if got != "~ ~~ ~x" {
		t.Fatalf("got %q, want the input unchanged", got)
	}
}

// A verbatim region lets a template document the placeholders it exists to carry — the
// second half of #481.
func TestTemplate_VerbatimRegion(t *testing.T) {
	look := func(n string) (string, bool) {
		v, ok := map[string]string{"domain": "example.com"}[n]
		return v, ok
	}
	src := "before ~{domain}\n~{raw}\n# write ~{domain} to substitute it\n~{endraw}\nafter ~{domain}"
	got, err := Template(src, look)
	if err != nil {
		t.Fatal(err)
	}
	want := "before example.com\n# write ~{domain} to substitute it\nafter example.com"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// An undefined variable inside a region is not an error: nothing in there is looked up.
// That is the whole point — the region exists to hold text that is not a placeholder.
func TestTemplate_VerbatimHidesUndefinedNames(t *testing.T) {
	look := func(string) (string, bool) { return "", false }
	got, err := Template("~{raw}~{nope}~{endraw}", look)
	if err != nil {
		t.Fatalf("a name inside a verbatim region must not be resolved: %v", err)
	}
	if got != "~{nope}" {
		t.Fatalf("got %q", got)
	}
}

// An unclosed region is an error rather than silently swallowing the rest of the file.
func TestTemplate_UnclosedVerbatimIsAnError(t *testing.T) {
	look := func(string) (string, bool) { return "", false }
	if _, err := Template("~{raw}\nforever", look); err == nil {
		t.Fatal("an unterminated verbatim region must be refused")
	}
}
