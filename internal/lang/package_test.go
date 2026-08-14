package lang

import (
	"strings"
	"testing"
)

func pkg(t *testing.T, planSrc string, libs map[string]string) (map[string]Def, error) {
	t.Helper()
	_, defs, err := ParsePackage(planSrc, libs, nil, map[string]string{}, map[string]string{}, testStdSig)
	return defs, err
}

func TestPackage_InlineDefUsedInPlan(t *testing.T) {
	plan, defs, err := ParsePackage(
		"def my-x(msg: str) { apply { shell { echo \"$msg\" } return ok.done } }\non web { my-x(\"hi\") }",
		nil, nil, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs["my-x"].Name != "my-x" {
		t.Fatalf("user def not collected: %+v", defs)
	}
	step := plan[0].Steps[0]
	if step.Instruction != "my-x" || step.Args["msg"] != "hi" {
		t.Fatalf("call not resolved against the user def: %+v", step)
	}
}

func TestPackage_DefInSiblingFile(t *testing.T) {
	// A def written in one file is usable from the plan file — no import.
	libs := map[string]string{"lib.shellf": `def helper(path: str) { apply { shell { touch "$path" } return ok.done } }`}
	plan, defs, err := ParsePackage(`on web { helper("/tmp/x") }`, libs, nil, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs["helper"].Name != "helper" {
		t.Fatalf("sibling def not collected: %+v", defs)
	}
	if plan[0].Steps[0].Args["path"] != "/tmp/x" {
		t.Fatalf("sibling def call not resolved: %+v", plan[0].Steps[0])
	}
}

func TestPackage_DuplicateDef(t *testing.T) {
	_, err := pkg(t, "def a(x: str) { apply { shell { echo hi } return ok.done } }\ndef a(y: str) { apply { shell { echo ho } return ok.done } }\non web { }", nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate def") {
		t.Fatalf("duplicate def must error: %v", err)
	}
}

// Shadowing is now expressed by placement: a def in a `dir/` sub-package resolves as
// `dir.ensure`, which is a stdlib name (ADR-0033 §4). The rule itself is unchanged
// (ADR-0014 §5) — only how the qualified name is written, since a dot may no longer
// appear in a def name.
func TestPackage_ShadowStdlibNeedsOverride(t *testing.T) {
	sub := func(src string) map[string]string { return map[string]string{"dir/ensure.shellf": src} }

	// A plain def whose qualified name hits the stdlib is an error…
	_, _, err := ParsePackage(`on web { }`, sub(`def ensure(path: str) { apply { shell { mkdir "$path" } return ok.done } }`),
		nil, map[string]string{}, map[string]string{}, testStdSig)
	if err == nil || !strings.Contains(err.Error(), "shadows a stdlib def") {
		t.Fatalf("shadowing must error without override: %v", err)
	}
	// …and the message names the qualified form, which is what the caller would type.
	if !strings.Contains(err.Error(), "dir.ensure") {
		t.Fatalf("error must name the qualified def, got: %v", err)
	}

	// …but `override def` is allowed and wins.
	_, defs, err := ParsePackage(`on web { }`, sub(`override def ensure(path: str) { apply { shell { mkdir "$path" } return ok.done } }`),
		nil, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatalf("override should be allowed: %v", err)
	}
	if len(defs) != 1 || !defs["dir.ensure"].Override {
		t.Fatalf("override def not recorded under its qualified name: %+v", defs)
	}
}

// ADR-0033 §1: the dot separates package from action on a call; it is never part of
// what an author writes after `def`. The package comes from the directory.
func TestPackage_DotInDefNameRejected(t *testing.T) {
	_, err := pkg(t, `def dir.ensure(path: str) { apply { shell { mkdir "$path" } return ok.done } }`, nil)
	if err == nil {
		t.Fatal("a dot in a def name must be rejected (ADR-0033)")
	}
}

// A sub-package qualifies its defs; a plain (non-stdlib) name is reachable as
// `<dir>.<def>` and not under its bare name.
func TestPackage_SubPackageQualifies(t *testing.T) {
	libs := map[string]string{"tools/helper.shellf": `def helper(path: str) { apply { shell { touch "$path" } return ok.done } }`}
	_, defs, err := ParsePackage(`on web { tools.helper("/tmp/x") }`, libs, nil, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["tools.helper"]; !ok {
		t.Fatalf("sub-package def must register qualified: %+v", defs)
	}
	if _, bare := defs["helper"]; bare {
		t.Fatalf("sub-package def must NOT register bare: %+v", defs)
	}
}

func TestPackage_OverrideNothing(t *testing.T) {
	_, err := pkg(t, `override def brand-new(x: str) { apply { shell { echo hi } return ok.done } }`, nil)
	if err == nil || !strings.Contains(err.Error(), "overrides nothing") {
		t.Fatalf("override of an unknown name must error: %v", err)
	}
}

func TestPackage_SiblingCannotTargetHosts(t *testing.T) {
	libs := map[string]string{"web.shellf": `on web { }`}
	_, err := pkg(t, `on app { }`, libs)
	if err == nil || !strings.Contains(err.Error(), "may only contain defs") {
		t.Fatalf("an `on` block in a sibling must error: %v", err)
	}
}

func TestImport_QualifiedCallResolves(t *testing.T) {
	imports := map[string][]string{
		"web": {`def deploy(port: str) { apply { shell { echo "$port" } return ok.done } }`},
	}
	plan, defs, err := ParsePackage(
		"import web \"../shared\"\non target { web.deploy(\"8080\") }",
		nil, imports, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	step := plan[0].Steps[0]
	if step.Instruction != "web.deploy" || step.Args["port"] != "8080" {
		t.Fatalf("qualified call not resolved: %+v", step)
	}
	if _, ok := defs["web.deploy"]; !ok {
		t.Fatalf("imported def not registered under its qualified name: %v", defs)
	}
}

func TestImport_Errors(t *testing.T) {
	simple := map[string][]string{"web": {`def deploy(port: str) { apply { shell { echo hi } return ok.done } }`}}
	cases := []struct {
		name, plan, want string
		imports          map[string][]string
	}{
		{"duplicate-alias", "import web \"../a\"\nimport web \"../b\"\non t { }", "duplicate import alias", map[string][]string{"web": {`def d() { apply { shell { echo hi } return ok.done } }`}}},
		{"import-after-on", "on t { }\nimport web \"../shared\"", "imports must come before", simple},
		{"package-not-loaded", "import ghost \"../none\"\non t { }", "not loaded", nil},
		{"imported-has-on", "import web \"../shared\"\non t { }", "may only contain defs", map[string][]string{"web": {`on x { }`}}},
		{"imported-imports", "import web \"../shared\"\non t { }", "may not itself import", map[string][]string{"web": {`import y "../z"`}}},
		{"collides-with-stdlib", "import apt \"../x\"\non t { }", "collides with a stdlib", map[string][]string{"apt": {`def install(pkg: str) { apply { shell { echo hi } return ok.done } }`}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ParsePackage(c.plan, nil, c.imports, map[string]string{}, map[string]string{}, testStdSig)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error %q, got %v", c.want, err)
			}
		})
	}
}

func TestOptionalArgs(t *testing.T) {
	def := `def f(a: str, b: str = "x") { apply { shell { echo hi } return ok.done } }`
	// Omitting the defaulted `b` is allowed; only `a` lands in Args.
	plan, _, err := ParsePackage(def+"\non t { f(\"y\") }", nil, nil, map[string]string{}, map[string]string{}, testStdSig)
	if err != nil {
		t.Fatal(err)
	}
	step := plan[0].Steps[0]
	if step.Args["a"] != "y" || len(step.Args) != 1 {
		t.Fatalf("optional arg not omitted: %+v", step.Args)
	}
	// Too few (a is required) and too many both error.
	if _, _, err := ParsePackage(def+"\non t { f() }", nil, nil, map[string]string{}, map[string]string{}, testStdSig); err == nil {
		t.Fatal("omitting a required arg must error")
	}
	if _, _, err := ParsePackage(def+"\non t { f(\"y\", \"z\", \"w\") }", nil, nil, map[string]string{}, map[string]string{}, testStdSig); err == nil {
		t.Fatal("too many args must error")
	}
}

func TestScanImports(t *testing.T) {
	imps, err := ScanImports("import web \"../shared\"\nimport db \"../data\"\non t { }")
	if err != nil {
		t.Fatal(err)
	}
	if len(imps) != 2 || imps[0].Alias != "web" || imps[0].Path != "../shared" || imps[1].Alias != "db" {
		t.Fatalf("scanned imports: %+v", imps)
	}
}

func TestPackage_ForwardRefInPlanFile(t *testing.T) {
	// Documented v1 constraint: within the plan file a def must precede its use
	// (sibling files are order-free). A forward reference is an unknown call.
	_, err := pkg(t, "on web { later() }\ndef later() { apply { shell { echo hi } return ok.done } }", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown instruction") {
		t.Fatalf("a forward-referenced def should be unknown: %v", err)
	}
}

// ADR-0032 §4: the old names are not accepted, but a plan using one must say what to
// write instead — sixty failures each naming its replacement is a mechanical edit,
// sixty "unknown instruction" is a treasure hunt. Driven off the rename table itself,
// so a future rename cannot forget an entry.
func TestRenamedInstructions_ErrorNamesReplacement(t *testing.T) {
	if len(Renamed) == 0 {
		t.Fatal("the rename table is empty")
	}
	for old, want := range Renamed {
		t.Run(old, func(t *testing.T) {
			_, err := ParsePlanWithVars("on web { "+old+"(\"a\", \"b\") }", nil, nil, testStdSig)
			if err == nil {
				t.Fatalf("%q must not resolve any more", old)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error must name the replacement %q, got: %v", want, err)
			}
		})
	}
	// The replacements themselves must not sit in the table (a rename to itself would
	// make the message advise what already failed).
	for old, want := range Renamed {
		if old == want {
			t.Fatalf("%q maps to itself", old)
		}
	}
}
