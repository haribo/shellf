package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end proof of the local transport (ADR-0027, #261): build shellf, then run
// a real plan against a `local: "true"` host with no SSH, and assert it provisions
// the control host. Same agent/request/protocol as SSH — just no network.
func TestLocalTransport_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "shellf")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shellf: %v\n%s", err, out)
	}

	target := filepath.Join(tmp, "provisioned")
	writeFile(t, tmp, "inv.shellf", `host self = { local: "true" }`)
	writeFile(t, tmp, "plan.shellf", `on self {
    dir.ensure("`+target+`")
    file.write("`+filepath.Join(target, "marker")+`", "made locally")
}`)

	shellf := func() (string, error) {
		out, err := exec.Command(bin, "run",
			"--inventory", filepath.Join(tmp, "inv.shellf"),
			filepath.Join(tmp, "plan.shellf")).CombinedOutput()
		return string(out), err
	}

	if out, err := shellf(); err != nil {
		t.Fatalf("local run: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(target, "marker")); err != nil || string(b) != "made locally" {
		t.Fatalf("the plan did not provision the control host: %q (%v)", b, err)
	}

	// A second run is idempotent: the report shows the converged skip.
	out, err := shellf()
	if err != nil {
		t.Fatalf("re-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "already") {
		t.Fatalf("re-run should report a converged skip:\n%s", out)
	}
}
