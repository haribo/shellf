package lang

import (
	"fmt"
	"strings"

	"shellf/internal/inventory"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// instructionArgs maps an instruction (bare or qualified) to its positional
// argument names, so the parser can turn `apt-install("nginx")` into
// Step{Instruction:"apt-install", Args:{"pkg":"nginx"}}. A known smell: this
// duplicates each def's params; resolving positional args at eval time (where
// the def is known) is the cleaner future.
var instructionArgs = map[string][]string{
	"apt-install":     {"pkg"},
	"file-copy":       {"src", "dst"},
	"service":         {"name", "running", "enabled"},
	"file-download":   {"url", "dst", "sha256"},
	"archive-extract": {"src", "dst"},
	"git-clone":       {"url", "dst"},
	"dir-ensure":      {"path"},
	"dir-exists":      {"path"},
	"dir-owner":       {"path", "owner"},
	"file-exists":     {"path"},
	"user-group":      {"user", "group"},
	"file-write":      {"path", "content"},
	"file-line":       {"path", "line"},
	"file-delete":     {"path"},
	"docker.install":    {},
	"docker.network":    {"name"},
	"docker.compose-up": {"dir"},
	"ufw.open":          {"port", "proto"},
	"ufw.enable":        {},
}

// ParseInventory parses an inventory file into an Inventory.
func ParseInventory(src string) (inv inventory.Inventory, err error) {
	defer catch(&err)
	p := newParser(src)
	inv = p.inventory()
	return
}

// ParsePlan parses a plan file into an orchestration Plan.
func ParsePlan(src string) (orchestrator.Plan, error) {
	return ParsePlanWithVars(src, map[string]string{}, nil)
}

// ParsePlanWithVars parses a plan with pre-loaded global variables. `baseVars`
// (from a --vars file) is the lower-precedence table; `setVars` (from --set) is
// the highest. Plan-level bindings are appended to `baseVars` (which is mutated
// in place), so the caller can pass the enriched table to the orchestrator for
// per-host resolution of the Steps' Refs. Interpolation and binding values are
// resolved here; bare-identifier arguments are left as Refs.
func ParsePlanWithVars(src string, baseVars, setVars map[string]string) (plan orchestrator.Plan, err error) {
	defer catch(&err)
	p := newParser(src)
	if baseVars != nil {
		p.baseVars = baseVars
	}
	p.setVars = setVars
	plan = p.plan()
	return
}

// ParseVars parses a file of `name = value` bindings (a --vars file) into a
// map. Later bindings may reference earlier ones (resolved in order).
func ParseVars(src string) (vars map[string]string, err error) {
	defer catch(&err)
	p := newParser(src)
	for p.tok.kind != tEOF {
		name := p.expect(tIdent, "variable name").val
		p.expect(tEq, "=")
		p.baseVars[name] = p.arg()
	}
	return p.baseVars, nil
}

// --- parser ---

type parseErr struct{ err error }

func catch(dst *error) {
	if r := recover(); r != nil {
		if pe, ok := r.(parseErr); ok {
			*dst = pe.err
			return
		}
		panic(r)
	}
}

type parser struct {
	lex      *lexer
	tok      token
	baseVars map[string]string // --vars + plan bindings (lower precedence)
	setVars  map[string]string // --set overrides (highest precedence)
}

func newParser(src string) *parser {
	p := &parser{lex: newLexer(src), baseVars: map[string]string{}}
	p.adv()
	return p
}

// lookup resolves a variable name at parse time, --set winning over base.
func (p *parser) lookup(name string) (string, bool) {
	if v, ok := p.setVars[name]; ok {
		return v, true
	}
	v, ok := p.baseVars[name]
	return v, ok
}

func (p *parser) adv() {
	t, err := p.lex.next()
	if err != nil {
		panic(parseErr{err})
	}
	p.tok = t
}

func (p *parser) fail(format string, a ...any) {
	panic(parseErr{fmt.Errorf("%d:%d: %s", p.tok.line, p.tok.col, fmt.Sprintf(format, a...))})
}

func (p *parser) expect(k tokKind, what string) token {
	if p.tok.kind != k {
		p.fail("expected %s, got %q", what, p.tok.val)
	}
	t := p.tok
	p.adv()
	return t
}

// --- inventory grammar ---

func (p *parser) inventory() inventory.Inventory {
	inv := inventory.Inventory{
		Hosts:  map[string]inventory.Host{},
		Groups: map[string][]string{},
	}
	for p.tok.kind != tEOF {
		switch kw := p.expect(tIdent, "defaults/host/group").val; kw {
		case "defaults":
			p.expect(tEq, "=")
			inv.Defaults = p.host()
		case "host":
			name := p.expect(tIdent, "host name").val
			p.expect(tEq, "=")
			inv.Hosts[name] = p.host()
		case "group":
			name := p.expect(tIdent, "group name").val
			p.expect(tEq, "=")
			inv.Groups[name] = p.identList()
		default:
			p.fail("unknown inventory keyword %q", kw)
		}
	}
	return inv
}

func (p *parser) host() inventory.Host {
	var h inventory.Host
	for k, v := range p.record() {
		switch k {
		case "address":
			h.Address = v
		case "user":
			h.User = v
		case "port":
			h.Port = v
		case "key":
			h.Key = v
		default:
			// Any other field is a free-form per-host variable.
			if h.Vars == nil {
				h.Vars = map[string]string{}
			}
			h.Vars[k] = v
		}
	}
	return h
}

func (p *parser) record() map[string]string {
	p.expect(tLBrace, "{")
	m := map[string]string{}
	for p.tok.kind != tRBrace {
		key := p.expect(tIdent, "field name").val
		p.expect(tColon, ":")
		m[key] = p.expect(tString, "string value").val
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRBrace, "}")
	return m
}

func (p *parser) identList() []string {
	p.expect(tLBrack, "[")
	var out []string
	for p.tok.kind != tRBrack {
		out = append(out, p.expect(tIdent, "host name").val)
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRBrack, "]")
	return out
}

// --- plan grammar ---

func (p *parser) plan() orchestrator.Plan {
	var plan orchestrator.Plan
	for p.tok.kind != tEOF {
		kw := p.expect(tIdent, "'on' or a binding").val
		if kw != "on" {
			// Top-level binding: `name = value`. Appended to baseVars; --set still
			// wins at resolution (via lookup order), so no pinning is needed here.
			p.expect(tEq, "=")
			p.baseVars[kw] = p.arg()
			continue
		}
		target := p.expect(tIdent, "group or host").val
		plan = append(plan, orchestrator.Block{Target: target, Steps: p.block()})
	}
	return plan
}

func (p *parser) block() []proto.Step {
	p.expect(tLBrace, "{")
	var steps []proto.Step
	for p.tok.kind != tRBrace {
		steps = append(steps, p.step())
	}
	p.expect(tRBrace, "}")
	return steps
}

func (p *parser) step() proto.Step {
	// `shell` and `if` are special forms, handled before the call() path.
	if p.tok.kind == tIdent && p.tok.val == "shell" {
		return p.shellStep()
	}
	if p.tok.kind == tIdent && p.tok.val == "if" {
		return p.ifStep()
	}
	name := p.expect(tIdent, "instruction or 'parallel'").val
	if p.tok.kind == tEq { // capture: name = <call>
		p.adv()
		rhs := p.step()
		if rhs.If != nil || len(rhs.Parallel) > 0 {
			p.fail("cannot capture an if/parallel block into a variable")
		}
		rhs.Bind = name
		return rhs
	}
	if p.tok.kind == tDot { // qualified call: module.instruction
		p.adv()
		name = name + "." + p.expect(tIdent, "instruction name").val
		return p.call(name)
	}
	if name == "parallel" {
		return proto.Step{Parallel: p.block()}
	}
	return p.call(name)
}

// ifStep parses `if <call> { then } [else { else }]`. The condition is a step
// (an instruction call, or a shell); the branch is taken on its Result `.ok`.
func (p *parser) ifStep() proto.Step {
	p.adv() // consume 'if'
	ib := &proto.IfBlock{}
	if cond, ref := p.condition(); ref != nil {
		ib.CondRef = ref
	} else {
		ib.Cond = cond
	}
	ib.Then = p.block()
	if p.tok.kind == tIdent && p.tok.val == "else" {
		p.adv()
		ib.Else = p.block()
	}
	return proto.Step{If: ib}
}

// condition parses an if condition: an instruction (call or shell) run inline,
// or a reference to a captured Result's field (`s1.ok`, `s1.changed`, or bare
// `s1` = `s1.ok`).
func (p *parser) condition() (*proto.Step, *proto.ResultRef) {
	if p.tok.kind == tIdent && p.tok.val == "shell" {
		s := p.shellStep()
		return &s, nil
	}
	name := p.expect(tIdent, "condition").val
	if p.tok.kind == tDot {
		p.adv()
		second := p.expect(tIdent, "field or instruction").val
		if p.tok.kind == tLParen { // qualified call: name.second(...)
			s := p.call(name + "." + second)
			return &s, nil
		}
		if second != "ok" && second != "changed" {
			p.fail("unknown result field %q (want ok or changed)", second)
		}
		return nil, &proto.ResultRef{Name: name, Field: second}
	}
	if p.tok.kind == tLParen { // call: name(...)
		s := p.call(name)
		return &s, nil
	}
	return nil, &proto.ResultRef{Name: name, Field: "ok"} // `if s1 {` → s1.ok
}

// shellStep parses `shell <line>` or `shell { … }` with an optional
// `unless { … }` guard. When p.tok is the `shell`/`unless` keyword, the lexer
// sits right after it, so raw capture reads from there.
func (p *parser) shellStep() proto.Step {
	body, err := p.lex.rawShellBody()
	if err != nil {
		panic(parseErr{err})
	}
	p.adv() // resync to the token after the shell body

	args := map[string]string{"cmd": body}
	if p.tok.kind == tIdent && p.tok.val == "unless" {
		guard, err := p.lex.rawBracesRequired()
		if err != nil {
			panic(parseErr{err})
		}
		p.adv()
		args["unless"] = guard
	}
	return proto.Step{Instruction: "shell", Args: args}
}

func (p *parser) call(name string) proto.Step {
	argNames, ok := instructionArgs[name]
	if !ok {
		p.fail("unknown instruction %q", name)
	}
	p.expect(tLParen, "(")
	type argv struct{ val, ref string }
	var vals []argv
	for p.tok.kind != tRParen {
		v, r := p.callArg()
		vals = append(vals, argv{v, r})
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRParen, ")")

	if len(vals) != len(argNames) {
		p.fail("%s expects %d argument(s), got %d", name, len(argNames), len(vals))
	}
	args := map[string]string{}
	var refs map[string]string
	for i, n := range argNames {
		if vals[i].ref != "" {
			if refs == nil {
				refs = map[string]string{}
			}
			refs[n] = vals[i].ref
		} else {
			args[n] = vals[i].val
		}
	}
	return proto.Step{Instruction: name, Args: args, Refs: refs}
}

// arg resolves a binding's value (plan top-level binding or --vars file entry)
// at parse time: a string (interpolated), a raw triple-quoted string, a bool,
// or a bare identifier resolved against the known vars. A binding cannot
// reference a per-host var (unknown at parse) — an unknown name errors here.
func (p *parser) arg() string {
	switch {
	case p.tok.kind == tString:
		v := p.interpolate(p.tok.val)
		p.adv()
		return v
	case p.tok.kind == tRawString:
		v := p.tok.val
		p.adv()
		return v
	case p.tok.kind == tIdent && (p.tok.val == "true" || p.tok.val == "false"):
		v := p.tok.val
		p.adv()
		return v
	case p.tok.kind == tIdent:
		name := p.tok.val
		p.adv()
		v, ok := p.lookup(name)
		if !ok {
			p.fail("undefined variable %q", name)
		}
		return v
	default:
		p.fail("expected a string, bool, or variable argument, got %q", p.tok.val)
		return "" // unreachable
	}
}

// callArg parses one instruction argument. Unlike arg, a bare identifier is
// returned as a ref name (empty value), NOT resolved: bare-identifier arguments
// are resolved per host at orchestration time (ADR-0003 §5). Strings are still
// interpolated at parse time (interpolation is global-only).
func (p *parser) callArg() (value, ref string) {
	switch {
	case p.tok.kind == tString:
		v := p.interpolate(p.tok.val)
		p.adv()
		return v, ""
	case p.tok.kind == tRawString:
		v := p.tok.val
		p.adv()
		return v, ""
	case p.tok.kind == tIdent && (p.tok.val == "true" || p.tok.val == "false"):
		v := p.tok.val
		p.adv()
		return v, ""
	case p.tok.kind == tIdent:
		name := p.tok.val
		p.adv()
		return "", name
	default:
		p.fail("expected a string, bool, or variable argument, got %q", p.tok.val)
		return "", "" // unreachable
	}
}

// interpolate replaces every `${name}` in a simple string with the bound value,
// failing on an unterminated `${` or an undefined name. Raw triple-quoted
// strings never reach here, so their `${VAR}` (shell/compose) stay verbatim.
func (p *parser) interpolate(s string) string {
	var out strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		rest := s[i+2:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			p.fail("unterminated ${...} interpolation")
		}
		name := rest[:end]
		v, ok := p.lookup(name)
		if !ok {
			p.fail("undefined variable %q in interpolation", name)
		}
		out.WriteString(v)
		s = rest[end+1:]
	}
}
