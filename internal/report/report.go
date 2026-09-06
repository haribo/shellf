// Package report renders what a run shows its operator: the text report, its JSON
// form, the `status` view, and the secret redaction applied to both.
//
// Split out of `cmd/shellf` (#491), where it was entangled with flag parsing and process
// exit: the layer the operator reads was the least tested in the tree — 46.7% against a
// repo floor of 81% — because testing it meant driving the whole command. Nothing here
// writes to stdout or decides an exit code; it takes results and returns text, which is
// what makes it testable.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"os"

	"shellf/internal/engine"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// jsonReport is the machine-readable shape of a run. It is a contract the moment it
// ships, so it carries a version: a consumer can detect a change instead of discovering
// it as a parse error in production (#459).
type jsonReport struct {
	Version int         `json:"version"`
	Blocks  []jsonBlock `json:"blocks"`
}

type jsonBlock struct {
	Target string     `json:"target"`
	Error  string     `json:"error,omitempty"` // the block could not run at all (#451)
	Hosts  []jsonHost `json:"hosts"`
}

type jsonHost struct {
	Host    string             `json:"host"`
	Error   string             `json:"error,omitempty"` // unreachable, or a resolution failure
	Halted  bool               `json:"halted,omitempty"`
	Results []proto.StepResult `json:"results,omitempty"`
}

// jsonVersion is bumped when the shape changes in a way a consumer would notice.
const jsonVersion = 1

// Render is what `printReports` needs: the finished, redacted output and whether the run
// failed. Printing it and exiting stay with the caller — a package that calls os.Exit
// cannot be tested, which is the entanglement this split undoes.
func Render(reports []orchestrator.BlockReport, secrets []string, asJSON bool) (string, bool) {
	if asJSON {
		// stdout carries the report and nothing else, or it is not parseable. Anything
		// diagnostic belongs on stderr.
		out, anyErr := JSON(reports)
		return RedactJSON(out, secrets), anyErr
	}
	text, anyErr := Text(reports)
	return Redact(text, secrets), anyErr
}

