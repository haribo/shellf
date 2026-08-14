package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/lang"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
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

	if params, req, ok := sig("file.copy"); !ok || len(params) != 2 || req != 2 || params[0] != "src" || params[1] != "dst" {
		t.Fatalf("builtin file-copy signature: %v req=%d ok=%v", params, req, ok)
	}
	// A stdlib def resolves its params from the embedded source (self-hosting).
	if params, req, ok := sig("dir.ensure"); !ok || len(params) != 1 || req != 1 || params[0] != "path" {
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

func TestStatusReport(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{
			{
				Host: "app1",
				Response: proto.Response{Results: []proto.StepResult{
					// a drifted value field
					{Label: "apt-install(nginx)", Category: "would", Fields: []engine.FieldDiff{
						{Name: "version", Current: "1.2.0", Desired: "1.3.0", Converged: false},
					}},
					// a converged truthy field
					{Label: "dir.ensure(/opt)", Category: "ok", Fields: []engine.FieldDiff{
						{Name: "present", Current: "true", Desired: "true", Converged: true},
					}},
					// an absent value renders as a dash
					{Label: "file.download(x)", Category: "would", Fields: []engine.FieldDiff{
						{Name: "present", Current: "", Desired: "true", Converged: false},
					}},
					// an action-shaped def
					{Label: "restart(nginx)", Category: "ok", Tag: "action"},
				}},
			},
			{Host: "app2", Err: errFake("dial")},
		},
	}}
	got := statusReport(reports)
	for _, want := range []string{
		"on web:",
		"  app1:",
		"version: 1.2.0 → 1.3.0",
		"present: true\n",
		"present: — → true",
		"restart(nginx)", "action (no observable state)",
		"  app2: unreachable (dial)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status report missing %q in:\n%s", want, got)
		}
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

func TestReportText(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{
			{
				Host: "app1",
				Response: proto.Response{
					Results: []proto.StepResult{
						{Label: "apt-install(nginx)", Category: "ok", Tag: "installed"},
						{Label: "shell(bad)", Category: "err", Tag: "runtime",
							Shell: &engine.ShellResult{Stdout: "line1\nline2"}},
					},
					Halted: true,
				},
			},
			{Host: "app2", Err: errFake("dial refused")},
		},
	}}
	text, anyErr := reportText(reports)
	if !anyErr {
		t.Fatal("a host with an err step must set anyErr")
	}
	for _, want := range []string{
		"on web:", "  app1:",
		"apt-install(nginx)", "ok.installed",
		"err.runtime", "| line1", "| line2",
		"(halted)",
		"  app2: unreachable (dial refused)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q in:\n%s", want, text)
		}
	}

	// A clean run reports no error.
	if _, anyErr := reportText([]orchestrator.BlockReport{{Target: "web", Hosts: []orchestrator.HostOutcome{
		{Host: "app1", Response: proto.Response{Results: []proto.StepResult{{Label: "x", Category: "ok"}}}},
	}}}); anyErr {
		t.Fatal("a clean run must not set anyErr")
	}
}

