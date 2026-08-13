package lang

import (
	"sort"
	"strings"
)

// Parser for `def` declarations. Reuses the lexer and the shared parser helpers
// (adv/expect/fail/catch) from parser.go.

var phaseNames = map[string]bool{
	"check": true, "observe": true, "preview": true, "apply": true,
}

// removedPhases turns a name that used to exist into an actionable error rather than
// "unknown" (ADR-0035). They are not accepted — the plan still fails.
var removedPhases = map[string]string{
	"check": "folded into `check` (ADR-0035); rename the phase",
	"post":      "removed (ADR-0035); it was never used and had no settled meaning",
}
var categories = map[string]bool{"ok": true, "err": true, "would": true}

// ParseDefs parses a file of `def` declarations.
func ParseDefs(src string) (defs []Def, err error) {
	defer catch(&err)
	p := newParser(src)
	for p.tok.kind != tEOF {
		defs = append(defs, p.def())
	}
	return
}

func (p *parser) def() Def {
	override := false
	kw := p.expect(tIdent, "'def' or 'override'").val
	if kw == "override" { // `override def …` deliberately shadows a stdlib def (ADR-0014)
		override = true
		kw = p.expect(tIdent, "'def' after 'override'").val
	}
	if kw != "def" {
		p.fail("expected 'def', got %q", kw)
	}
	d := Def{Name: p.expect(tIdent, "def name").val, Params: p.params(), Override: override}

	// Optional `as <user>` (ADR-0011) and `using <interp>` (ADR-0012), either order.
	for p.tok.kind == tIdent && (p.tok.val == "as" || p.tok.val == "using") {
		kw := p.tok.val
		p.adv()
		if kw == "as" {
			d.Become = p.expect(tIdent, "user after 'as'").val
		} else {
			d.Interp = p.expect(tIdent, "interpreter after 'using'").val
			if !validInterp(d.Interp) {
				p.fail("unknown interpreter %q (want sh/bash/dash/nu/raw)", d.Interp)
			}
		}
	}
	p.expect(tLBrace, "{")
	for p.tok.kind != tRBrace {
		switch {
		case p.tok.kind == tIdent && phaseNames[p.tok.val]:
			d.Phases = append(d.Phases, p.phase())
		case p.tok.kind == tIdent && removedPhases[p.tok.val] != "":
			p.fail("phase %q: %s", p.tok.val, removedPhases[p.tok.val])
		case p.tok.kind == tIdent && p.tok.val == "return":
			p.fail("`return` must be inside a phase, not at def top level (ADR-0007)")
		default:
			p.fail("expected a phase, got %q", p.tok.val)
		}
	}
	p.expect(tRBrace, "}")
	d.Return = nominalReturn(d)
	return d
}

// nominalReturn is the outcome a def yields when apply runs to completion: the
// last top-level statement of `apply`, if it is a `return`. It drives
// `would.<tag>` in check mode, where apply is never executed (ADR-0007).
func nominalReturn(d Def) *Outcome {
	for _, ph := range d.Phases {
		if ph.Name != "apply" {
			continue
		}
		if n := len(ph.Stmts); n > 0 {
			if rs, ok := ph.Stmts[n-1].(ReturnStmt); ok {
				o := rs.Outcome
				return &o
			}
		}
	}
	return nil
}

func (p *parser) params() []Param {
	p.expect(tLParen, "(")
	var out []Param
	for p.tok.kind != tRParen {
		name := p.expect(tIdent, "parameter name").val
		p.expect(tColon, ":")
		par := Param{Name: name, Type: p.expect(tIdent, "type").val}
		if p.tok.kind == tEq { // optional default: `ensure: str = "present"` (ADR-0013)
			p.adv()
			par.Default = p.primary() // a literal (string/bool/number)
		}
		out = append(out, par)
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRParen, ")")
	return out
}

