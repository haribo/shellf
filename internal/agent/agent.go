// Package agent runs a per-host sequence of steps ON THE TARGET. The same shellf
// binary is pushed over SSH and re-invoked in agent mode; it reads a Request on
// stdin and writes a Response on stdout. Steps run sequentially; a `parallel`
// step runs its branches concurrently. A step that errors halts the rest of the
// sequence (the halting rule), unless it is `?`-caught and handled by a
// following `if` (ADR-0009). An instruction resolves to an embedded stdlib def
// (executed by the language) or to a remaining Go builtin.
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
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

	return write(out, runRequest(req, ex))
}

// runRequest pre-flights the interpreters, then runs the steps. Shared by Serve
// (one-shot) and the resident agent's processJob, so both enforce the pre-flight.
func runRequest(req proto.Request, ex engine.Executor) proto.Response {
	// Package user defs, shipped as source and re-parsed here (ADR-0014). The
	// controller already validated them, so a parse failure is a protocol error.
	defs, err := userDefs(req.Defs)
	if err != nil {
		r := proto.StepResult{Label: "pre-flight", Category: "err", Tag: "badUserDefs",
			Shell: &engine.ShellResult{Stderr: err.Error()}}
		return proto.Response{Results: []proto.StepResult{r}, Halted: true}
	}
	// Pre-flight: fail before running if a required interpreter is absent, rather
	// than mid-apply at the first `shell(<interp>)` (ADR-0012).
	if missing := missingInterpreters(req.Steps, ex); len(missing) > 0 {
		r := proto.StepResult{Label: "pre-flight", Category: "err", Tag: "interpreterMissing",
			Shell: &engine.ShellResult{Stderr: "interpreter not found on target: " + strings.Join(missing, ", ")}}
		return proto.Response{Results: []proto.StepResult{r}, Halted: true}
	}
	results, halted := runSteps(req.Steps, ex, mode(req.Mode), map[string]engine.Result{}, defs)
	return proto.Response{Results: results, Halted: halted}
}

// userDefs re-parses the package's user def source into a name→def map (ADR-0014).
func userDefs(src string) (map[string]lang.Def, error) {
	if src == "" {
		return nil, nil
	}
	defs, err := lang.ParseDefs(src)
	if err != nil {
		return nil, fmt.Errorf("user defs: %w", err)
	}
	m := make(map[string]lang.Def, len(defs))
	for _, d := range defs {
		m[d.Name] = d
	}
	return m, nil
}

// missingInterpreters returns the shell interpreters used by the steps that are
// not available on this target. sh/raw map to /bin/sh and are never checked.
func missingInterpreters(steps []proto.Step, ex engine.Executor) []string {
	need := map[string]bool{}
	collectInterpreters(steps, need)
	var missing []string
	for interp := range need {
		if !ex.Shell("command -v "+interp, nil).OK() {
			missing = append(missing, interp)
		}
	}
	sort.Strings(missing)
	return missing
}

func collectInterpreters(steps []proto.Step, need map[string]bool) {
	for _, s := range steps {
		if s.Instruction == "shell" && s.Interp != "" && s.Interp != "sh" && s.Interp != "raw" {
			need[s.Interp] = true
		}
		if s.If != nil {
			if s.If.Cond != nil {
				collectInterpreters([]proto.Step{*s.If.Cond}, need)
			}
			collectInterpreters(s.If.Then, need)
			collectInterpreters(s.If.Else, need)
		}
		collectInterpreters(s.Block, need)
		collectInterpreters(s.Parallel, need)
	}
}

// runSteps executes a sequence, halting on the first err (the halting rule).
// A `?`-caught error (ADR-0009) does not halt immediately: it is deferred so the
// next step — an `if` on that var — can handle it; if nothing handles it, it
// halts like any other error. scope holds the Results captured by `name =
// <call>` steps in this block.
func runSteps(steps []proto.Step, ex engine.Executor, m engine.Mode, scope map[string]engine.Result, defs map[string]lang.Def) (results []proto.StepResult, halted bool) {
	pending := "" // a caught error (its var name) awaiting handling by the next step
	for _, step := range steps {
		if pending != "" {
			if !handlesCaught(step, pending, scope) {
				halted = true // caught error covered by nothing → halt
				break
			}
			pending = ""
		}
		sr := runStep(step, ex, m, scope, defs)
		results = append(results, sr)
		if step.Caught && step.Bind != "" && sr.Category == "err" {
			pending = step.Bind // `?` defers the halt to the handling `if`
			continue
		}
		if sr.Category == "err" {
			halted = true
			break
		}
	}
	if pending != "" {
		halted = true // a caught error was never handled
	}
	return results, halted
}

// handlesCaught reports whether step is an `if` that handles the caught error
// bound to name: it must test that var and either match it (its then-branch
// runs) or provide an `else` catch-all (ADR-0009).
func handlesCaught(step proto.Step, name string, scope map[string]engine.Result) bool {
	ib := step.If
	if ib == nil || ib.CondRef == nil || ib.CondRef.Name != name {
		return false
	}
	if len(ib.Else) > 0 {
		return true
	}
	matched := refTruth(scope[name], ib.CondRef)
	if ib.Negate {
		matched = !matched
	}
	return matched
}

