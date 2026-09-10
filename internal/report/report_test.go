package report

import (
	"encoding/json"
	"strings"
	"testing"

	"shellf/internal/engine"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// Moved here with the code they exercise (#491). They used to reach the renderers through
// `cmd/shellf`, which is why the layer an operator reads sat at 46.7% coverage: testing it
// meant driving the whole command.

type errFake string

func (e errFake) Error() string { return string(e) }

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
	got, _ := Status(reports)
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
	text, anyErr := Text(reports)
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
	if _, anyErr := Text([]orchestrator.BlockReport{{Target: "web", Hosts: []orchestrator.HostOutcome{
		{Host: "app1", Response: proto.Response{Results: []proto.StepResult{{Label: "x", Category: "ok"}}}},
	}}}); anyErr {
		t.Fatal("a clean run must not set anyErr")
	}
}

func TestRedact(t *testing.T) {
	got := Redact("file.write(pass=S3cr3t!, /etc/x)\nstdout: S3cr3t!", []string{"S3cr3t!", ""})
	if strings.Contains(got, "S3cr3t!") {
		t.Fatalf("secret not redacted: %q", got)
	}
	if strings.Count(got, "***") != 2 {
		t.Fatalf("both occurrences should be masked: %q", got)
	}
	// An empty secret does not blank the whole string.
	if Redact("abc", []string{""}) != "abc" {
		t.Fatal("empty secret must not redact")
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
	text, _ := Text(reports)
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

// #356: a plan that catches an error with `?` and handles it did its job, yet shellf
// exited 1 — so `shellf run … && echo deployed` never printed, and the language's own
// error handling was unusable from a script or a CI job.
//
// Found by running examples/plans/webserver.shellf, which demonstrates `?` on purpose:
// nothing failed, and the run reported failure.
func TestReportText_ACaughtErrorIsNotARunFailure(t *testing.T) {
	caught := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{{
			Host: "app1",
			Response: proto.Response{Results: []proto.StepResult{
				{Label: "apt.install(pkg=absent)", Category: "err", Tag: "runtime", Caught: true},
				{Label: "shell(logger …)", Category: "ok", Tag: "ran"},
			}},
		}},
	}}
	if _, anyErr := Text(caught); anyErr {
		t.Fatal("an error the plan caught and handled must not fail the run")
	}

	// The other half: an uncaught error still fails, or `?` would be a way to make every
	// failure invisible.
	uncaught := caught
	uncaught[0].Hosts[0].Response.Results[0].Caught = false
	if _, anyErr := Text(uncaught); !anyErr {
		t.Fatal("an uncaught error must still fail the run")
	}
}

// The whole point of #451 is the exit code, so it is asserted here rather than on the
// report string: the text was already empty-and-harmless, and every string assertion
// passed while `shellf run … && echo deployed` printed `deployed`.
func TestReportText_UnknownTargetErrorsTheRun(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "wbe",
		Err:    &orchestrator.UnknownTargetError{Target: "wbe"},
	}}
	text, anyErr := Text(reports)
	if !anyErr {
		t.Fatal("an unknown target must make the run exit non-zero")
	}
	if !strings.Contains(text, "wbe") {
		t.Fatalf("the report must name the target: %q", text)
	}
}

// A group with no members is a success, and it must say so: an empty block line reads
// exactly like a block that converged (#451).
func TestReportText_EmptyBlockSaysSo(t *testing.T) {
	reports := []orchestrator.BlockReport{{Target: "spare"}}
	text, anyErr := Text(reports)
	if anyErr {
		t.Fatal("an empty group is not an error")
	}
	if !strings.Contains(text, "no hosts") {
		t.Fatalf("an empty block must report why it did nothing: %q", text)
	}
}

