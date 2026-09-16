package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The three layers of ADR-0057, asserted on the built binary: a flag wins over `shellf.conf`,
// the file wins over the default, and the default holds when neither says anything (#664).
//
// The assertion runs against `-v`'s policy line, which exists for this: before it, nothing
// observed which layer won, so this test could not have been written at all.
func TestPolicy_FlagBeatsFileBeatsDefault(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "shellf")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shellf: %v\n%s", err, out)
	}

	proj := projectDir(t, filepath.Join(tmp, "proj"))
	target := filepath.Join(tmp, "made")
	writeFile(t, filepath.Join(proj, "inventories"), "inv.shellf", `host self = { local: "true" }`)
	writeFile(t, filepath.Join(proj, "plans"), "plan.shellf", `on self { dir.ensure("`+target+`") }`)
	inv := filepath.Join(proj, "inventories", "inv.shellf")
	plan := filepath.Join(proj, "plans", "plan.shellf")

	run := func(args ...string) string {
		t.Helper()
		full := append([]string{"run", "--inventory", inv, "-v", "--dry-run"}, args...)
		out, err := exec.Command(bin, append(full, plan)...).CombinedOutput()
		if err != nil {
			t.Fatalf("shellf %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// Nothing set anywhere: the built-in defaults, and they say so.
	if got := run(); !strings.Contains(got, "parallel 16 (default)") ||
		!strings.Contains(got, "agent-ttl 2h (default)") {
		t.Fatalf("with no shellf.conf the defaults must show as defaults:\n%s", got)
	}

	writeFile(t, proj, "shellf.conf", "parallel = \"8\"\nagent-ttl = \"4h\"\n")

	// The file is in force, and named as the source.
	got := run()
	if !strings.Contains(got, "parallel 8 (shellf.conf)") {
		t.Errorf("the file must set parallel:\n%s", got)
	}
	if !strings.Contains(got, "agent-ttl 4h (shellf.conf)") {
		t.Errorf("the file must set agent-ttl:\n%s", got)
	}

	// A flag overrides it — and only the one given. The reason this layering exists at all is
	// CI: an unattended run overrides without editing a committed file (ADR-0057 §2).
	got = run("--parallel", "3")
	if !strings.Contains(got, "parallel 3 (flag)") {
		t.Errorf("the flag must beat the file:\n%s", got)
	}
	if !strings.Contains(got, "agent-ttl 4h (shellf.conf)") {
		t.Errorf("a flag for one setting must not displace the file for another:\n%s", got)
	}
}

// A setting the file gets wrong stops the run naming it, rather than being skipped — a file is
// read once, so a line that does nothing is a setting its author believes is in force.
func TestPolicy_AnUnknownSettingStopsTheRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "shellf")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shellf: %v\n%s", err, out)
	}
	proj := projectDir(t, filepath.Join(tmp, "proj"))
	writeFile(t, filepath.Join(proj, "inventories"), "inv.shellf", `host self = { local: "true" }`)
	writeFile(t, filepath.Join(proj, "plans"), "plan.shellf", `on self { dir.ensure("`+tmp+`/x") }`)

	for body, want := range map[string]string{
		"paralel = \"8\"\n":     `unknown setting "paralel"`,
		"insecure = \"true\"\n": "host-key verification",
	} {
		writeFile(t, proj, "shellf.conf", body)
		out, err := exec.Command(bin, "run", "--inventory",
			filepath.Join(proj, "inventories", "inv.shellf"),
			filepath.Join(proj, "plans", "plan.shellf")).CombinedOutput()
		if err == nil {
			t.Fatalf("%q must stop the run:\n%s", body, out)
		}
		if !strings.Contains(string(out), want) {
			t.Fatalf("%q must be refused saying %q, got:\n%s", body, want, out)
		}
		_ = os.Remove(filepath.Join(proj, "shellf.conf"))
	}
}

// shortDur is arithmetic because the string-trimming version was wrong in a way its own test
// missed: it turned `1h30m0s` into `1h3`, and passed because the case used was `4h` (#664).
func TestShortDur(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{4 * time.Hour, "4h"},
		{2 * time.Hour, "2h"},
		{90 * time.Minute, "90m"},   // the trimming version answered "1h3"
		{100 * time.Minute, "100m"}, // and "1h4"
		{30 * time.Second, "30s"},   // and "3"
		{time.Minute, "1m"},
		{0, "0s"},
		{90 * time.Second, "1m30s"},
	} {
		if got := shortDur(tc.in); got != tc.want {
			t.Errorf("shortDur(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
