package lang

import (
	"strings"
	"testing"
)

// The parser tests cannot import std (that would be an import cycle: std imports
// lang), so they supply the stdlib signatures here. Production derives the same
// from std.Lookup + builtins in cmd/shellf (#107).
func init() { defaultSig = testStdSig }

func testStdSig(name string) ([]Param, int, bool) {
	m := map[string][]string{
		"apt.install":       {"pkg"},
		"file.copy":         {"src", "dst"},
		"service.ensure":    {"name", "running", "enabled"},
		"file.download":     {"url", "dst", "sha256"},
		"archive.extract":   {"src", "dst"},
		"git.clone":         {"url", "dst"},
		"dir.ensure":        {"path"},
		"dir.exists":        {"path"},
		"dir.owner":         {"path", "owner"},
		"file.exists":       {"path"},
		"user.group":        {"user", "group"},
		"file.write":        {"path", "content"},
		"file.line":         {"path", "line"},
		"file.delete":       {"path"},
		"docker.install":    {},
		"docker.network":    {"name"},
		"docker.compose-up": {"dir"},
		"ufw.open":          {"port", "proto"},
		"ufw.enable":        {},
	}
	names, ok := m[name]
	return strParams(names...), len(names), ok // the fake models no optional params
}

// strParams builds a signature of plain `str` parameters, which is what every fake here
// needs: a type is only load-bearing where a def declares `bool` (ADR-0045).
func strParams(names ...string) []Param {
	out := make([]Param, len(names))
	for i, n := range names {
		out[i] = Param{Name: n, Type: "str"}
	}
	return out
}

// #418, ADR-0045 §2 — the check that matters, on the value rather than the spelling.
// Measured before it existed: `service.ensure("cron", "yes", "true")` reported
// `ok.converged` and **stopped** the service, because the apply tests `[ "$running" = true ]`
// and anything that is not exactly "true" means stop.
func TestCall_BoolParameterTakesABooleanValue(t *testing.T) {
	sig := func(string) ([]Param, int, bool) {
		return []Param{{Name: "name", Type: "str"}, {Name: "running", Type: "bool"}}, 2, true
	}

	accepted := map[string]string{
		"bare literal":            `on web { svc.ensure("cron", true) }`,
		"boolean written as text": `on web { svc.ensure("cron", "false") }`,
		"a variable holding one":  "flag = \"true\"\non web { svc.ensure(\"cron\", flag) }",
	}
	for what, src := range accepted {
		t.Run(what, func(t *testing.T) {
			if _, err := ParsePlanWithVars(src, map[string]string{}, nil, sig); err != nil {
				t.Fatalf("a boolean value must be accepted however it is written: %v", err)
			}
		})
	}

	refused := map[string]string{
		"a word that is not a boolean": `on web { svc.ensure("cron", "yes") }`,
		"a number":                     `on web { svc.ensure("cron", "1") }`,
		"the wrong case":               `on web { svc.ensure("cron", "True") }`,
		"a variable holding a word":    "flag = \"yes\"\non web { svc.ensure(\"cron\", flag) }",
	}
	for what, src := range refused {
		t.Run(what, func(t *testing.T) {
			_, err := ParsePlanWithVars(src, map[string]string{}, nil, sig)
			if err == nil {
				t.Fatal("a value that is not a boolean must be refused before a host is contacted")
			}
			if !strings.Contains(err.Error(), "running") {
				t.Fatalf("the refusal must name the parameter: %v", err)
			}
			if !strings.Contains(err.Error(), "true or false") {
				t.Fatalf("the refusal must say what a boolean looks like: %v", err)
			}
		})
	}
}
