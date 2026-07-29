// Package agent runs a per-host sequence of steps ON THE TARGET. The same shellf
// binary is pushed over SSH and re-invoked in agent mode; it reads a Request on
// stdin and writes a Response on stdout. Steps run sequentially; a `parallel`
// step runs its branches concurrently. A step that errors halts the rest of the
// sequence (the halting rule). An instruction resolves to an embedded stdlib def
// (executed by the language) or to a remaining Go builtin.
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"shellf/internal/engine"
	"shellf/internal/lang"
	"shellf/internal/proto"
	"shellf/internal/std"
)

// Serve reads one Request, runs its steps on this host, writes one Response.
func Serve(in io.Reader, out io.Writer, ex engine.Executor) error {
	var req proto.Request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return write(out, proto.Response{Error: fmt.Sprintf("decode: %v", err)})
	}

	m := mode(req.Mode)
	var resp proto.Response
	for _, step := range req.Steps {
		sr := runStep(step, ex, m)
		resp.Results = append(resp.Results, sr)
		if sr.Category == "err" {
			resp.Halted = true
			break
		}
	}
	return write(out, resp)
}

func runStep(step proto.Step, ex engine.Executor, m engine.Mode) proto.StepResult {
	if len(step.Parallel) > 0 {
		subs := make([]proto.StepResult, len(step.Parallel))
		var wg sync.WaitGroup
		for i, b := range step.Parallel {
			wg.Add(1)
			go func(i int, b proto.Step) { defer wg.Done(); subs[i] = runStep(b, ex, m) }(i, b)
		}
		wg.Wait()

		cat := "ok" // aggregate: err if any branch errs
		for _, s := range subs {
			if s.Category == "err" {
				cat = "err"
			}
		}
		return proto.StepResult{Label: "parallel", Category: cat, Sub: subs}
	}

	res, err := runInstruction(step, ex, m)
	if err != nil {
		return proto.StepResult{Label: step.Label(), Category: "err", Tag: "agent"}
	}
	return proto.StepResult{Label: step.Label(), Category: res.Category.String(), Tag: res.Tag, Shell: res.Shell}
}

// runInstruction resolves an instruction to an embedded stdlib def (run by the
// language) or a remaining Go builtin.
func runInstruction(step proto.Step, ex engine.Executor, m engine.Mode) (engine.Result, error) {
	if def, ok := std.Lookup(step.Instruction); ok {
		return lang.EvalDef(def, step.Args, ex, m)
	}
	inst, err := dispatch(step)
	if err != nil {
		return engine.Result{}, err
	}
	return engine.Run(inst, ex, m), nil
}

func dispatch(step proto.Step) (engine.Instruction, error) {
	switch step.Instruction {
	case "file-copy":
		return engine.FileCopy{Src: step.Args["src"], Dst: step.Args["dst"]}, nil
	case "service":
		return engine.Service{
			Unit:    step.Args["name"],
			Running: step.Args["running"] == "true",
			Enabled: step.Args["enabled"] == "true",
		}, nil
	case "shell":
		return engine.Shell{Cmd: step.Args["cmd"], Unless: step.Args["unless"]}, nil
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

func write(out io.Writer, r proto.Response) error {
	return json.NewEncoder(out).Encode(r)
}