// runIf evaluates the condition, then takes the branch on its truth (an inline
// instruction's Result being `ok`, or a captured result's outcome test). In
// check, a would-condition (effect not applied) makes the branch undetermined —
// the then-branch is previewed but never claimed to run.
func runIf(ib *proto.IfBlock, ex engine.Executor, m engine.Mode, scope map[string]engine.Result, defs map[string]lang.Def) proto.StepResult {
	var condResult engine.Result
	var condSub *proto.StepResult // the inline cond's own result, shown in Sub
	label := "if(" + ib.CondLabel() + ")"
	// Default predicate (inline cond, no match): the instruction's Result is `ok`.
	truthFn := func(r engine.Result) bool { return r.Category == engine.OK }

	if ib.CondRef != nil {
		ref := ib.CondRef
		r, ok := scope[ref.Name]
		if !ok {
			return proto.StepResult{Label: label, Category: "err", Tag: "undefinedResult"}
		}
		condResult = r
		truthFn = func(res engine.Result) bool { return refTruth(res, ref) }
	} else {
		res, err := runInstruction(*ib.Cond, ex, m, defs)
		if err != nil {
			return proto.StepResult{Label: label, Category: "err", Tag: "agent"}
		}
		condResult = res
		condSub = &proto.StepResult{Label: ib.Cond.Label(), Category: res.Category.String(), Tag: res.Tag, Changed: res.Changed, Shell: res.Shell}
		if ib.Match != nil { // inline outcome test: `call() == err.tag`
			match := ib.Match
			truthFn = func(res engine.Result) bool { return refTruth(res, match) }
		}
	}

	// Never-lie: an unapplied action's result is undetermined in check.
	if m == engine.Check && condResult.Category == engine.WOULD {
		preview, _ := runSteps(ib.Then, ex, m, scope, defs)
		return proto.StepResult{Label: label, Category: "undetermined", Sub: withCond(condSub, preview)}
	}

	truth := truthFn(condResult)
	if ib.Negate {
		truth = !truth
	}
	// A `?`-caught inline error that no branch covers halts (ADR-0009).
	if ib.Cond != nil && ib.Cond.Caught && condResult.Category == engine.ERR && !truth && len(ib.Else) == 0 {
		return proto.StepResult{Label: label, Category: "err", Tag: condResult.Tag, Shell: condResult.Shell, Sub: withCond(condSub, nil)}
	}
	branch := ib.Else // false/err → else (a captured result never halts)
	if truth {
		branch = ib.Then
	}
	subs, halted := runSteps(branch, ex, m, scope, defs)
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

func runStep(step proto.Step, ex engine.Executor, m engine.Mode, scope map[string]engine.Result, defs map[string]lang.Def) proto.StepResult {
	if step.If != nil {
		return runIf(step.If, ex, m, scope, defs)
	}
	if len(step.Block) > 0 { // `as <user> { … }` — run the group escalated (ADR-0011)
		subs, halted := runSteps(step.Block, ex.As(step.Become), m, scope, defs)
		cat := "ok"
		if halted {
			cat = "err"
		}
		return proto.StepResult{Label: "as " + step.Become, Category: cat, Sub: subs}
	}
	if len(step.Parallel) > 0 {
		subs := make([]proto.StepResult, len(step.Parallel))
		var wg sync.WaitGroup
		for i, b := range step.Parallel {
			wg.Add(1)
			// Each branch gets a copy of the scope: reads see prior captures,
			// writes stay local (no data race; captures inside parallel don't escape).
			go func(i int, b proto.Step) { defer wg.Done(); subs[i] = runStep(b, ex, m, copyScope(scope), defs) }(i, b)
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

	res, err := runInstruction(step, ex.As(step.Become).Using(step.Interp), m, defs) // step-level `as <user>` + `shell(<interp>)`
	if err != nil {
		return proto.StepResult{Label: step.Label(), Category: "err", Tag: "agent"}
	}
	if step.Bind != "" {
		scope[step.Bind] = res
	}
	return proto.StepResult{Label: step.Label(), Category: res.Category.String(), Tag: res.Tag, Changed: res.Changed, Shell: res.Shell, Fields: res.Fields}
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
func runInstruction(step proto.Step, ex engine.Executor, m engine.Mode, defs map[string]lang.Def) (engine.Result, error) {
	// A package user def resolves first, so it can add new instructions and
	// (with `override def`) replace a stdlib one (ADR-0014).
	if def, ok := defs[step.Instruction]; ok {
		return lang.EvalDef(def, step.Args, ex, m)
	}
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
		return engine.Shell{Cmd: step.Args["cmd"], Unless: step.Args["unless"], Env: engine.Env(step.Env)}, nil
	default:
		return nil, fmt.Errorf("unknown instruction: %q", step.Instruction)
	}
}

func mode(s string) engine.Mode {
	switch s {
	case "check":
		return engine.Check
	case "status":
		return engine.Status
	default:
		return engine.Apply
	}
}

func write(out io.Writer, r proto.Response) error {
	return json.NewEncoder(out).Encode(r)
}
