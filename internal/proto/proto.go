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
func ResolveRefs(steps []Step, env map[string]string, interp string) ([]Step, error) {
	out := make([]Step, len(steps))
	for i, s := range steps {
		if s.If != nil {
			var cond *Step
			if s.If.Cond != nil {
				resolved, err := ResolveRefs([]Step{*s.If.Cond}, env, interp)
				if err != nil {
					return nil, err
				}
				cond = &resolved[0]
			}
			then, err := ResolveRefs(s.If.Then, env, interp)
			if err != nil {
				return nil, err
			}
			els, err := ResolveRefs(s.If.Else, env, interp)
			if err != nil {
				return nil, err
			}
			out[i] = Step{If: &IfBlock{Cond: cond, Match: s.If.Match, CondRef: s.If.CondRef, Negate: s.If.Negate, Then: then, Else: els}}
			continue
		}
		if len(s.Block) > 0 {
			sub, err := ResolveRefs(s.Block, env, interp)
			if err != nil {
				return nil, err
			}
			out[i] = Step{Block: sub, Become: s.Become}
			continue
		}
		if len(s.Parallel) > 0 {
			sub, err := ResolveRefs(s.Parallel, env, interp)
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
		step := Step{Instruction: s.Instruction, Args: args, Bind: s.Bind, Caught: s.Caught, Become: s.Become, Interp: s.Interp, With: s.With}
		if s.Instruction == "shell" { // a plan-level shell sees the per-host env via $name (#106)
			step.Env = env
			if len(s.With) > 0 { // a `with` binding overrides the host env for this call (ADR-0022)
				merged := make(map[string]string, len(env)+len(s.With))
				for k, v := range env { // copy so the shared host env is never mutated
					merged[k] = v
				}
				for k, v := range s.With {
					merged[k] = v
				}
				step.Env = merged
			}
			if step.Interp == "" { // no block annotation → the host's interpreter (ADR-0012)
				step.Interp = interp
			}
		}
		out[i] = step
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
	Bind        string            `json:"bind,omitempty"`   // capture this step's Result under this name
	Caught      bool              `json:"caught,omitempty"` // `?` — an err here is handled, not an automatic halt (ADR-0009)
	Become      string            `json:"become,omitempty"` // escalate this step's shells to this user (ADR-0011)
	Interp      string            `json:"interp,omitempty"` // shell interpreter for a `shell(<interp>)` step (ADR-0012)
	Env         map[string]string `json:"env,omitempty"`    // per-host env for a plan-level `shell` step (#106)
	With        map[string]string `json:"with,omitempty"`   // per-call variable override (ADR-0022)
	Block       []Step            `json:"block,omitempty"`  // an `as <user> { … }` sequential group (ADR-0011)
	Parallel    []Step            `json:"parallel,omitempty"`
	If          *IfBlock          `json:"if,omitempty"`
}

// IfBlock is a conditional. The condition is either Cond (an instruction run
// inline) or CondRef (an outcome test on a previously captured Result); exactly
// one is set. With Cond, an optional Match tests its Result against an outcome
// pattern (`if call() == err.tag`); nil Match means branch on `ok`.
type IfBlock struct {
	Cond    *Step      `json:"cond,omitempty"`
	Match   *ResultRef `json:"match,omitempty"` // outcome pattern for Cond's result (nil = `ok`); ADR-0009
	CondRef *ResultRef `json:"condRef,omitempty"`
	Negate  bool       `json:"negate,omitempty"` // `if !cond` / `!=` — flip the branch truth
	Then    []Step     `json:"then"`
	Else    []Step     `json:"else,omitempty"`
}

// CondLabel renders the condition for reports.
func (ib *IfBlock) CondLabel() string {
	if ib.CondRef != nil {
		return ib.CondRef.Label()
	}
	l := ib.Cond.Label()
	if ib.Match != nil {
		l += " == " + ib.Match.Pattern()
	}
	return l
}

// ResultRef tests a captured Result: either the `changed` flag (Changed) or an
// outcome pattern — a Category with an optional Tag ("" tag = category
// wildcard), e.g. `s == ok`, `s == err.dbLocked`. See ADR-0008.
type ResultRef struct {
	Name     string `json:"name"`
	Category string `json:"category,omitempty"` // ok | err | would (outcome pattern)
	Tag      string `json:"tag,omitempty"`      // optional; "" = category wildcard
	Changed  bool   `json:"changed,omitempty"`  // test `.changed` instead of the pattern
}

// Pattern renders the outcome pattern: `err` or `err.dbLocked`.
func (r *ResultRef) Pattern() string {
	if r.Tag != "" {
		return r.Category + "." + r.Tag
	}
	return r.Category
}

// Label renders the ref for reports: `s.changed`, `s == ok`, `s == err.dbLocked`.
func (r *ResultRef) Label() string {
	if r.Changed {
		return r.Name + ".changed"
	}
	return r.Name + " == " + r.Pattern()
}

// Label is a compact human-readable form for reports.
func (s Step) Label() string {
	if s.If != nil {
		return "if(" + s.If.CondLabel() + ")"
	}
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
	Mode string `json:"mode"` // "apply" | "check" | "status"
	// Defs maps a resolved instruction name (bare for the local package, qualified
	// `alias.def` for imports) to that def's source, re-parsed on the agent and
	// registered under the key (ADR-0014/0015).
	Defs  map[string]string `json:"defs,omitempty"`
	Steps []Step            `json:"steps"`
}

type StepResult struct {
	Label    string              `json:"label"`
	Category string              `json:"category"`
	Tag      string              `json:"tag,omitempty"`
	Changed  bool                `json:"changed,omitempty"`
	Shell    *engine.ShellResult `json:"shell,omitempty"`
	Fields   []engine.FieldDiff  `json:"fields,omitempty"` // status mode: observed vs desired (ADR-0013)
	Sub      []StepResult        `json:"sub,omitempty"`
}

type Response struct {
	Results []StepResult `json:"results"`
	Halted  bool         `json:"halted,omitempty"`
	Error   string       `json:"error,omitempty"`
}