func TestLoadPlanPackage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.shellf", `on web { mark("/x", "hi") }`)
	writeFile(t, dir, "mark.shellf", `def mark(path: str, content: str) { apply { shell { echo hi } return ok.done } }`)
	writeFile(t, dir, "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	writeFile(t, dir, "notes.txt", "not a shellf file")

	plan, defsSrc, err := loadPlanPackage(
		filepath.Join(dir, "plan.shellf"), filepath.Join(dir, "inventory.shellf"),
		map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	// The plan resolves `mark` against the sibling def.
	if plan[0].Steps[0].Instruction != "mark" || plan[0].Steps[0].Args["path"] != "/x" {
		t.Fatalf("sibling def not resolved: %+v", plan[0].Steps[0])
	}
	// The def ships keyed by name; the inventory does not leak into it.
	if !strings.Contains(defsSrc["mark"], "def mark") {
		t.Fatalf("def source not collected: %v", defsSrc)
	}
	for _, src := range defsSrc {
		if strings.Contains(src, "host web") {
			t.Fatal("the inventory must not be loaded as a package def")
		}
	}
}

func TestPackageLibs_ExcludesPlanAndInventory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.shellf", `on web { }`)
	writeFile(t, dir, "lib.shellf", `def a() { apply { shell { echo hi } return ok.done } }`)
	writeFile(t, dir, "inventory.shellf", `host web = { address: "x", user: "u" }`)

	libs, err := packageLibs(filepath.Join(dir, "plan.shellf"), filepath.Join(dir, "inventory.shellf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := libs["lib.shellf"]; !ok || len(libs) != 1 {
		t.Fatalf("expected only lib.shellf, got %v", keys(libs))
	}
}

func TestReadImports(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.shellf", "import lib \"sub\"\non web { lib.helper() }")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "h.shellf"), []byte(`def helper() { apply { shell { echo hi } return ok.done } }`), 0o600); err != nil {
		t.Fatal(err)
	}
	planSrc, _ := os.ReadFile(filepath.Join(dir, "plan.shellf"))
	imports, err := readImports(filepath.Join(dir, "plan.shellf"), string(planSrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(imports["lib"]) != 1 || !strings.Contains(imports["lib"][0], "def helper") {
		t.Fatalf("import not resolved to its package sources: %v", imports)
	}
	// An import of a missing directory errors.
	bad := "import ghost \"nope\"\non web { }"
	if _, err := readImports(filepath.Join(dir, "plan.shellf"), bad); err == nil {
		t.Fatal("importing a missing directory must error")
	}
	// The full path resolves through loadPlanPackage too.
	writeFile(t, dir, "inv.shellf", `host web = { address: "x", user: "u" }`)
	_, defs, err := loadPlanPackage(filepath.Join(dir, "plan.shellf"), filepath.Join(dir, "inv.shellf"), map[string]string{}, map[string]string{})
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
	dir := t.TempDir()
	writeFile(t, dir, "svc.tmpl", "service=@{svc}\n")
	writeFile(t, dir, "plan.shellf", `on t {
		for svc in ["alpha", "beta"] {
			file.template(%"svc.tmpl", "/opt/${svc}/x") with { svc = "${svc}" }
		}
	}`)
	writeFile(t, dir, "inv.shellf", `host t = { address: "x", user: "u" }`)
	plan, _, err := loadPlanPackage(
		filepath.Join(dir, "plan.shellf"), filepath.Join(dir, "inv.shellf"),
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

func TestRenderTemplate(t *testing.T) {
	dir := t.TempDir()
	// @{var} is shellf's; a downstream ${SHELL} and {{ go }} pass through verbatim.
	writeFile(t, dir, "conf.tmpl", "email=@{acme}\ndomain=@{site}\nkeep=${SHELL} {{ .X }}\n")
	got, err := renderTemplate(filepath.Join(dir, "conf.tmpl"),
		map[string]string{"acme": "a@b.co", "site": "ex.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "email=a@b.co\ndomain=ex.com\nkeep=${SHELL} {{ .X }}\n" {
		t.Fatalf("render (or passthrough) broken: %q", got)
	}
	if _, err := renderTemplate(filepath.Join(dir, "nope.tmpl"), nil); err == nil ||
		!strings.Contains(err.Error(), "file.template") {
		t.Fatalf("a missing template file must error: %v", err)
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
	planDir := t.TempDir()
	writeFile(t, planDir, "plan.shellf",
		"import r \"file://"+repo+"@v1.0.0\"\non web { r.deploy(\"9090\") }")
	writeFile(t, planDir, "inv.shellf", `host web = { address: "x", user: "u" }`)

	plan, defs, err := loadPlanPackage(
		filepath.Join(planDir, "plan.shellf"), filepath.Join(planDir, "inv.shellf"),
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

func TestRedact(t *testing.T) {
	got := redact("file.write(pass=S3cr3t!, /etc/x)\nstdout: S3cr3t!", []string{"S3cr3t!", ""})
	if strings.Contains(got, "S3cr3t!") {
		t.Fatalf("secret not redacted: %q", got)
	}
	if strings.Count(got, "***") != 2 {
		t.Fatalf("both occurrences should be masked: %q", got)
	}
	// An empty secret does not blank the whole string.
	if redact("abc", []string{""}) != "abc" {
		t.Fatal("empty secret must not redact")
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

func TestResolveDirCopy_ExpandsTree(t *testing.T) {
	dir := t.TempDir()
	// a small tree with a nested dir and a binary file
	if err := os.MkdirAll(filepath.Join(dir, "site", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "assets", "logo.bin"), []byte{0x00, 0xff, 0x10}, 0o644); err != nil {
		t.Fatal(err)
	}

	steps := []proto.Step{{Instruction: "dir.copy", Args: map[string]string{"src": "site", "dst": "/var/www/app"}}}
	out, err := resolveDirCopy(steps, dir)
	if err != nil {
		t.Fatal(err)
	}
	var dirEnsures, filePuts int
	var binContent string
	for _, s := range out {
		switch s.Instruction {
		case "dir.ensure":
			dirEnsures++
		case "file.put":
			filePuts++
			if s.Args["path"] == "/var/www/app/assets/logo.bin" {
				binContent = s.Args["content"]
			}
		default:
			t.Fatalf("unexpected step %q", s.Instruction)
		}
	}
	if dirEnsures < 2 || filePuts != 2 { // dst root + assets/ ; index.html + logo.bin
		t.Fatalf("expansion: %d dir-ensure, %d file-put", dirEnsures, filePuts)
	}
	// the binary file is base64 of the raw bytes (byte-for-byte)
	if got, _ := base64.StdEncoding.DecodeString(binContent); !bytes.Equal(got, []byte{0x00, 0xff, 0x10}) {
		t.Fatalf("binary member not base64-preserved: %v", got)
	}
}

func TestResolveDirCopy_Errors(t *testing.T) {
	dir := t.TempDir()
	// missing src
	if _, err := resolveDirCopy([]proto.Step{{Instruction: "dir.copy", Args: map[string]string{"src": "nope", "dst": "/x"}}}, dir); err == nil {
		t.Fatal("a missing src must error")
	}
	// a per-host ref for src/dst is rejected
	if _, err := resolveDirCopy([]proto.Step{{Instruction: "dir.copy", Args: map[string]string{"dst": "/x"}, Refs: map[string]string{"src": "v"}}}, dir); err == nil {
		t.Fatal("a ref src must error")
	}
}

func TestResolveDirCopy_RecursesAndCeiling(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "t"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t", "a"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dc := proto.Step{Instruction: "dir.copy", Args: map[string]string{"src": "t", "dst": "/d"}}

	// dir-copy nested in if / block / parallel is expanded in place.
	steps := []proto.Step{
		{If: &proto.IfBlock{Then: []proto.Step{dc}, Else: []proto.Step{dc}}},
		{Block: []proto.Step{dc}},
		{Parallel: []proto.Step{dc}},
	}
	out, err := resolveDirCopy(steps, dir)
	if err != nil {
		t.Fatal(err)
	}
	hasPut := func(steps []proto.Step) bool {
		for _, s := range steps {
			if s.Instruction == "file.put" {
				return true
			}
		}
		return false
	}
	if !hasPut(out[0].If.Then) || !hasPut(out[0].If.Else) || !hasPut(out[1].Block) || !hasPut(out[2].Parallel) {
		t.Fatalf("dir-copy not expanded inside if/block/parallel: %+v", out)
	}

	// the payload ceiling is enforced with a clear error.
	old := dirCopyCeiling
	dirCopyCeiling = 1 // 1 byte
	defer func() { dirCopyCeiling = old }()
	if _, err := resolveDirCopy([]proto.Step{dc}, dir); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("an oversized tree must be refused: %v", err)
	}
}

func TestReportText_Preview(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{{
			Host: "app1",
			Response: proto.Response{Results: []proto.StepResult{
				{Label: "compose-up(dir=/opt/app)", Category: "would", Tag: "up",
					Preview: "Recreate app-web-1\nRecreate app-worker-1"},
			}},
		}},
	}}
	text, _ := reportText(reports)
	for _, want := range []string{
		"compose-up(dir=/opt/app)", "would.up",
		"preview ▸ Recreate app-web-1",
		"preview ▸ Recreate app-worker-1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview not rendered (%q) in:\n%s", want, text)
		}
	}
}

func TestResolveDirCopy_AbsoluteSrc(t *testing.T) {
	// #281: an absolute src must be used as-is, not glued onto the plan dir.
	src := t.TempDir() // an absolute path unrelated to the plan dir
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	planDir := t.TempDir() // a different dir
	out, err := resolveDirCopy([]proto.Step{{Instruction: "dir.copy", Args: map[string]string{"src": src, "dst": "/d"}}}, planDir)
	if err != nil {
		t.Fatalf("absolute src must resolve as-is (#281): %v", err)
	}
	var puts int
	for _, s := range out {
		if s.Instruction == "file.put" {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("expected the file under the absolute src, got %d file-put", puts)
	}
}

func TestSrcPath(t *testing.T) {
	if got := srcPath("/plan/dir", "/abs/x"); got != "/abs/x" {
		t.Fatalf("absolute src should be used as-is: %q", got)
	}
	if got := srcPath("/plan/dir", "rel/x"); got != "/plan/dir/rel/x" {
		t.Fatalf("relative src should join the plan dir: %q", got)
	}
}

// Regression for #293: resolveDirCopy must reach a `dir.copy` wherever it can
// appear in the step tree. In a condition it cannot be expanded at all — one
// dir-copy becomes one step per file, and a condition holds a single step with a
// single Result — so the plan must be refused control-side with a clear message
// instead of shipping `dir.copy` to the agent, which fails the opaque `err.agent`.
func TestResolveDirCopy_EveryRecursivePosition(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dc := func() proto.Step {
		return proto.Step{Instruction: "dir.copy", Args: map[string]string{"src": "tree", "dst": "/d"}}
	}

	expanded := map[string]struct {
		in  proto.Step
		get func(proto.Step) []proto.Step
	}{
		"sequence": {dc(), nil}, // expands in place, checked separately
		"block":    {proto.Step{Block: []proto.Step{dc()}}, func(s proto.Step) []proto.Step { return s.Block }},
		"parallel": {proto.Step{Parallel: []proto.Step{dc()}}, func(s proto.Step) []proto.Step { return s.Parallel }},
		"if-then": {proto.Step{If: &proto.IfBlock{CondRef: &proto.ResultRef{Name: "x"}, Then: []proto.Step{dc()}}},
			func(s proto.Step) []proto.Step { return s.If.Then }},
		"if-else": {proto.Step{If: &proto.IfBlock{CondRef: &proto.ResultRef{Name: "x"}, Else: []proto.Step{dc()}}},
			func(s proto.Step) []proto.Step { return s.If.Else }},
	}
	for name, c := range expanded {
		t.Run(name, func(t *testing.T) {
			out, err := resolveDirCopy([]proto.Step{c.in}, dir)
			if err != nil {
				t.Fatal(err)
			}
			got := out
			if c.get != nil {
				got = c.get(out[0])
			}
			for _, s := range got {
				if s.Instruction == "dir.copy" {
					t.Fatalf("dir-copy left unexpanded in %s position: %+v", name, got)
				}
			}
			if len(got) == 0 {
				t.Fatalf("%s: nothing expanded", name)
			}
		})
	}

	t.Run("if-cond", func(t *testing.T) {
		in := proto.Step{If: &proto.IfBlock{Cond: &proto.Step{Instruction: "dir.copy", Args: map[string]string{"src": "tree", "dst": "/d"}}}}
		_, err := resolveDirCopy([]proto.Step{in}, dir)
		if err == nil {
			t.Fatal("dir-copy as a condition must be refused control-side, not shipped to the agent")
		}
		if !strings.Contains(err.Error(), "condition") {
			t.Fatalf("the error must say the condition is the problem, got: %v", err)
		}
	})
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

// parseDefsFor re-parses the shipped def sources so their `%` occurrences can be
// extracted for the allow-list. A source that no longer parses is skipped rather than
// fatal: it parsed once already, and failing the run here would be a worse diagnostic
// than the refusal the request will get.
func TestParseDefsFor(t *testing.T) {
	got := parseDefsFor(map[string]string{
		"a":      `def a(p: str) { apply { x = ~file.read(%"conf.j2") return ok.done } }`,
		"broken": `def b( {`,
	})
	if _, ok := got["a"]; !ok {
		t.Fatalf("a valid def must be parsed: %v", got)
	}
	if _, ok := got["broken"]; ok {
		t.Fatal("an unparsable source must be skipped, not returned")
	}

	declared := lang.ControlResources(got, nil)
	if len(declared) != 1 || declared[0] != "file.read:conf.j2" {
		t.Fatalf("the def's control-host resource must be extracted: %v", declared)
	}
}

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

// A plan that renders needs the channel even when it declares no `%"…"` path of its
// own — the content may come from the target. Missing this leaves ~file.render with no
// renderer at runtime.
func TestUsesRender(t *testing.T) {
	renders := parseDefsFor(map[string]string{
		"t": `def t(c: str) { apply { x = ~file.render(c) return ok.done } }`,
	})
	if !usesRender(renders) {
		t.Fatal("a def calling ~file.render must require the channel")
	}
	plain := parseDefsFor(map[string]string{
		"t": `def t(p: str) { apply { shell { echo "$p" } return ok.done } }`,
	})
	if usesRender(plain) {
		t.Fatal("a def that never renders must not force a channel open")
	}
}

func TestMergeVars(t *testing.T) {
	// ADR-0022 precedence: --set wins over the host, the host wins over globals.
	got := mergeVars(
		map[string]string{"a": "global", "b": "global"},
		map[string]string{"b": "host", "c": "host"},
		map[string]string{"c": "set"},
	)
	for k, want := range map[string]string{"a": "global", "b": "host", "c": "set"} {
		if got[k] != want {
			t.Errorf("%s: got %q, want %q", k, got[k], want)
		}
	}
}

// #311: a call cycle is refused when the defs are loaded, not when they run (ADR-0030
// §6). The distinction is the whole issue: the evaluator's guard fires on the target,
// after earlier steps of the plan have already acted, leaving a partially applied host.
//
// loadPlanPackage runs before anything is dialled, so a failure here *is* the "no host
// was contacted" assertion — there is no transport in this call path to stub out.
func TestLoadPlanPackage_RefusesACycleBeforeAnyTransport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.shellf", `on web { a("/x") }`)
	writeFile(t, dir, "a.shellf", `def a(p: str) { apply { b(p) return ok.done } }`)
	writeFile(t, dir, "b.shellf", `def b(p: str) { apply { a(p) return ok.done } }`)
	writeFile(t, dir, "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)

	_, _, err := loadPlanPackage(
		filepath.Join(dir, "plan.shellf"), filepath.Join(dir, "inventory.shellf"),
		map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("a cyclic package must not load")
	}
	if !strings.Contains(err.Error(), "call cycle: a -> b -> a") {
		t.Fatalf("the error must name the chain, got %v", err)
	}
}

// A cycle that only exists because a user def overrides a stdlib one and calls back into
// the caller. This is why the check takes the run's own resolver rather than the package
// map: seen from the user package alone, `file.write` is just a name that is not there.
func TestLoadPlanPackage_RefusesACycleThroughAnOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.shellf", `on web { deliver("/x", "hi") }`)
	writeFile(t, dir, "deliver.shellf", `def deliver(path: str, content: str) { apply { file.write(path, content) return ok.done } }`)
	writeFile(t, dir, "inventory.shellf", `host web = { address: "1.1.1.1", user: "u" }`)
	// A sub-package directory: its defs are qualified `<dir>.<def>` (ADR-0033), so this
	// one is `file.write` and overrides the stdlib def of that name.
	if err := os.MkdirAll(filepath.Join(dir, "file"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "file"), "write.shellf",
		`override def write(path: str, content: str) { apply { deliver(path, content) return ok.done } }`)

	_, _, err := loadPlanPackage(
		filepath.Join(dir, "plan.shellf"), filepath.Join(dir, "inventory.shellf"),
		map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("a cycle through an override must not load")
	}
	if !strings.Contains(err.Error(), "call cycle") {
		t.Fatalf("got %v", err)
	}
}
