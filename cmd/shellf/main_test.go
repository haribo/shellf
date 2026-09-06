package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/lang"
	"shellf/internal/orchestrator"
)

// The end-to-end wiring of `runCmd`/`cleanCmd` (flag parsing → transport → exit
// code) is covered by the real-SSH harness in test/e2e/. These unit tests lock
// the risk-bearing pure logic: variable precedence and signature resolution.

func TestLoadGlobals_SetOnly(t *testing.T) {
	base, set, err := loadGlobals("", kvFlags{"pkg=nginx", "env=prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 0 {
		t.Fatalf("no --vars file → empty base, got %v", base)
	}
	if set["pkg"] != "nginx" || set["env"] != "prod" {
		t.Fatalf("--set not collected: %v", set)
	}
}

func TestLoadGlobals_VarsFileThenSet(t *testing.T) {
	dir := t.TempDir()
	vf := filepath.Join(dir, "vars.shellf")
	if err := os.WriteFile(vf, []byte("pkg = \"apache\"\nregion = \"eu\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, set, err := loadGlobals(vf, kvFlags{"pkg=nginx"})
	if err != nil {
		t.Fatal(err)
	}
	// The file feeds baseVars; --set stays separate (higher precedence, layered
	// later by the orchestrator). loadGlobals must not merge them.
	if base["pkg"] != "apache" || base["region"] != "eu" {
		t.Fatalf("vars file not parsed into base: %v", base)
	}
	if set["pkg"] != "nginx" {
		t.Fatalf("--set override not kept distinct: %v", set)
	}
}

func TestLoadGlobals_MalformedSet(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue"} {
		if _, _, err := loadGlobals("", kvFlags{bad}); err == nil {
			t.Fatalf("--set %q must error", bad)
		}
	}
}

func TestLoadGlobals_MissingVarsFile(t *testing.T) {
	if _, _, err := loadGlobals("/does/not/exist.shellf", nil); err == nil {
		t.Fatal("a missing --vars file must error")
	}
}

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
	dir := project(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { m.mark("/x", "hi") }`)
	writeDef(t, dir, "m", "mark.shellf", `def mark(path: str, content: str) { apply { shell { echo hi } return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	writeFile(t, filepath.Join(dir, "defs"), "notes.txt", "not a shellf file")

	plan, defsSrc, _, err := loadPlanPackage(
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
	dir := project(t, t.TempDir())
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
	dir := project(t, t.TempDir())
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
	_, defs, _, err := loadPlanPackage(filepath.Join(plans, "plan.shellf"),
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
	dir := project(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "assets"), "svc.tmpl", "service=@{svc}\n")
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on t {
		for svc in ["alpha", "beta"] {
			file.template(%"svc.tmpl", "/opt/${svc}/x") with { svc = "${svc}" }
		}
	}`)
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host t = { address: "x", user: "u" }`)
	plan, _, _, err := loadPlanPackage(
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
	planDir := project(t, t.TempDir())
	writeFile(t, filepath.Join(planDir, "plans"), "plan.shellf",
		"import r \"file://"+repo+"@v1.0.0\"\non web { r.deploy(\"9090\") }")
	writeFile(t, filepath.Join(planDir, "inventories"), "inv.shellf", `host web = { address: "x", user: "u" }`)

	plan, defs, _, err := loadPlanPackage(
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

func TestLoadInventory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "inv.shellf", `host web = { address: "10.0.0.1", user: "deploy" }`)
	inv, err := loadInventory(filepath.Join(dir, "inv.shellf"))
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := inv.Resolve("web"); !ok || h.Address != "10.0.0.1" {
		t.Fatalf("inventory not parsed: %+v ok=%v", h, ok)
	}
	if _, err := loadInventory(filepath.Join(dir, "missing.shellf")); err == nil {
		t.Fatal("a missing inventory must error")
	}
}

// project lays out an empty shellf project (ADR-0038) under root and returns it, so a
// test states the layout once instead of repeating four MkdirAll calls.
func project(t *testing.T, root string) string {
	t.Helper()
	for _, d := range []string{"plans", "defs", "assets", "inventories"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// writeDef writes a def into its package directory, creating it: defs/<pkg>/<name>.
func writeDef(t *testing.T, root, pkg, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "defs", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, name, content)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestLoadSecrets(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "s.txt", "S3cr3t!\n") // trailing newline is trimmed
	t.Setenv("MY_SECRET", "envval")

	secrets, values, err := loadSecrets(
		kvFlags{"pass=" + filepath.Join(dir, "s.txt")},
		kvFlags{"tok=MY_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if secrets["pass"] != "S3cr3t!" || secrets["tok"] != "envval" {
		t.Fatalf("secrets: %v", secrets)
	}
	if len(values) != 2 {
		t.Fatalf("values: %v", values)
	}
	// Malformed and missing-file both error.
	if _, _, err := loadSecrets(kvFlags{"noequals"}, nil); err == nil {
		t.Fatal("malformed --secret-file must error")
	}
	if _, _, err := loadSecrets(kvFlags{"x=/does/not/exist"}, nil); err == nil {
		t.Fatal("a missing secret file must error")
	}
}

func TestKVFlags(t *testing.T) {
	var k kvFlags
	if err := k.Set("a=1"); err != nil {
		t.Fatal(err)
	}
	_ = k.Set("b=2")
	if k.String() != "a=1,b=2" {
		t.Fatalf("String(): %q", k.String())
	}
}

func TestVersionLine(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "v9.9.9"
	if got := versionLine(); got != "shellf v9.9.9" {
		t.Fatalf("versionLine: %q", got)
	}
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

// TestParseDefsFor stood here: the CLI re-parsed the shipped def sources to extract
// their `%` occurrences for the allow-list. A def may no longer name a control-host file
// (#403, ADR-0043), so the plan's steps are the only source and the helper went with the
// question — see TestControl_DefMayNotDeclareAPath in internal/lang.

// ADR-0035 renames the flag. The old spelling must fail naming the new one rather than
// be accepted, or two names live forever — the same rule as the renamed instructions.
func TestDryRunFlag_OldNameIsRefused(t *testing.T) {
	bin := buildShellf(t)
	out, err := exec.Command(bin, "run", "--inventory", "x", "--check", "p.shellf").CombinedOutput()
	if err == nil {
		t.Fatal("--check must fail")
	}
	if !strings.Contains(string(out), "--dry-run") {
		t.Fatalf("the error must name the replacement: %s", out)
	}
}

// buildShellf compiles the CLI once for flag-level tests.
func buildShellf(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "shellf")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Skipf("cannot build: %v: %s", err, out)
	}
	return bin
}

func TestRemovedFlag(t *testing.T) {
	if got := removedFlag(false); got != "" {
		t.Fatalf("no removed flag passed: %q", got)
	}
	got := removedFlag(true)
	if !strings.Contains(got, "--dry-run") || !strings.Contains(got, "--check") {
		t.Fatalf("must name both the old and the new spelling: %q", got)
	}
}

// TestUsesRender stood here, guarding the case where a plan rendered without declaring a
// single `%"…"` path: the content came from the target, so nothing else opened the
// channel. A render now names a declared template (#392, ADR-0042), so a non-empty
// allow-list is the only condition left and `usesRender` went with the case.

// TestMergeVars stood here. It asserted "the host wins over globals" against a mergeVars
// helper that no longer exists: #540 took the inventory out of a bare reference's sources
// (ADR-0053), and the control-host render scope now uses orchestrator.HostEnv like every
// other call path. Its comment cited ADR-0022 for the precedence chain, which was wrong —
// that chain is ADR-0003 §3; ADR-0022 is the `with { }` per-call override.

// #311: a call cycle is refused when the defs are loaded, not when they run (ADR-0030
// §6). The distinction is the whole issue: the evaluator's guard fires on the target,
// after earlier steps of the plan have already acted, leaving a partially applied host.
//
// loadPlanPackage runs before anything is dialled, so a failure here *is* the "no host
// was contacted" assertion — there is no transport in this call path to stub out.
func TestLoadPlanPackage_RefusesACycleBeforeAnyTransport(t *testing.T) {
	dir := project(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { c.a("/x") }`)
	writeDef(t, dir, "c", "a.shellf", `def a(p: str) { apply { c.b(p) return ok.done } }`)
	writeDef(t, dir, "c", "b.shellf", `def b(p: str) { apply { c.a(p) return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)

	_, _, _, err := loadPlanPackage(
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
	dir := project(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { d.deliver("/x", "hi") }`)
	writeDef(t, dir, "d", "deliver.shellf",
		`def deliver(path: str, content: str) { apply { file.write(path, content) return ok.done } }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	// `defs/file/` is the package `file`, so this declares `file.write` and overrides the
	// stdlib def of that name (ADR-0038 §2 over ADR-0033's rule).
	writeDef(t, dir, "file", "write.shellf",
		`override def write(path: str, content: str) { apply { d.deliver(path, content) return ok.done } }`)

	_, _, _, err := loadPlanPackage(
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
	dir := project(t, t.TempDir())
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

// #345: a plan that asks the control host for nothing opens no bridge. The channel
// factory returns nil for every host, which is what keeps a detached job detached
// (ADR-0031 §2) — a bridge opened for nothing would make every plan depend on the
// control host staying reachable.
func TestControlChannel_NilWhenThePlanAsksNothing(t *testing.T) {
	dir := project(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf", `on web { dir.ensure("/x") }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	planPath := filepath.Join(dir, "plans", "plan.shellf")
	invPath := filepath.Join(dir, "inventories", "inv.shellf")

	plan, _, _, err := loadPlanPackage(planPath, invPath, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := loadInventory(invPath)
	if err != nil {
		t.Fatal(err)
	}
	channelFor := controlChannel(planPath, plan, inv, map[string]string{}, map[string]string{})
	if channelFor("web") != nil {
		t.Fatal("a plan that asks for nothing must open no bridge")
	}
}

// And the other half: a plan that marks a control-host path gets a channel.
func TestControlChannel_ServesADeclaredPath(t *testing.T) {
	dir := project(t, t.TempDir())
	writeFile(t, filepath.Join(dir, "assets"), "motd.tmpl", "hello @{who}\n")
	writeFile(t, filepath.Join(dir, "plans"), "plan.shellf",
		`on web { file.template(%"motd.tmpl", "/etc/motd") }`)
	writeFile(t, filepath.Join(dir, "inventories"), "inv.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	planPath := filepath.Join(dir, "plans", "plan.shellf")
	invPath := filepath.Join(dir, "inventories", "inv.shellf")

	plan, _, _, err := loadPlanPackage(planPath, invPath, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := loadInventory(invPath)
	if err != nil {
		t.Fatal(err)
	}
	channelFor := controlChannel(planPath, plan, inv, map[string]string{}, map[string]string{})
	if channelFor("web") == nil {
		t.Fatal("a plan declaring a control-host path must get a bridge")
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

// `--parallel 0` is the operator typing something that cannot mean anything — it is
// refused, never read as "unlimited" (#462). An *absent* flag is a different thing and
// takes the default, which is why the check asks the flag set what was provided rather
// than comparing to zero.
func TestCheckParallel(t *testing.T) {
	parsed := func(args ...string) (*flag.FlagSet, int) {
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		n := fs.Int("parallel", 0, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return fs, *n
	}
	// Absent: takes the default, so nothing is refused here.
	fs, n := parsed()
	checkParallel(fs, n) // must not exit
	fs, n = parsed("--parallel", "4")
	checkParallel(fs, n) // must not exit

	// Provided and impossible: the process must exit 2. Asserted in a subprocess, since
	// checkParallel exits rather than returning an error.
	if os.Getenv("SHELLF_TEST_BAD_PARALLEL") != "" {
		fs, n := parsed("--parallel", os.Getenv("SHELLF_TEST_BAD_PARALLEL"))
		checkParallel(fs, n)
		return
	}
	for _, bad := range []string{"0", "-1"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestCheckParallel")
		cmd.Env = append(os.Environ(), "SHELLF_TEST_BAD_PARALLEL="+bad)
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Fatalf("--parallel %s must exit 2, got %v", bad, err)
		}
	}
}

// anyBlockError is what makes `status` exit non-zero on an unknown target (#451). It had
// no test of its own — the behaviour was only covered end to end, where a change to it
// would surface as a puzzling exit code rather than a failing assertion.
// errFake is a minimal error for table cases. internal/report has its own since #491:
// a test helper does not cross a package boundary.
type errFake string

func (e errFake) Error() string { return string(e) }

func TestAnyBlockError(t *testing.T) {
	none := []orchestrator.BlockReport{
		{Target: "web", Hosts: []orchestrator.HostOutcome{{Host: "h1"}}},
		{Target: "db"},
	}
	if anyBlockError(none) {
		t.Fatal("no block failed as a whole")
	}
	// A per-host failure is not a block failure: the block ran, the host did not.
	perHost := []orchestrator.BlockReport{{
		Target: "web",
		Hosts:  []orchestrator.HostOutcome{{Host: "h1", Err: errFake("unreachable")}},
	}}
	if anyBlockError(perHost) {
		t.Fatal("a host error is not a block error")
	}
	blocked := []orchestrator.BlockReport{
		{Target: "web", Hosts: []orchestrator.HostOutcome{{Host: "h1"}}},
		{Target: "wbe", Err: &orchestrator.UnknownTargetError{Target: "wbe"}},
	}
	if !anyBlockError(blocked) {
		t.Fatal("a block that could not run must be reported")
	}
}

// `-v` must not undo what the report masks: the tracer is where redaction happens,
// because the CLI is what knows the run's secrets (#461).
func TestTracer_RedactsAndStaysOffStdout(t *testing.T) {
	if tracer(false, nil) != nil {
		t.Fatal("without -v there must be no tracer at all")
	}
	tr := tracer(true, []string{"sup3rs3cret"})
	if tr == nil {
		t.Fatal("with -v there must be one")
	}

	// Captured from stderr, which is also the assertion that it does not use stdout.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, stdout := os.Stderr, os.Stdout
	outR, outW, _ := os.Pipe()
	os.Stderr, os.Stdout = w, outW
	tr("pushing %s to %s", "sup3rs3cret", "/tmp/agent")
	os.Stderr, os.Stdout = stderr, stdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}

	var buf, outBuf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&outBuf, outR); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(buf.String(), "sup3rs3cret") {
		t.Fatalf("the secret leaked into the trace: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "***") {
		t.Fatalf("it must be masked, not dropped: %q", buf.String())
	}
	if outBuf.String() != "" {
		t.Fatalf("nothing may reach stdout: %q", outBuf.String())
	}
}