// redact masks every non-empty secret value with `***` (by value, so it catches
// a secret wherever it surfaces — a label, a report, an echoed stdout). ADR-0018.
func Redact(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// statusReport renders the per-host state report: one line per resource, with a
// `current → desired` diff on each field that has drifted. Pure (returns the
// text) so it is unit-testable without capturing stdout.
func Status(reports []orchestrator.BlockReport) string {
	var b strings.Builder
	for _, blk := range reports {
		fmt.Fprintf(&b, "on %s:\n", blk.Target)
		// Block error and empty block, rendered as in reportText (#451).
		if blk.Err != nil {
			fmt.Fprintf(&b, "  ! %v\n", blk.Err)
			continue
		}
		if len(blk.Hosts) == 0 {
			fmt.Fprintf(&b, "  (no hosts)\n")
			continue
		}
		for _, h := range blk.Hosts {
			if h.Err != nil {
				fmt.Fprintf(&b, "  %s: unreachable (%v)\n", h.Host, h.Err)
				continue
			}
			fmt.Fprintf(&b, "  %s:\n", h.Host)
			for _, s := range h.Response.Results {
				statusStep(&b, s, "    ")
			}
		}
	}
	return b.String()
}

func statusStep(b *strings.Builder, s proto.StepResult, indent string) {
	switch {
	case len(s.Fields) > 0:
		fmt.Fprintf(b, "%s%s:\n", indent, s.Label)
		for _, f := range s.Fields {
			if f.Converged {
				fmt.Fprintf(b, "%s  %s: %s\n", indent, f.Name, orDash(f.Current))
			} else {
				fmt.Fprintf(b, "%s  %s: %s → %s\n", indent, f.Name, orDash(f.Current), f.Desired)
			}
		}
	case s.Tag == "action":
		fmt.Fprintf(b, "%s%-28s action (no observable state)\n", indent, s.Label)
	default: // control-flow wrappers, questions, check errors — one line, no recursion
		label := s.Category
		if s.Tag != "" {
			label += "." + s.Tag
		}
		fmt.Fprintf(b, "%s%-28s %s\n", indent, s.Label, label)
		// An error with no message is a dead end: `err.agent` alone sends the operator
		// looking at the target when the cause may be on their own machine. `run`
		// already prints it; `status` was silent.
		if s.Category == "err" && s.Shell != nil && strings.TrimSpace(s.Shell.Stderr) != "" {
			fmt.Fprintf(b, "%s  ! %s\n", indent, strings.TrimSpace(s.Shell.Stderr))
		}
	}
	for _, sub := range s.Sub {
		statusStep(b, sub, indent+"  ")
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// reportJSON renders the same run reportText does, as JSON, and reports whether any host
// errored. The two must agree on that boolean: a consumer branching on the exit code and
// a human reading the prose have to see the same run.
func JSON(reports []orchestrator.BlockReport) (string, bool) {
	out := jsonReport{Version: jsonVersion, Blocks: make([]jsonBlock, 0, len(reports))}
	anyErr := false

	for _, blk := range reports {
		jb := jsonBlock{Target: blk.Target, Hosts: []jsonHost{}}
		if blk.Err != nil {
			jb.Error = blk.Err.Error()
			anyErr = true
			out.Blocks = append(out.Blocks, jb)
			continue
		}
		for _, h := range blk.Hosts {
			jh := jsonHost{Host: h.Host}
			if h.Err != nil {
				jh.Error = h.Err.Error()
				anyErr = true
				jb.Hosts = append(jb.Hosts, jh)
				continue
			}
			jh.Results, jh.Halted = h.Response.Results, h.Response.Halted
			for _, st := range h.Response.Results {
				// Same rule as the text renderer: a caught error is not a failed run
				// (ADR-0009, #356).
				anyErr = anyErr || (st.Category == "err" && !st.Caught)
			}
			jb.Hosts = append(jb.Hosts, jh)
		}
		out.Blocks = append(out.Blocks, jb)
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		// Nothing here can fail to marshal — every field is a plain Go value — but a
		// silent empty report would be worse than a loud one.
		fmt.Fprintf(os.Stderr, "rendering the JSON report: %v\n", err)
		os.Exit(1)
	}
	return string(b) + "\n", anyErr
}

// redactJSON masks secrets in encoded JSON.
//
// `redact` alone is not enough here, and the gap is not theoretical: JSON escapes quotes,
// backslashes and newlines, so a secret containing any of them is simply *not present*
// verbatim in the encoded bytes — the plain ReplaceAll walks straight past it. Each secret
// is therefore masked in both forms, raw and as JSON would write it.
func RedactJSON(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, "***")
		if enc, err := json.Marshal(sec); err == nil && len(enc) >= 2 {
			s = strings.ReplaceAll(s, string(enc[1:len(enc)-1]), "***")
		}
	}
	return s
}

// reportText renders the run/check report and reports whether any host errored.
// Pure (returns the text) so it is unit-testable without capturing stdout.
func Text(reports []orchestrator.BlockReport) (string, bool) {
	var b strings.Builder
	anyErr := false
	for _, blk := range reports {
		fmt.Fprintf(&b, "on %s:\n", blk.Target)
		// A block that could not run at all: no host to attach an outcome to, so the
		// reason goes on the block. Without this the block printed its header and
		// nothing else, and the run exited 0 (#451).
		if blk.Err != nil {
			fmt.Fprintf(&b, "  ! %v\n", blk.Err)
			anyErr = true
			continue
		}
		// A target that resolves to nobody is a legitimate no-op, and it says so: an
		// empty block reads exactly like a block where everything converged.
		if len(blk.Hosts) == 0 {
			fmt.Fprintf(&b, "  (no hosts)\n")
			continue
		}
		for _, h := range blk.Hosts {
			if h.Err != nil {
				var re *orchestrator.ResolveError
				if errors.As(h.Err, &re) {
					fmt.Fprintf(&b, "  %s: %v\n", h.Host, h.Err) // resolution error, not unreachable
				} else {
					fmt.Fprintf(&b, "  %s: unreachable (%v)\n", h.Host, h.Err)
				}
				anyErr = true
				continue
			}
			fmt.Fprintf(&b, "  %s:\n", h.Host)
			for _, s := range h.Response.Results {
				stepText(&b, s, "    ")
				// A caught error is not a failed run: `?` means the plan handles it,
				// and it did (ADR-0009). Counting it made `shellf run … && …` never
				// succeed for any plan using the language's own error handling (#356).
				anyErr = anyErr || (s.Category == "err" && !s.Caught)
			}
			if h.Response.Halted {
				fmt.Fprintf(&b, "    (halted)\n")
			}
		}
	}
	// Every target was refused, so no block was executed. Say it: the report above is a
	// list of names, and nothing distinguishes it from a run that did work.
	if len(reports) > 0 && allUnknownTargets(reports) {
		fmt.Fprintf(&b, "nothing ran: fix the target name(s) above, or the inventory\n")
	}
	return b.String(), anyErr
}

func allUnknownTargets(reports []orchestrator.BlockReport) bool {
	for _, blk := range reports {
		var ue *orchestrator.UnknownTargetError
		if !errors.As(blk.Err, &ue) {
			return false
		}
	}
	return true
}

// shellSource renders one shell block: the source text, where it was written, and the
// values it could read.
//
// The text is the source, with `$var` unexpanded, because that is what actually ran —
// values reach the shell through its environment and no substituted command line ever
// exists (internal/engine/executor.go). Printing a reconstructed one would show a string
// that never executed, and would put a secret parameter in plain sight.
func shellSource(b *strings.Builder, r engine.ShellResult, indent string) {
	if r.Cmd == "" {
		return // a diagnostic ShellResult the agent built by hand, with no command behind it
	}
	where := ""
	if r.Def != "" && r.Line > 0 {
		where = fmt.Sprintf("   %s:%d", r.Def, r.Line)
	}
	for i, line := range strings.Split(strings.TrimRight(r.Cmd, "\n"), "\n") {
		if i == 0 {
			fmt.Fprintf(b, "%s    $ %s%s\n", indent, line, where)
			continue
		}
		fmt.Fprintf(b, "%s      %s\n", indent, line)
	}
	// Sorted: a map walk would reorder the report between two identical runs.
	names := make([]string, 0, len(r.Vars))
	for n := range r.Vars {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(b, "%s        %s = %s\n", indent, n, oneLine(r.Vars[n]))
	}
}

// oneLine renders a value on a single bounded line. A variable holds arbitrary content —
// a whole config file arrives as one `content` argument — and printing it raw breaks the
// report's shape and floods it.
func oneLine(v string) string {
	const max = 60
	if i := strings.IndexByte(v, '\n'); i >= 0 {
		v = strings.TrimRight(v[:i], "\r") + " …"
	}
	if len(v) > max {
		v = v[:max] + " …"
	}
	return v
}

func stepText(b *strings.Builder, s proto.StepResult, indent string) {
	label := s.Category
	if s.Tag != "" {
		label += "." + s.Tag
	}
	fmt.Fprintf(b, "%s%-24s %s\n", indent, s.Label, label)
	// Show the preview/error payload (e.g. a file-copy diff in check mode).
	if s.Shell != nil && s.Shell.Stdout != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Shell.Stdout, "\n"), "\n") {
			fmt.Fprintf(b, "%s    | %s\n", indent, line)
		}
	}
	// The command that failed, and where it is written. Without it a report says a step
	// exited 2 and never which of a def's commands did (#470).
	if s.Category == "err" && s.Shell != nil {
		shellSource(b, *s.Shell, indent)
	}
	// Under `-v`, every command the step ran — the successful ones included, which is the
	// difference between a verbose run and a silent one.
	for _, r := range s.Ran {
		shellSource(b, r, indent)
	}
	// On a failure, what went wrong. Without this the diagnostics the agent attaches —
	// an unbound variable, a refused resource, a call cycle — are written and never
	// seen, leaving `err.agent` to mean "something broke, good luck".
	if s.Category == "err" && s.Shell != nil && s.Shell.Stderr != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Shell.Stderr, "\n"), "\n") {
			fmt.Fprintf(b, "%s    ! %s\n", indent, line)
		}
	}
	// An action-shaped def's `--check` preview: what apply would do, marked so it
	// never reads as a convergence claim (ADR-0029).
	if s.Preview != "" {
		for _, line := range strings.Split(strings.TrimRight(s.Preview, "\n"), "\n") {
			fmt.Fprintf(b, "%s    preview ▸ %s\n", indent, line)
		}
	}
	for _, sub := range s.Sub {
		stepText(b, sub, indent+"  ")
	}
}
