package lang

import (
	"fmt"

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
	"docker.install":  {},
	"docker.network":  {"name"},
}

// ParseInventory parses an inventory file into an Inventory.
func ParseInventory(src string) (inv inventory.Inventory, err error) {
	defer catch(&err)
	p := newParser(src)
	inv = p.inventory()
	return
}

// ParsePlan parses a plan file into an orchestration Plan.
func ParsePlan(src string) (plan orchestrator.Plan, err error) {
	defer catch(&err)
	p := newParser(src)
	plan = p.plan()
	return
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
	lex *lexer
	tok token
}

func newParser(src string) *parser {
	p := &parser{lex: newLexer(src)}
	p.adv()
	return p
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
			p.fail("unknown host field %q", k)
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
		if kw := p.expect(tIdent, "'on'").val; kw != "on" {
			p.fail("expected 'on', got %q", kw)
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
	// `shell` is a special form (raw capture), handled before the call() path.
	if p.tok.kind == tIdent && p.tok.val == "shell" {
		return p.shellStep()
	}
	name := p.expect(tIdent, "instruction or 'parallel'").val
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
	var vals []string
	for p.tok.kind != tRParen {
		vals = append(vals, p.arg())
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
	for i, n := range argNames {
		args[n] = vals[i]
	}
	return proto.Step{Instruction: name, Args: args}
}

// arg accepts a quoted string or a bare bool literal (true/false), both kept
// as their string form.
func (p *parser) arg() string {
	switch {
	case p.tok.kind == tString:
		v := p.tok.val
		p.adv()
		return v
	case p.tok.kind == tIdent && (p.tok.val == "true" || p.tok.val == "false"):
		v := p.tok.val
		p.adv()
		return v
	default:
		p.fail("expected a string or bool argument, got %q", p.tok.val)
		return "" // unreachable
	}
}
