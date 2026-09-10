package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `status` reads a plan with the same inputs `run` does (#640).
//
// It shipped without `--vars` and `--set`, and passed an empty map where `run` passes the
// globals — so the command whose whole job is answering "what would this plan see?" could
// not be handed what the plan sees. A plan using `${port}` failed to resolve under
// `status` while applying cleanly under `run`.
//
// Asserted by comparing the two commands' reports rather than by reading the code: the
// claim is that they resolve the same values, and only running both shows that.
func TestStatus_TakesTheSameInputsAsRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "shellf")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shellf: %v\n%s", err, out)
	}

	proj := projectDir(t, filepath.Join(tmp, "proj"))
	target := filepath.Join(tmp, "conf")
	writeFile(t, filepath.Join(proj, "inventories"), "inv.shellf", `host self = { local: "true" }`)
	writeFile(t, filepath.Join(proj, "plans"), "plan.shellf", `on self {
    dir.ensure("`+target+`")
    file.write("`+filepath.Join(target, "app.conf")+`", "port = ${port}")
}`)
	inv := filepath.Join(proj, "inventories", "inv.shellf")
	plan := filepath.Join(proj, "plans", "plan.shellf")

	shellf := func(args ...string) (string, error) {
		out, err := exec.Command(bin, append(args, plan)...).CombinedOutput()
		return string(out), err
	}

	// A vars file for the base layer, and a --set that must win over it.
	writeFile(t, proj, "vars.shellf", `port = "1"`)
	vars := filepath.Join(proj, "vars.shellf")

	status, err := shellf("status", "--inventory", inv, "--vars", vars, "--set", "port=8080")
	if err != nil {
		t.Fatalf("status with the plan's own inputs must resolve: %v\n%s", err, status)
	}
	preview, err := shellf("run", "--inventory", inv, "--dry-run", "--vars", vars, "--set", "port=8080")
	if err != nil {
		t.Fatalf("run --dry-run: %v\n%s", err, preview)
	}

	// Both reports carry the same resolved value, and neither carries the one from the
	// vars file that --set overrode. The path is not asserted: both reports elide a long
	// one, which would make this test about the renderer instead.
	for name, out := range map[string]string{"status": status, "run --dry-run": preview} {
		if !strings.Contains(out, "port = 8080") {
			t.Fatalf("%s did not resolve ${port} from --set:\n%s", name, out)
		}
		if strings.Contains(out, "port = 1\n") {
			t.Fatalf("%s used the --vars value where --set should have won:\n%s", name, out)
		}
	}

	// And a plan needing no variables behaves as before.
	writeFile(t, filepath.Join(proj, "plans"), "plain.shellf", `on self { dir.ensure("`+target+`") }`)
	out, err := exec.Command(bin, "status", "--inventory", inv,
		filepath.Join(proj, "plans", "plain.shellf")).CombinedOutput()
	if err != nil {
		t.Fatalf("status on a plan with no variables: %v\n%s", err, out)
	}
}