// A run has to be consumable by something other than a human: a CI step gating on what
// changed, a dashboard, a script. Parsing the prose breaks the day a line is reworded
// (#459).
func TestReportJSON_CarriesTheSameVerdictsAsTheText(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{
			{Host: "app1", Response: proto.Response{
				Results: []proto.StepResult{
					{Label: "apt.install(nginx)", Category: "ok", Tag: "installed", Changed: true},
					{Label: "shell(bad)", Category: "err", Tag: "runtime"},
				},
				Halted: true,
			}},
			{Host: "app2", Err: errFake("dial refused")},
		},
	}}

	out, anyErr, err := JSON(reports)
	if err != nil {
		t.Fatal(err)
	}
	_, textErr := Text(reports)
	if anyErr != textErr {
		t.Fatalf("the two renderers must agree on failure: json=%v text=%v", anyErr, textErr)
	}

	var got jsonReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the output must parse: %v\n%s", err, out)
	}
	if got.Version == 0 {
		t.Fatal("the shape is a contract once published; it carries a version")
	}
	if len(got.Blocks) != 1 || got.Blocks[0].Target != "web" {
		t.Fatalf("blocks: %+v", got.Blocks)
	}
	if len(got.Blocks[0].Hosts) != 2 {
		t.Fatalf("both hosts must appear, including the unreachable one: %+v", got.Blocks[0].Hosts)
	}
	h := got.Blocks[0].Hosts[0]
	if len(h.Results) != 2 || h.Results[0].Tag != "installed" || !h.Halted {
		t.Fatalf("host detail lost: %+v", h)
	}
	if got.Blocks[0].Hosts[1].Error == "" {
		t.Fatal("a host that could not be reached must carry its error")
	}
}

// A block-level failure (#451) has no host to hang on, and must survive into the JSON —
// otherwise a consumer sees an empty block and reads it as "nothing to do".
func TestReportJSON_CarriesBlockErrors(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "wbe",
		Err:    &orchestrator.UnknownTargetError{Target: "wbe"},
	}}
	out, anyErr, err := JSON(reports)
	if err != nil {
		t.Fatal(err)
	}
	if !anyErr {
		t.Fatal("an unknown target must fail the run in JSON mode too")
	}
	var got jsonReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Blocks[0].Error == "" || !strings.Contains(got.Blocks[0].Error, "wbe") {
		t.Fatalf("the block error must be named: %+v", got.Blocks[0])
	}
}

// `--json` must not become a secret-exfiltration flag. The trap is JSON escaping: a secret
// holding a quote or a backslash is not present verbatim in the encoded bytes, so masking
// the rendered string is not enough on its own.
func TestReportJSON_RedactsSecretsIncludingEscapedForms(t *testing.T) {
	for _, secret := range []string{"pl41ntext", `qu"ote`, `back\slash`, "new\nline"} {
		reports := []orchestrator.BlockReport{{
			Target: "web",
			Hosts: []orchestrator.HostOutcome{{Host: "app1", Response: proto.Response{
				Results: []proto.StepResult{{
					Label:    "file.write(content=" + secret + ")",
					Category: "ok",
					Tag:      "written",
					Preview:  "wrote " + secret,
				}},
			}}},
		}}
		out, _, _ := JSON(reports)
		masked := RedactJSON(out, []string{secret})
		if strings.Contains(masked, secret) {
			t.Fatalf("the raw secret survived: %q in %s", secret, masked)
		}
		// And the escaped form, which is what actually sits in the encoded bytes.
		esc, _ := json.Marshal(secret)
		inner := string(esc[1 : len(esc)-1])
		if strings.Contains(masked, inner) {
			t.Fatalf("the escaped secret survived: %q in %s", inner, masked)
		}
		if !json.Valid([]byte(masked)) {
			t.Fatalf("masking must keep the output parseable: %s", masked)
		}
	}
}

// The JSON renderer must agree with the text one on the cases that decide the exit code,
// not only on the happy path: a caught error is not a failure (ADR-0009, #356), and an
// uncaught one is.
func TestReportJSON_CaughtErrorIsNotAFailure(t *testing.T) {
	caught := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{{Host: "h1", Response: proto.Response{
			Results: []proto.StepResult{{Label: "s", Category: "err", Tag: "runtime", Caught: true}},
		}}},
	}}
	if _, anyErr, _ := JSON(caught); anyErr {
		t.Fatal("an error the plan handled is not a failed run")
	}
	uncaught := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{{Host: "h1", Response: proto.Response{
			Results: []proto.StepResult{{Label: "s", Category: "err", Tag: "runtime"}},
		}}},
	}}
	if _, anyErr, _ := JSON(uncaught); !anyErr {
		t.Fatal("an uncaught error must fail the run")
	}
}

