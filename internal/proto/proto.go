// Package proto holds the wire types exchanged between control and agent: the
// per-host Request (a sequence of steps) and its Response. Kept dependency-free
// (only engine) so both agent and lang can use it without an import cycle.
package proto

import (
	"sort"
	"strings"

	"shellf/internal/engine"
)

// Step is either a single instruction call (Instruction + Args) or a parallel
// block (Parallel set).
type Step struct {
	Instruction string            `json:"instruction,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
	Parallel    []Step            `json:"parallel,omitempty"`
}

// Label is a compact human-readable form for reports.
func (s Step) Label() string {
	if len(s.Parallel) > 0 {
		return "parallel"
	}
	if s.Instruction == "shell" {
		return "shell(" + firstLine(s.Args["cmd"]) + ")"
	}
	keys := make([]string, 0, len(s.Args))
	for k := range s.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = s.Args[k]
	}
	return s.Instruction + "(" + strings.Join(vals, ", ") + ")"
}

func firstLine(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		return cmd[:i] + " …"
	}
	if len(cmd) > 50 {
		return cmd[:50] + "…"
	}
	return cmd
}

type Request struct {
	Mode  string `json:"mode"` // "apply" | "check"
	Steps []Step `json:"steps"`
}

type StepResult struct {
	Label    string              `json:"label"`
	Category string              `json:"category"`
	Tag      string              `json:"tag,omitempty"`
	Shell    *engine.ShellResult `json:"shell,omitempty"`
	Sub      []StepResult        `json:"sub,omitempty"`
}

type Response struct {
	Results []StepResult `json:"results"`
	Halted  bool         `json:"halted,omitempty"`
	Error   string       `json:"error,omitempty"`
}
