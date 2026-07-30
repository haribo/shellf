// Package proto holds the wire types exchanged between control and agent: the
// per-host Request (a sequence of steps) and its Response. Kept dependency-free
// (only engine) so both agent and lang can use it without an import cycle.
package proto

import (
	"fmt"
	"sort"
	"strings"

	"shellf/internal/engine"
)

// ResolveRefs returns a copy of steps with every Ref resolved against env and
// merged into Args (an unresolved ref is an error). Recurses into Parallel.
// Called per host by the orchestrator, so the agent only ever sees Args.
func ResolveRefs(steps []Step, env map[string]string) ([]Step, error) {
	out := make([]Step, len(steps))
	for i, s := range steps {
		if len(s.Parallel) > 0 {
			sub, err := ResolveRefs(s.Parallel, env)
			if err != nil {
				return nil, err
			}
			out[i] = Step{Parallel: sub}
			continue
		}
		args := make(map[string]string, len(s.Args)+len(s.Refs))
		for k, v := range s.Args {
			args[k] = v
		}
		for argName, varName := range s.Refs {
			v, ok := env[varName]
			if !ok {
				return nil, fmt.Errorf("undefined variable %q", varName)
			}
			args[argName] = v
		}
		out[i] = Step{Instruction: s.Instruction, Args: args}
	}
	return out, nil
}

// Step is either a single instruction call (Instruction + Args) or a parallel
// block (Parallel set). Refs holds bare-identifier arguments not yet resolved
// (argName → varName); the orchestrator resolves them per host into Args before
// the Request is sent, so the agent never sees a Ref.
type Step struct {
	Instruction string            `json:"instruction,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
	Refs        map[string]string `json:"refs,omitempty"`
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
		vals[i] = firstLine(s.Args[k]) // truncate long/multi-line values (e.g. file-write content)
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