// An empty run still produces a valid document — a consumer parses it unconditionally, so
// "no blocks" must not mean "no JSON".
func TestReportJSON_EmptyRunStaysValid(t *testing.T) {
	out, anyErr, err := JSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	if anyErr {
		t.Fatal("an empty run did not fail")
	}
	var got jsonReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("must still parse: %v (%s)", err, out)
	}
	if got.Blocks == nil {
		t.Fatal("blocks must be an empty array, not null: a consumer iterates it")
	}
}

// An empty secret masks nothing: it would otherwise match everywhere and turn the whole
// document into asterisks.
func TestRedactJSON_IgnoresEmptySecrets(t *testing.T) {
	const doc = `{"host":"h1"}`
	if got := RedactJSON(doc, []string{""}); got != doc {
		t.Fatalf("an empty secret must be ignored, got %q", got)
	}
	if got := RedactJSON(doc, nil); got != doc {
		t.Fatalf("no secrets must leave the document untouched, got %q", got)
	}
}

// `status` renders block errors and empty blocks like `run` does (#451) — the paths that
// only a status sweep reaches.
func TestStatusReport_BlockErrorAndEmptyBlock(t *testing.T) {
	text, _ := Status([]orchestrator.BlockReport{
		{Target: "wbe", Err: &orchestrator.UnknownTargetError{Target: "wbe"}},
		{Target: "spare"},
	})
	if !strings.Contains(text, "wbe") {
		t.Fatalf("the block error must be named: %q", text)
	}
	if !strings.Contains(text, "no hosts") {
		t.Fatalf("an empty block must say so: %q", text)
	}
}

// A shell variable holds arbitrary content — a whole config file arrives as one `content`
// argument. Printed raw it breaks the report's shape, which is what a first run showed
// (#470).
func TestOneLine_BoundsAValue(t *testing.T) {
	if got := oneLine("hello\n"); got != "hello …" {
		t.Fatalf("a trailing newline must not split the line: %q", got)
	}
	if got := oneLine("first\nsecond\nthird"); got != "first …" {
		t.Fatalf("only the first line is kept: %q", got)
	}
	if got := oneLine(strings.Repeat("x", 200)); len(got) > 70 {
		t.Fatalf("a long value must be cut: %d chars", len(got))
	}
	if got := oneLine("/opt/app"); got != "/opt/app" {
		t.Fatalf("an ordinary value must pass through untouched: %q", got)
	}
}

// Render is the seam this package exists for: `cmd/shellf` prints what it returns and
// exits on the bool, so everything below is testable without driving the command (#491).
func TestRender_TextAndJSONBothRedact(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{{
			Host: "app1",
			Response: proto.Response{Results: []proto.StepResult{
				{Label: `file.write(content=hunter2)`, Category: "ok", Tag: "written"},
			}},
		}},
	}}
	for _, asJSON := range []bool{false, true} {
		out, anyErr, err := Render(reports, []string{"hunter2"}, asJSON)
		if err != nil {
			t.Fatal(err)
		}
		if anyErr {
			t.Fatalf("asJSON=%v: a run of ok results has not failed", asJSON)
		}
		if strings.Contains(out, "hunter2") {
			t.Fatalf("asJSON=%v: the secret reached the output: %s", asJSON, out)
		}
		if !strings.Contains(out, "file.write") {
			t.Fatalf("asJSON=%v: the step is missing from the output: %s", asJSON, out)
		}
	}
}

func TestRender_ReportsAFailingRun(t *testing.T) {
	reports := []orchestrator.BlockReport{{
		Target: "web",
		Hosts: []orchestrator.HostOutcome{{
			Host:     "app1",
			Response: proto.Response{Results: []proto.StepResult{{Label: "apt.install(pkg=nginx)", Category: "err", Tag: "runtime"}}},
		}},
	}}
	for _, asJSON := range []bool{false, true} {
		if _, anyErr, _ := Render(reports, nil, asJSON); !anyErr {
			t.Fatalf("asJSON=%v: an err result is a failed run", asJSON)
		}
	}
}