// phase parses `phase { <stmts> }` — always a block (ADR-0006).
func (p *parser) phase() Phase {
	ph := Phase{Name: p.expect(tIdent, "phase name").val}
	p.expect(tLBrace, "{")
	for p.tok.kind != tRBrace {
		ph.Stmts = append(ph.Stmts, p.stmt())
	}
	p.expect(tRBrace, "}")
	return ph
}

// stmt parses one statement: `if <cond> { … }`, `return <outcome>`, a binding
// `name = expr`, or a bare effect expression (a `shell { … }`). See ADR-0006.
func (p *parser) stmt() Stmt {
	if p.tok.kind == tIdent {
		switch p.tok.val {
		case "if":
			p.adv()
			cond := p.expr()
			p.expect(tLBrace, "{")
			var body []Stmt
			for p.tok.kind != tRBrace {
				body = append(body, p.stmt())
			}
			p.expect(tRBrace, "}")
			return IfStmt{Cond: cond, Body: body}
		case "return":
			p.adv()
			// `return state(...)` (an observe record) vs `return <outcome>`; `state`
			// is not an outcome category, so the two never collide (ADR-0013).
			if p.tok.kind == tIdent && p.tok.val == "state" {
				return p.stateReturn()
			}
			return ReturnStmt{Outcome: p.outcome()}
		}
	}
	// A binding `name = expr`, or a bare effect expression. Parse the expression
	// first; a trailing `=` marks a binding (no lookahead needed).
	lhs := p.expr()
	if p.tok.kind == tEq {
		id, ok := lhs.(Ident)
		if !ok {
			p.fail("cannot bind to a non-identifier")
		}
		p.adv()
		return LetStmt{Name: id.Name, Value: p.expr()}
	}
	return EffectStmt{Expr: lhs}
}

// stateReturn parses `state(field: expr, …)` — the observe record (ADR-0013).
func (p *parser) stateReturn() StateReturnStmt {
	p.expect(tIdent, "state") // the `state` keyword
	p.expect(tLParen, "(")
	var fields []StateField
	for p.tok.kind != tRParen {
		name := p.expect(tIdent, "field name").val
		p.expect(tColon, ":")
		fields = append(fields, StateField{Name: name, Value: p.expr()})
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRParen, ")")
	return StateReturnStmt{Fields: fields}
}

func (p *parser) outcome() Outcome {
	o := Outcome{Category: p.expect(tIdent, "outcome category").val}
	if !categories[o.Category] {
		p.fail("unknown outcome category %q (want ok/err/would)", o.Category)
	}
	if p.tok.kind == tDot {
		p.adv()
		o.Tag = p.expect(tIdent, "outcome tag").val
		if p.tok.kind == tLParen { // payload: err.runtime(r)
			p.adv()
			o.Payload = p.expr()
			p.expect(tRParen, ")")
		}
	}
	return o
}

// --- expressions: comparison over postfix over primary ---

func (p *parser) expr() Expr {
	left := p.unary()
	if p.tok.kind == tEqEq || p.tok.kind == tNotEq {
		op := p.tok.val
		p.adv()
		return Binary{Op: op, L: left, R: p.unary()}
	}
	return left
}

// unary parses an optional leading `!` (negate truthiness), then a postfix
// expression. Lets `if !r` work inside a def (ADR-0010).
func (p *parser) unary() Expr {
	if p.tok.kind == tBang {
		p.adv()
		return Unary{Op: "!", X: p.unary()}
	}
	return p.postfix()
}

// peekQualifiedCall reports whether the current `.` starts a qualified instruction
// name rather than a field access: it does when an identifier follows and then a `(`.
func (p *parser) peekQualifiedCall() bool {
	save := *p.lex
	tok := p.tok
	defer func() { *p.lex, p.tok = save, tok }()
	p.adv() // past '.'
	if p.tok.kind != tIdent {
		return false
	}
	p.adv()
	return p.tok.kind == tLParen
}

