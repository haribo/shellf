package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/lang"
)

// writeFile and projectDir are this package's copies of the two helpers cmd/shellf also
// has: a test helper does not cross a package boundary.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDef(t *testing.T, root, pkg, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "defs", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, name, content)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// Moved here with the code they exercise (#491/#587). They used to reach the loader
// through `cmd/shellf`, which also parsed flags and exited the process.

func TestStdSignatures(t *testing.T) {
	sig := stdSignatures()

	if params, req, ok := sig("file.copy"); !ok || len(params) != 2 || req != 2 || params[0].Name != "src" || params[1].Name != "dst" {
		t.Fatalf("file-copy signature: %v req=%d ok=%v", params, req, ok)
	}
	// A stdlib def resolves its params from the embedded source (self-hosting).
	if params, req, ok := sig("dir.ensure"); !ok || len(params) != 1 || req != 1 || params[0].Name != "path" {
		t.Fatalf("stdlib dir-ensure signature: %v req=%d ok=%v", params, req, ok)
	}
	// compose-up gained an optional `build` param → 2 params, 1 required.
	if params, req, ok := sig("docker.compose-up"); !ok || len(params) != 2 || req != 1 {
		t.Fatalf("compose-up optional-param signature: %v req=%d ok=%v", params, req, ok)
	}
	if _, _, ok := sig("no-such-instruction"); ok {
		t.Fatal("an unknown instruction must not resolve")
	}
}