// What an operator reads when a shell fails: the command, where it was written, and the
// values it could see — sorted, because a map walk would reorder the report between two
// identical runs.
func TestShellSource_RendersCommandOriginAndVars(t *testing.T) {
	var b strings.Builder
	shellSource(&b, engine.ShellResult{
		Cmd:  "set -e\napt-get install -y \"$pkg\"",
		Def:  "apt.install",
		Line: 12,
		Vars: map[string]string{"pkg": "nginx", "cache": strings.Repeat("x", 200)},
	}, "  ")
	got := b.String()
	for _, want := range []string{"$ set -e   apt.install:12", "apt-get install", "cache = ", "pkg = nginx"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "cache ="), strings.Index(got, "pkg ="); i > j {
		t.Fatalf("variables must be sorted, or two identical runs differ:\n%s", got)
	}
	if strings.Contains(got, strings.Repeat("x", 200)) {
		t.Fatalf("a long value must be bounded:\n%s", got)
	}
}

// A ShellResult the agent built by hand carries no command; rendering a bare `$` for it
// would show the operator a prompt with nothing behind it.
func TestShellSource_SaysNothingWithoutACommand(t *testing.T) {
	var b strings.Builder
	shellSource(&b, engine.ShellResult{Exit: 1}, "  ")
	if b.String() != "" {
		t.Fatalf("a command-less result must render nothing, got %q", b.String())
	}
}

func TestStatusStep_ShapesByWhatTheStepIs(t *testing.T) {
	cases := map[string]struct {
		step proto.StepResult
		want []string
	}{
		"observed fields": {
			step: proto.StepResult{Label: "dir.ensure(path=/opt)", Fields: []engine.FieldDiff{
				{Name: "present", Current: "false", Desired: "true"},
				{Name: "mode", Current: "755", Converged: true},
			}},
			want: []string{"dir.ensure(path=/opt):", "present: false → true", "mode: 755"},
		},
		"an action-shaped def": {
			step: proto.StepResult{Label: "docker.prune(until=24h)", Tag: "action"},
			want: []string{"docker.prune", "action (no observable state)"},
		},
		"a check error": {
			step: proto.StepResult{Label: "file.replace(key=a=b)", Category: "err", Tag: "keyMustNotContainEquals"},
			want: []string{"file.replace", "err.keyMustNotContainEquals"},
		},
	}
	for what, c := range cases {
		t.Run(what, func(t *testing.T) {
			var b strings.Builder
			statusStep(&b, c.step, "  ")
			for _, w := range c.want {
				if !strings.Contains(b.String(), w) {
					t.Fatalf("missing %q in:\n%s", w, b.String())
				}
			}
		})
	}
}

// #615. `status` exited 0 over a fleet where every host was unreachable: it asked
// `anyBlockError`, which answers about blocks, while the verdict it needed was the one
// `Text` and `JSON` already compute from the hosts. `Status` now returns it too, so the
// three renderers answer the same question the same way and the caller cannot pick the
// wrong one.
func TestStatus_ReportsAHostThatCouldNotBeReached(t *testing.T) {
	cases := map[string]struct {
		reports []orchestrator.BlockReport
		failed  bool
	}{
		"an unreachable host": {
			reports: []orchestrator.BlockReport{{
				Target: "web",
				Hosts:  []orchestrator.HostOutcome{{Host: "h1", Err: errFake("dial refused")}},
			}},
			failed: true,
		},
		"an unknown target": {
			reports: []orchestrator.BlockReport{
				{Target: "wbe", Err: &orchestrator.UnknownTargetError{Target: "wbe"}},
			},
			failed: true,
		},
		// Drift is what `status` is for, not a failure: a field that differs must still
		// exit 0, or a monitor cannot tell "unreachable" from "not converged".
		"a host reporting drift": {
			reports: []orchestrator.BlockReport{{
				Target: "web",
				Hosts: []orchestrator.HostOutcome{{Host: "h1", Response: proto.Response{
					Results: []proto.StepResult{{Label: "dir.ensure(path=/opt)", Fields: []engine.FieldDiff{
						{Name: "present", Current: "false", Desired: "true"},
					}}},
				}}},
			}},
			failed: false,
		},
		"a converged host": {
			reports: []orchestrator.BlockReport{{
				Target: "web",
				Hosts:  []orchestrator.HostOutcome{{Host: "h1", Response: proto.Response{}}},
			}},
			failed: false,
		},
	}
	for what, c := range cases {
		t.Run(what, func(t *testing.T) {
			_, failed := Status(c.reports)
			if failed != c.failed {
				t.Fatalf("failed = %v, want %v", failed, c.failed)
			}
		})
	}
}
