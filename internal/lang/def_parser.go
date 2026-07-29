package lang

// Parser for `def` declarations. Reuses the lexer and the shared parser helpers
// (adv/expect/fail/catch) from parser.go.

var phaseNames = map[string]bool{
	"pre-check": true, "check": true, "guard": true, "apply": true, "post": true,
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
	if kw := p.expect(tIdent, "'def'").val; kw != "def" {
		p.fail("expected 'def', got %q", kw)
	}
	d := Def{Name: p.expect(tIdent, "def name").val, Params: p.params()}

	p.expect(tLBrace, "{")
	for p.tok.kind != tRBrace {
		switch {
		case p.tok.kind == tIdent && phaseNames[p.tok.val]:
			d.Phases = append(d.Phases, p.phase())
		case p.tok.kind == tIdent && categories[p.tok.val]:
			o := p.outcome()
			d.Return = &o
		default:
			p.fail("expected a phase or a return outcome, got %q", p.tok.val)
		}
	}
	p.expect(tRBrace, "}")
	return d
}

func (p *parser) params() []Param {
	p.expect(tLParen, "(")
	var out []Param
	for p.tok.kind != tRParen {
		name := p.expect(tIdent, "parameter name").val
		p.expect(tColon, ":")
		out = append(out, Param{Name: name, Type: p.expect(tIdent, "type").val})
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRParen, ")")
	return out
}

func (p *parser) phase() Phase {
	ph := Phase{Name: p.expect(tIdent, "phase name").val}
	if p.tok.kind == tColon { // `phase: <stmt>`
		p.adv()
		ph.Stmts = []Stmt{p.stmt()}
		return ph
	}
	p.expect(tLBrace, "{ or :") // `phase { <stmts> }`
	for p.tok.kind != tRBrace {
		ph.Stmts = append(ph.Stmts, p.stmt())
	}
	p.expect(tRBrace, "}")
	return ph
}

func (p *parser) stmt() Stmt {
	if p.tok.kind == tIdent && p.tok.val == "when" {
		p.adv()
		cond := p.expr()
		p.expect(tArrow, "->")
		return GuardStmt{Cond: cond, Outcome: p.outcome()}
	}
	// A binding `name = expr` (no keyword) or an effect `expr [ -> outcome [ when cond ] ]`.
	// Parse the expression first, then a trailing `=` marks a binding — no lookahead needed.
	lhs := p.expr()
	if p.tok.kind == tEq {
		id, ok := lhs.(Ident)
		if !ok {
			p.fail("cannot bind to a non-identifier")
		}
		p.adv()
		return LetStmt{Name: id.Name, Value: p.expr()}
	}
	e := EffectStmt{Expr: lhs}
	if p.tok.kind == tArrow {
		p.adv()
		o := p.outcome()
		e.Outcome = &o
		if p.tok.kind == tIdent && p.tok.val == "when" {
			p.adv()
			e.When = p.expr()
		}
	}
	return e
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
	left := p.postfix()
	if p.tok.kind == tEqEq || p.tok.kind == tNotEq {
		op := p.tok.val
		p.adv()
		return Binary{Op: op, L: left, R: p.postfix()}
	}
	return left
}

func (p *parser) postfix() Expr {
	e := p.primary()
	for p.tok.kind == tDot {
		p.adv()
		e = Field{Recv: e, Name: p.expect(tIdent, "field name").val}
	}
	return e
}

func (p *parser) primary() Expr {
	switch p.tok.kind {
	case tString:
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
	body, err := p.lex.rawShellBody() // lexer sits right after "shell"
	if err != nil {
		panic(parseErr{err})
	}
	p.adv()
	e := ShellExpr{Cmd: body}
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
