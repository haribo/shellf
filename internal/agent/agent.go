// Package agent runs a per-host sequence of steps ON THE TARGET. The same shellf
// binary is pushed over SSH and re-invoked in agent mode; it reads a Request on
// stdin and writes a Response on stdout. Steps run sequentially; a `parallel`
// step runs its branches concurrently on the target. A step that errors halts
// the rest of the sequence (the halting rule).
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"shellf/internal/engine"
)

// Step is either a single instruction call (Instruction + Args) or a parallel
// block (Parallel set). Args are instruction-specific: {"pkg": …} for
// apt-install, {"src": …, "dst": …} for file-copy.
type Step struct {
	Instruction string            `json:"instruction,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
	Parallel    []Step            `json:"parallel,omitempty"`
}

func (s Step) label() string {
	if len(s.Parallel) > 0 {
		return "parallel"
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

type Request struct {
	Mode  string `json:"mode"` // "apply" | "check"
	Steps []Step `json:"steps"`
}

type StepResult struct {
	Label    string              `json:"label"`
	Category string              `json:"category"`
	Tag      string              `json:"tag,omitempty"`
	Shell    *engine.ShellResult `json:"shell,omitempty"`
	Sub      []StepResult        `json:"sub,omitempty"` // parallel branch results
}

type Response struct {
	Results []StepResult `json:"results"`
	Halted  bool         `json:"halted,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// Serve reads one Request, runs its steps on this host, writes one Response.
func Serve(in io.Reader, out io.Writer, ex engine.Executor) error {
	var req Request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return write(out, Response{Error: fmt.Sprintf("decode: %v", err)})
	}

	m := mode(req.Mode)
	var resp Response
	for _, step := range req.Steps {
		sr := runStep(step, ex, m)
		resp.Results = append(resp.Results, sr)
		if sr.Category == "err" {
			resp.Halted = true
			break // halt the sequence on this host
		}
	}
	return write(out, resp)
}

func runStep(step Step, ex engine.Executor, m engine.Mode) StepResult {
	if len(step.Parallel) > 0 {
		subs := make([]StepResult, len(step.Parallel))
		var wg sync.WaitGroup
		for i, b := range step.Parallel {
			wg.Add(1)
			go func(i int, b Step) { defer wg.Done(); subs[i] = runStep(b, ex, m) }(i, b)
		}
		wg.Wait()

		cat := "ok" // aggregate: err if any branch errs
		for _, s := range subs {
			if s.Category == "err" {
				cat = "err"
			}
		}
		return StepResult{Label: "parallel", Category: cat, Sub: subs}
	}

	inst, err := dispatch(step)
	if err != nil {
		return StepResult{Label: step.label(), Category: "err", Tag: "agent"}
	}
	res := engine.Run(inst, ex, m)
	return StepResult{Label: step.label(), Category: res.Category.String(), Tag: res.Tag, Shell: res.Shell}
}

func dispatch(step Step) (engine.Instruction, error) {
	switch step.Instruction {
	case "apt-install":
		return engine.AptInstall{Pkg: step.Args["pkg"]}, nil
	case "file-copy":
		return engine.FileCopy{Src: step.Args["src"], Dst: step.Args["dst"]}, nil
	case "service":
		return engine.Service{
			Unit:    step.Args["name"],
			Running: step.Args["running"] == "true",
			Enabled: step.Args["enabled"] == "true",
		}, nil
	default:
		return nil, fmt.Errorf("unknown instruction: %q", step.Instruction)
	}
}

func mode(s string) engine.Mode {
	if s == "check" {
		return engine.Check
	}
	return engine.Apply
}

func write(out io.Writer, r Response) error {
	return json.NewEncoder(out).Encode(r)
}