// controlExpr parses what follows a `%`: either a path literal (`%"conf.j2"`) or a
// call to a control-host primitive (`%file.read(…)`). Anything else is refused here,
// naming what was found — that refusal is the rule keeping shell off the operator's
// machine, so it must not be a silent fallback to an ordinary call.
func (p *parser) controlExpr() Expr {
	p.adv() // consume '%'
	if p.tok.kind == tString || p.tok.kind == tRawString {
		v := p.tok.val
		p.adv()
		return ControlPath{Value: v}
	}
	if p.tok.kind != tIdent {
		p.fail("%% must be followed by a control-host path or primitive, got %q", p.tok.val)
	}
	name := p.tok.val
	p.adv()
	if p.tok.kind == tDot {
		p.adv()
		name += "." + p.expect(tIdent, "primitive name after '.'").val
	}
	if !ControlPrimitives[name] {
		p.fail("%%%s is not a control-host primitive (ADR-0034); only %s may carry %%", name, controlPrimitiveList())
	}
	if p.tok.kind != tLParen {
		p.fail("%%%s must be called", name)
	}
	return Call{Name: name, Args: p.callArgs(), Control: true}
}

func controlPrimitiveList() string {
	names := make([]string, 0, len(ControlPrimitives))
	for n := range ControlPrimitives {
		names = append(names, "%"+n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (p *parser) postfix() Expr {
	e := p.primary()
	for p.tok.kind == tDot {
		p.adv()
		e = Field{Recv: e, Name: p.expect(tIdent, "field name").val}
	}
	return e
}

// ControlPrimitives is the closed set a `%` may prefix (ADR-0034 §2). It is closed on
// purpose: if `%` could prefix a def, a def could run shell, and shell would run on the
// machine holding every SSH key and every secret. shellf runs shell on targets.
var ControlPrimitives = map[string]bool{
	"file.read":   true,
	"file.render": true,
	"dir.list":    true,
}

func (p *parser) primary() Expr {
	switch p.tok.kind {
	case tPercent:
		return p.controlExpr()
	case tString, tRawString:
		v := p.tok.val
		p.adv()
		return StrLit{Value: v}
	case tNumber:
		v := p.tok.val
		p.adv()
		return IntLit{Raw: v}
	case tLParen:
		p.adv()
		e := p.expr()
		p.expect(tRParen, ")")
		return e
	case tIdent:
		switch p.tok.val {
		case "true", "false":
			v := p.tok.val == "true"
			p.adv()
			return BoolLit{Value: v}
		case "shell":
			return p.shellExpr()
		}
		name := p.tok.val
		p.adv()
		// A qualified call — `file.write(...)`, `docker.compose-up(...)`. The dot is
		// package membership here, not a field access, so it must be recognised before
		// postfix() would read it as one. Distinguished by the `(` that follows: a
		// bare `r.exit` stays a field (#296).
		if p.tok.kind == tDot && p.peekQualifiedCall() {
			p.adv()
			name += "." + p.expect(tIdent, "instruction name after '.'").val
		}
		if p.tok.kind == tLParen { // a call: name(args)
			return Call{Name: name, Args: p.callArgs()}
		}
		return Ident{Name: name}
	default:
		p.fail("expected an expression, got %q", p.tok.val)
		return nil // unreachable
	}
}

func (p *parser) shellExpr() Expr {
	interp := p.shellInterp() // optional `shell(<interp>)` (ADR-0012)
	body, err := p.lex.rawShellBody() // lexer sits right after "shell" / its interp
	if err != nil {
		panic(parseErr{err})
	}
	p.adv()
	e := ShellExpr{Cmd: body, Interp: interp}
	if p.tok.kind == tIdent && p.tok.val == "unless" {
		guard, err := p.lex.rawBracesRequired()
		if err != nil {
			panic(parseErr{err})
		}
		p.adv()
		e.Unless = guard
	}
	return e
}

func (p *parser) callArgs() []Expr {
	p.expect(tLParen, "(")
	var args []Expr
	for p.tok.kind != tRParen {
		args = append(args, p.expr())
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRParen, ")")
	return args
}