func TestLoadPlanPackage(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { m.mark("/x", "hi") }`)
	writeDef(t, dir, "m", "mark.shellf", `def mark(path: str, content: str) { apply { shell { echo hi } return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	writeFile(t, filepath.Join(dir, "defs"), "notes.txt", "not a shellf file")

	plan, defsSrc, _, err := Load(
		filepath.Join(dir, "plans", "plan.shellf"), filepath.Join(dir, "inventories", "inventory.shellf"),
		map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	// The plan resolves `mark` against the sibling def.
	if plan[0].Steps[0].Instruction != "m.mark" || plan[0].Steps[0].Args["path"] != "/x" {
		t.Fatalf("sibling def not resolved: %+v", plan[0].Steps[0])
	}
	// The def ships keyed by name; the inventory does not leak into it.
	if !strings.Contains(defsSrc["m.mark"], "def mark") {
		t.Fatalf("def source not collected: %v", defsSrc)
	}
	for _, src := range defsSrc {
		if strings.Contains(src, "host web") {
			t.Fatal("the inventory must not be loaded as a package def")
		}
	}
}

func TestPackageLibs_ExcludesPlanAndInventory(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { }`)
	writeDef(t, dir, "p", "lib.shellf", `def a() { apply { shell { echo hi } return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "x", user: "u" }`)

	libs, err := packageLibs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := libs["p/lib.shellf"]; !ok || len(libs) != 1 {
		t.Fatalf("expected only lib.shellf, got %v", keys(libs))
	}
}

func TestReadImports(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	plans := filepath.Join(dir, "plans")
	writeFile(t, plans, "plan.shellf", "import lib \"sub\"\non web { lib.helper() }")
	sub := filepath.Join(plans, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "h.shellf"), []byte(`def helper() { apply { shell { echo hi } return ok.done } }`), 0o600); err != nil {
		t.Fatal(err)
	}
	planSrc, _ := os.ReadFile(filepath.Join(plans, "plan.shellf"))
	imports, err := readImports(filepath.Join(plans, "plan.shellf"), string(planSrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(imports["lib"]) != 1 || !strings.Contains(imports["lib"][0], "def helper") {
		t.Fatalf("import not resolved to its package sources: %v", imports)
	}
	// An import of a missing directory errors.
	bad := "import ghost \"nope\"\non web { }"
	if _, err := readImports(filepath.Join(plans, "plan.shellf"), bad); err == nil {
		t.Fatal("importing a missing directory must error")
	}
	// The full path resolves through loadPlanPackage too.
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host web = { address: "x", user: "u" }`)
	_, defs, _, err := Load(filepath.Join(plans, "plan.shellf"),
		filepath.Join(dir, "inventories", "inv.shellf"), map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(defs["lib.helper"], "def helper") {
		t.Fatalf("imported def not shipped under its qualified name: %v", defs)
	}
}

func TestLoadPlanPackage_KeepsTemplateStepsForPerHostRender(t *testing.T) {
	// Templates are NOT resolved at load time anymore — they render per host in
	// the orchestrator (ADR-0024). loadPlanPackage keeps the `file.template` step, with
	// its parse-time `dst` interpolation and `with { }` intact. Here a `for` loop
	// var is captured into `with` for the render (ADR-0023 composition).
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "assets"), "svc.tmpl", "service=@{svc}\n")
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on t {
		for svc in ["alpha", "beta"] {
			file.template(%"svc.tmpl", "/opt/${svc}/x") with { svc = "${svc}" }
		}
	}`)
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host t = { address: "x", user: "u" }`)
	plan, _, _, err := Load(
		filepath.Join(dir, "plans", "plan.shellf"), filepath.Join(dir, "inventories", "inv.shellf"),
		map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range []string{"alpha", "beta"} {
		s := plan[0].Steps[i]
		if s.Instruction != "file.template" { // still a template, not yet a file-write
			t.Fatalf("iteration %d: template should survive load, got %q", i, s.Instruction)
		}
		if s.Args["dst"] != "/opt/"+item+"/x" { // dst `${svc}` interpolated at parse
			t.Fatalf("iteration %d: dst=%q", i, s.Args["dst"])
		}
		if s.With["svc"] != item { // loop var captured into `with` for the render
			t.Fatalf("iteration %d: with[svc]=%q, want %q", i, s.With["svc"], item)
		}
	}
}

func TestReadImports_RemoteModule(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // isolate the module cache

	// A git repo module of one def, tagged v1.0.0.
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "web.shellf"),
		[]byte(`def deploy(port: str) { apply { shell { echo "$port" } return ok.done } }`), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	gitRun(t, repo, "tag", "v1.0.0")

	// A plan that imports it remotely.
	planDir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(planDir, "plans"), "plan.shellf",
		"import r \"file://"+repo+"@v1.0.0\"\non web { r.deploy(\"9090\") }")
	writeFile(t, filepath.Join(planDir, "inventories"), "inv.shellf", `host web = { address: "x", user: "u" }`)

	plan, defs, _, err := Load(
		filepath.Join(planDir, "plans", "plan.shellf"),
		filepath.Join(planDir, "inventories", "inv.shellf"),
		map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Steps[0].Instruction != "r.deploy" || plan[0].Steps[0].Args["port"] != "9090" {
		t.Fatalf("remote import not resolved: %+v", plan[0].Steps[0])
	}
	if !strings.Contains(defs["r.deploy"], "def deploy") {
		t.Fatalf("imported def not shipped qualified: %v", defs)
	}
	// The lockfile was written next to the plan.
	if _, err := os.Stat(filepath.Join(planDir, "shellf.lock")); err != nil {
		t.Fatalf("shellf.lock not written: %v", err)
	}
}

func TestDefSource(t *testing.T) {
	// Each def maps to its own source, keyed by resolved name.
	got := defSource(map[string]lang.Def{
		"a":          {Source: "def a() {}"},
		"web.deploy": {Source: "def deploy() {}"},
	})
	if got["a"] != "def a() {}" || got["web.deploy"] != "def deploy() {}" {
		t.Fatalf("defSource: %v", got)
	}
}

// project lays out an empty shellf project (ADR-0038) under root and returns it, so a
// test states the layout once instead of repeating four MkdirAll calls.
func projectDir(t *testing.T, root string) string {
	t.Helper()
	for _, d := range []string{"plans", "defs", "assets", "inventories"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// #311: a call cycle is refused when the defs are loaded, not when they run (ADR-0030
// §6). The distinction is the whole issue: the evaluator's guard fires on the target,
// after earlier steps of the plan have already acted, leaving a partially applied host.
//
// loadPlanPackage runs before anything is dialled, so a failure here *is* the "no host
// was contacted" assertion — there is no transport in this call path to stub out.
func TestLoadPlanPackage_RefusesACycleBeforeAnyTransport(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { c.a("/x") }`)
	writeDef(t, dir, "c", "a.shellf", `def a(p: str) { apply { c.b(p) return ok.done } }`)
	writeDef(t, dir, "c", "b.shellf", `def b(p: str) { apply { c.a(p) return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)

	_, _, _, err := Load(
		filepath.Join(dir, "plans", "plan.shellf"), filepath.Join(dir, "inventories", "inventory.shellf"),
		map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("a cyclic package must not load")
	}
	if !strings.Contains(err.Error(), "call cycle: c.a -> c.b -> c.a") {
		t.Fatalf("the error must name the chain, got %v", err)
	}
}

// A cycle that only exists because a user def overrides a stdlib one and calls back into
// the caller. This is why the check takes the run's own resolver rather than the package
// map: seen from the user package alone, `file.write` is just a name that is not there.
func TestLoadPlanPackage_RefusesACycleThroughAnOverride(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { d.deliver("/x", "hi") }`)
	writeDef(t, dir, "d", "deliver.shellf",
		`def deliver(path: str, content: str) { apply { file.write(path, content) return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	// `defs/file/` is the package `file`, so this declares `file.write` and overrides the
	// stdlib def of that name (ADR-0038 §2 over ADR-0033's rule).
	writeDef(t, dir, "file", "write.shellf",
		`override def write(path: str, content: str) { apply { d.deliver(path, content) return ok.done } }`)

	_, _, _, err := Load(
		filepath.Join(dir, "plans", "plan.shellf"), filepath.Join(dir, "inventories", "inventory.shellf"),
		map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("a cycle through an override must not load")
	}
	if !strings.Contains(err.Error(), "call cycle") {
		t.Fatalf("got %v", err)
	}
}

// #355: a def must live in a package directory. The message says where to move it —
// written when `defs/` replaced the plan's siblings, and never exercised until now.
func TestPackageLibs_RefusesALooseDef(t *testing.T) {
	dir := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "defs"), "stray.shellf",
		`def a() { apply { return ok.done } }`)

	_, err := packageLibs(dir)
	if err == nil {
		t.Fatal("a def outside a package directory must be refused")
	}
	if !strings.Contains(err.Error(), "defs/<package>/stray.shellf") {
		t.Fatalf("the error must name where to move it, got %v", err)
	}
}

// A project with no `defs/` at all is legitimate — a plan may call only stdlib
// instructions — and must load rather than fail on a missing directory.
func TestPackageLibs_NoDefsDirectoryIsFine(t *testing.T) {
	dir := t.TempDir() // deliberately not laid out: only plans/ is required
	libs, err := packageLibs(dir)
	if err != nil {
		t.Fatalf("a project without defs/ must load: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("expected no libs, got %v", keys(libs))
	}
}

// #414: `dir.copy`'s third argument is documented (`docs/language.md`, README) and was
// refused — `dir.copy expects 2 argument(s), got 3`. A stale Go-builtin entry in
// stdSignatures shadowed the def, which grew `compare` when `dir.copy` became a def over
// `~dir.sync` (#335, ADR-0039 §6). `dir.sync` was never in that table, which is why the
// same call works there.
//
// The rule the table's own comment states: "adding a def needs no parser-side edit".
func TestStdSignatures_ComeFromTheDefs(t *testing.T) {
	sig := stdSignatures()

	params, required, ok := sig("dir.copy")
	if !ok {
		t.Fatal("dir.copy must resolve")
	}
	if len(params) != 3 || params[2].Name != "compare" {
		t.Fatalf("dir.copy's signature must come from its def: %v", params)
	}
	if required != 2 {
		t.Fatalf("compare has a default, so two arguments are required, got %d", required)
	}

	// The neighbour that was in the same table, for the same stale reason.
	if params, required, ok := sig("file.copy"); !ok || len(params) != 2 || required != 2 {
		t.Fatalf("file.copy: %v %d %v", params, required, ok)
	}
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// The rename table must not advise a name that does not exist: a message pointing at
// `file.write` is only useful if `file.write` actually resolves. Checked here because
// internal/lang cannot import internal/std (std imports lang).
func TestRenameTable_TargetsResolve(t *testing.T) {
	sig := stdSignatures()
	if len(lang.Renamed) < 25 {
		t.Fatalf("rename table looks incomplete: %d entries", len(lang.Renamed))
	}
	for old, want := range lang.Renamed {
		if _, _, ok := sig(want); !ok {
			t.Errorf("%q is advised as the replacement for %q but does not resolve", want, old)
		}
		if _, _, ok := sig(old); ok {
			t.Errorf("%q must no longer resolve (ADR-0032 §4: no aliases)", old)
		}
	}
}

// Root's error is most of its value: it is the first thing anyone running shellf outside a
// project sees, so it has to name the layout rather than report a file not found from
// somewhere inside the loader (ADR-0038).
func TestRoot_NamesTheLayoutWhenThePlanIsNotInOne(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.shellf", `on web { dir.ensure("/x") }`)

	_, err := Root(filepath.Join(dir, "plan.shellf"))
	if err == nil {
		t.Fatal("a plan outside plans/ is not in a project")
	}
	for _, want := range []string{"plans/", "defs/", "assets/", "inventories/", "ADR-0038"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name the layout, missing %q: %v", want, err)
		}
	}
}

func TestRoot_ReturnsTheDirectoryHoldingTheLayout(t *testing.T) {
	root := projectDir(t, t.TempDir())
	got, err := Root(filepath.Join(root, "plans", "p.shellf"))
	if err != nil {
		t.Fatal(err)
	}
	// Resolved through symlinks on both sides: macOS hands out /var → /private/var
	// temp dirs, and comparing one form against the other fails for no reason.
	want, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Fatalf("root = %q, want %q", gotResolved, want)
	}
}

// A missing plan is reported as such, not as a project-layout error: the two send the
// reader to different places.
func TestLoad_MissingPlanFile(t *testing.T) {
	root := projectDir(t, t.TempDir())
	_, _, _, err := Load(filepath.Join(root, "plans", "absent.shellf"),
		filepath.Join(root, "inventories", "inv.shellf"), map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("a plan that does not exist must error")
	}
	if strings.Contains(err.Error(), "ADR-0038") {
		t.Fatalf("a missing file is not a layout problem: %v", err)
	}
}

// A def that does not parse stops the load, naming the file — the alternative is a plan
// that runs with a def silently missing from its table.
func TestLoad_RefusesADefThatDoesNotParse(t *testing.T) {
	root := projectDir(t, t.TempDir())
	writeFile(t, filepath.Join(root, "plans"), "p.shellf", `on web { dir.ensure("/x") }`)
	writeFile(t, filepath.Join(root, "inventories"), "inv.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	writeDef(t, root, "broken", "b.shellf", `def oops(p: str) { apply { `)

	_, _, _, err := Load(filepath.Join(root, "plans", "p.shellf"),
		filepath.Join(root, "inventories", "inv.shellf"), map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("a def that does not parse must stop the load")
	}
}
