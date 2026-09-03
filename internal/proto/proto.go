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

// InventoryPrefix marks an interpolation naming a field of the host a step will run on
// (ADR-0052). It lives here rather than in `lang` because the orchestrator needs it too,
// and ADR-0024 keeps `lang` out of `orchestrator` and `proto`.
const InventoryPrefix = "inventory."

// expandInventory substitutes every `${inventory.<field>}` in s from env, which the
// orchestrator has already populated with this host's fields. An unknown field is an error
// naming it — the caller adds the host.
func expandInventory(s string, env map[string]string) (string, error) {
	var out strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			out.WriteString(s)
			return out.String(), nil
		}
		out.WriteString(s[:i])
		rest := s[i+2:]
		end := strings.IndexByte(rest, '}')
		if end < 0 { // the parser rejects this; belt and braces at the boundary
			return "", fmt.Errorf("unterminated ${...} in %q", s)
		}
		name := rest[:end]
		v, ok := env[name]
		if !ok {
			return "", missingInventory(name, env)
		}
		out.WriteString(v)
		s = rest[end+1:]
	}
}

// missingInventory names what is actually wrong with an unresolved `${inventory.…}`.
//
// A cross-host read has two ways to be misspelled and they are different mistakes: the host
// or the field (ADR-0054 §4). "undefined variable inventory.bd.address" makes the reader
// check both; naming which one moved is the whole value of the message.
//
// The host is judged by whether *any* field of it is in the table, not by asking the
// inventory: this package must not grow a dependency on it to explain a lookup it was
// handed (the same constraint ADR-0052 met).
func missingInventory(name string, env map[string]string) error {
	field, ok := strings.CutPrefix(name, InventoryPrefix)
	if !ok {
		return fmt.Errorf("undefined variable %q", name)
	}
	host, f, cross := strings.Cut(field, ".")
	if !cross {
		return fmt.Errorf("this host declares no field %q, read as ${%s}", field, name)
	}
	prefix := InventoryPrefix + host + "."
	for k := range env {
		if strings.HasPrefix(k, prefix) {
			return fmt.Errorf("host %q declares no field %q, read as ${%s}", host, f, name)
		}
	}
	return fmt.Errorf("undefined host %q, read as ${%s}", host, name)
}

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
			out[i] = Step{If: &IfBlock{Cond: cond, Match: s.If.Match, CondRef: s.If.CondRef, Negate: s.If.Negate, Then: then, Else: els}, Control: s.Control}
			continue
		}
		if len(s.Block) > 0 {
			sub, err := ResolveRefs(s.Block, env, interp)
			if err != nil {
				return nil, err
			}
			out[i] = Step{Block: sub, Become: s.Become, Control: s.Control}
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
		// `${inventory.<field>}` left for this pass (ADR-0052). Expanded here rather than
		// in `lang`: this package must not depend on the language to substitute a name it
		// has already been handed, and what remains in a Template is only ever the deferred
		// form — every global was substituted while the plan was read.
		for argName, tmpl := range s.Templates {
			v, err := expandInventory(tmpl, env)
			if err != nil {
				return nil, err
			}
			args[argName] = v
		}
		// Control must survive: it says which arguments the plan marked `%"…"`, and
		// dropping it makes the agent read those paths on the target instead of the
		// control host — silently, since a path is a path (#334).
		step := Step{Instruction: s.Instruction, Args: args, Bind: s.Bind, Caught: s.Caught,
			Become: s.Become, Interp: s.Interp, With: s.With, Control: s.Control}
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
//
// Templates holds arguments carrying a `${inventory.<field>}` the control host could not
// resolve while reading the plan, because the host was not known then (ADR-0052). Same
// timing as Refs, and resolved in the same pass; the difference is that a Ref *is* a name
// while a Template is a string with names inside it.
type Step struct {
	Instruction string            `json:"instruction,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
	Refs        map[string]string `json:"refs,omitempty"`
	Templates   map[string]string `json:"templates,omitempty"`
	Bind        string            `json:"bind,omitempty"`   // capture this step's Result under this name
	Caught      bool              `json:"caught,omitempty"` // `?` — an err here is handled, not an automatic halt (ADR-0009)
	Become      string            `json:"become,omitempty"` // escalate this step's shells to this user (ADR-0011)
	Interp      string            `json:"interp,omitempty"` // shell interpreter for a `shell(<interp>)` step (ADR-0012)
	Env         map[string]string `json:"env,omitempty"`    // per-host env for a plan-level `shell` step (#106)
	With        map[string]string `json:"with,omitempty"`   // per-call variable override (ADR-0022)
	// Control lists the argument names written `%"path"`: paths on the control host
	// (ADR-0034). The value travels as an ordinary string; this records which ones the
	// control host must be prepared to serve, so the allow-list is known before the
	// plan is sent (ADR-0031 §3).
	Control  []string `json:"control,omitempty"`
	Block    []Step   `json:"block,omitempty"` // an `as <user> { … }` sequential group (ADR-0011)
	Parallel []Step   `json:"parallel,omitempty"`
	If       *IfBlock `json:"if,omitempty"`
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
	// Label args as `name=value`: Args is a map so positional order is gone, and
	// bare values in key-sorted order read as swapped (#258). name=value is
	// unambiguous regardless of order. Keys are sorted for a stable label.
	keys := make([]string, 0, len(s.Args))
	for k := range s.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + firstLine(s.Args[k]) // truncate long/multi-line values (e.g. file-write content)
	}
	return s.Instruction + "(" + strings.Join(parts, ", ") + ")"
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
	// Verbose asks the agent to report every shell each step ran, not only the one a
	// failing def hands back. Gated on the request rather than sent always: a job with
	// many steps would otherwise carry every command it ran, on every host, for nobody
	// (#470).
	Verbose bool `json:"verbose,omitempty"`
}

type StepResult struct {
	Label    string `json:"label"`
	Category string `json:"category"`
	Tag      string `json:"tag,omitempty"`
	Changed  bool   `json:"changed,omitempty"`
	// Caught marks a step whose `?` handed its error to the plan (ADR-0009). It rides the
	// result because the *report* has to tell the two apart: an error the plan handled is
	// not a failed run, and counting it as one made `shellf run … && …` unusable for any
	// plan using `?` (#356).
	Caught  bool                 `json:"caught,omitempty"`
	Shell   *engine.ShellResult  `json:"shell,omitempty"`
	Fields  []engine.FieldDiff   `json:"fields,omitempty"`  // status mode: observed vs desired (ADR-0013)
	Preview string               `json:"preview,omitempty"` // check mode: what an action would do (ADR-0029)
	Ran     []engine.ShellResult `json:"ran,omitempty"`     // every shell this step ran; `-v` only (#470)
	Sub     []StepResult         `json:"sub,omitempty"`
}

type Response struct {
	Results []StepResult `json:"results"`
	Halted  bool         `json:"halted,omitempty"`
	Error   string       `json:"error,omitempty"`
}
