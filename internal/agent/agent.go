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

	results, halted := runSteps(req.Steps, ex, mode(req.Mode), map[string]engine.Result{})
	return write(out, proto.Response{Results: results, Halted: halted})
}

// runSteps executes a sequence, halting on the first err (the halting rule).
// scope holds the Results captured by `name = <call>` steps in this block.
func runSteps(steps []proto.Step, ex engine.Executor, m engine.Mode, scope map[string]engine.Result) (results []proto.StepResult, halted bool) {
	for _, step := range steps {
		sr := runStep(step, ex, m, scope)
		results = append(results, sr)
		if sr.Category == "err" {
			halted = true
			break
		}
	}
	return results, halted
}

// runIf evaluates the condition, then takes the branch on its truth (an inline
// instruction's Result being `ok`, or a captured result's outcome test). In
// check, a would-condition (effect not applied) makes the branch undetermined —
// the then-branch is previewed but never claimed to run.
func runIf(ib *proto.IfBlock, ex engine.Executor, m engine.Mode, scope map[string]engine.Result) proto.StepResult {
	var condResult engine.Result
	var condSub *proto.StepResult // the inline cond's own result, shown in Sub
	var label string
	// Default predicate (inline cond): the instruction's Result is `ok`.
	truthFn := func(r engine.Result) bool { return r.Category == engine.OK }

	if ib.CondRef != nil {
		ref := ib.CondRef
		label = "if(" + ref.Label() + ")"
		r, ok := scope[ref.Name]
		if !ok {
			return proto.StepResult{Label: label, Category: "err", Tag: "undefinedResult"}
		}
		condResult = r
		truthFn = func(res engine.Result) bool { return refTruth(res, ref) }
	} else {
		label = "if(" + ib.Cond.Label() + ")"
		res, err := runInstruction(*ib.Cond, ex, m)
		if err != nil {
			return proto.StepResult{Label: label, Category: "err", Tag: "agent"}
		}
		condResult = res
		condSub = &proto.StepResult{Label: ib.Cond.Label(), Category: res.Category.String(), Tag: res.Tag, Changed: res.Changed, Shell: res.Shell}
	}

	// Never-lie: an unapplied action's result is undetermined in check.
	if m == engine.Check && condResult.Category == engine.WOULD {
		preview, _ := runSteps(ib.Then, ex, m, scope)
		return proto.StepResult{Label: label, Category: "undetermined", Sub: withCond(condSub, preview)}
	}

	truth := truthFn(condResult)
	if ib.Negate {
		truth = !truth
	}
	branch := ib.Else // false/err → else (a captured result never halts)
	if truth {
		branch = ib.Then
	}
	subs, halted := runSteps(branch, ex, m, scope)
	cat := "ok"
	if halted {
		cat = "err"
	}
	return proto.StepResult{Label: label, Category: cat, Sub: withCond(condSub, subs)}
}

// refTruth evaluates a captured-result test: the `changed` flag, or an outcome
// pattern — category match plus an optional tag match ("" tag = wildcard).
func refTruth(r engine.Result, ref *proto.ResultRef) bool {
	if ref.Changed {
		return r.Changed
	}
	if r.Category.String() != ref.Category {
		return false
	}
	return ref.Tag == "" || r.Tag == ref.Tag
}

func withCond(cond *proto.StepResult, subs []proto.StepResult) []proto.StepResult {
	if cond == nil {
		return subs
	}
	return append([]proto.StepResult{*cond}, subs...)
}

func runStep(step proto.Step, ex engine.Executor, m engine.Mode, scope map[string]engine.Result) proto.StepResult {
	if step.If != nil {
		return runIf(step.If, ex, m, scope)
	}
	if len(step.Parallel) > 0 {
		subs := make([]proto.StepResult, len(step.Parallel))
		var wg sync.WaitGroup
		for i, b := range step.Parallel {
			wg.Add(1)
			// Each branch gets a copy of the scope: reads see prior captures,
			// writes stay local (no data race; captures inside parallel don't escape).
			go func(i int, b proto.Step) { defer wg.Done(); subs[i] = runStep(b, ex, m, copyScope(scope)) }(i, b)
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
	if step.Bind != "" {
		scope[step.Bind] = res
	}
	return proto.StepResult{Label: step.Label(), Category: res.Category.String(), Tag: res.Tag, Changed: res.Changed, Shell: res.Shell}
}

func copyScope(s map[string]engine.Result) map[string]engine.Result {
	c := make(map[string]engine.Result, len(s))
	for k, v := range s {
		c[k] = v
	}
	return c
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
