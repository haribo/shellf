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
	"post":  "removed (ADR-0035); it was never used and had no settled meaning",
}
var categories = map[string]bool{"ok": true, "err": true, "would": true}

// ParseDefs parses a file of `def` declarations written by a user — a project's own
// defs, or an imported module's (ADR-0016). Its shell blocks are checked (ADR-0040 §2).
func ParseDefs(src string) (defs []Def, err error) { return parseDefs(src, false) }

// ParseStdlibDefs parses the embedded standard library, which the detector exempts: it is
// the layer that reaches the system, and `service.ensure` is implemented with the very
// `systemctl` a rule would forbid (ADR-0040 §6).
func ParseStdlibDefs(src string) (defs []Def, err error) { return parseDefs(src, true) }

func parseDefs(src string, trusted bool) (defs []Def, err error) {
	defer catch(&err)
	p := newParser(src)
	p.trusted = trusted
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

	// Everything below is the def's body, where a control-host path is refused
	// (ADR-0043). Restored rather than cleared: a def written inline in a plan file
	// returns to plan context, where `%"…"` is exactly how a path is declared.
	outer := p.inDef
	p.inDef = true
	defer func() { p.inDef = outer }()

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
		case p.tok.kind == tIdent && p.peekIsCall():
			// A call outside every phase: a delegation (ADR-0037 §2). Gated on the
			// lookahead so an unknown phase name still gets "expected a phase, got
			// %q" — without it, `badphase { … }` reads as a call and fails two tokens
			// later on the brace, which says nothing about what is wrong.
			if d.Delegate != nil {
				p.fail("a def delegates to exactly one other def; a second call belongs in `apply` (ADR-0037)")
			}
			e := p.expr()
			c, ok := e.(Call)
			if !ok {
				p.fail("expected a phase or a call to delegate to, got %q", p.tok.val)
			}
			d.Delegate = &c
		default:
			p.fail("expected a phase, got %q", p.tok.val)
		}
	}
	p.expect(tRBrace, "}")
	if d.Delegate != nil {
		p.checkDelegation(d)
	} else {
		p.checkApplyReturns(d)
	}
	d.Return = nominalReturn(d)
	return d
}

// checkDelegation enforces what may sit beside a delegation (ADR-0037 §2 and §3).
func (p *parser) checkDelegation(d Def) {
	for _, ph := range d.Phases {
		if ph.Name != "check" {
			// You delegate the decision or you make it. An `observe` next to a
			// delegation duplicates the callee's, which is the duplication the form
			// exists to remove; `apply` and `preview` describe an action the callee
			// already owns.
			p.fail("a def that delegates may only declare `check`, not %q — delegate the decision or make it (ADR-0037)", ph.Name)
		}
	}
	// The arguments are evaluated in every mode, `--dry-run` included, because the
	// callee's `observe` needs their value to decide whether it is already in sync. A
	// `shell` there would run for real during a dry-run.
	for _, a := range d.Delegate.Args {
		if hasShell(a) {
			p.fail("a delegation's arguments may not run a shell: they are evaluated in `--dry-run` too (ADR-0037)")
		}
	}
}

// hasShell reports whether an expression runs a shell anywhere inside it.
func hasShell(e Expr) bool {
	switch t := e.(type) {
	case ShellExpr:
		return true
	case Call:
		for _, a := range t.Args {
			if hasShell(a) {
				return true
			}
		}
	}
	return false
}

// checkApplyReturns enforces ADR-0037 §1: an `apply` names its verdict. The implicit
// tag-less `ok` it replaces (ADR-0007 §4) made a forgotten `return` read as a success.
func (p *parser) checkApplyReturns(d Def) {
	for _, ph := range d.Phases {
		if ph.Name != "apply" {
			continue
		}
		n := len(ph.Stmts)
		if n == 0 {
			p.fail("`apply` is empty: end it with a `return ok.<tag>` (ADR-0037)")
		}
		if _, ok := ph.Stmts[n-1].(ReturnStmt); !ok {
			p.fail("`apply` must end with a `return ok.<tag>` saying what it did (ADR-0037)")
		}
	}
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
// peekIsCall reports whether the ident under the cursor starts a call — `name(` or a
// qualified `pkg.name(`. It exists so a def body can tell a delegation from a phase name
// without consuming anything.
func (p *parser) peekIsCall() bool {
	save := *p.lex
	tok := p.tok
	defer func() { *p.lex, p.tok = save, tok }()
	p.adv()
	if p.tok.kind == tDot { // qualified: pkg.name(
		p.adv()
		if p.tok.kind != tIdent {
			return false
		}
		p.adv()
	}
	return p.tok.kind == tLParen
}

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

// primitiveCall parses `~name.action(args)` — an engine primitive (ADR-0036 §1). The
// marker says what it is, not where it runs: `~file.write` acts on the target, and the
// `%` on an argument is what says a path is the operator's.
//
// The name must be in the closed set. That refusal is the rule keeping shell off the
// operator machine: a def can run shell, so if `~` could prefix a def, shell would run
// where every SSH key lives.
func (p *parser) primitiveCall() Expr {
	p.adv() // consume '~'
	if p.tok.kind != tIdent {
		p.fail("~ must be followed by a primitive name, got %q", p.tok.val)
	}
	name := p.tok.val
	p.adv()
	if p.tok.kind == tDot {
		p.adv()
		name += "." + p.expect(tIdent, "primitive name after '.'").val
	}
	if !ControlPrimitives[name] {
		p.fail("~%s is not a primitive (ADR-0036); only %s may carry ~", name, controlPrimitiveList())
	}
	if p.tok.kind != tLParen {
		p.fail("~%s must be called", name)
	}
	return Call{Name: name, Args: p.callArgs(), Control: true}
}

// controlPathLit parses `%"conf.j2"` — a path on the control host. `%` before anything
// else is refused: it used to prefix a primitive (ADR-0034) and that spelling is gone,
// so the error names what to write instead.
func (p *parser) controlPathLit() Expr {
	p.adv() // consume '%'
	if p.tok.kind != tString && p.tok.kind != tRawString {
		if p.tok.kind == tIdent {
			// `%name(…)` was how a primitive was written until ADR-0036. Point at the
			// new spelling without claiming this particular name is one — it may not be.
			p.fail("%% now marks a control-host path, not a call; primitives are written ~%s and only %s exist (ADR-0036)",
				p.tok.val, controlPrimitiveList())
		}
		p.fail("%% must be followed by a path string, got %q", p.tok.val)
	}
	// Checked after the two form errors above, so `%file.read(…)` in a def still gets the
	// ADR-0036 spelling message rather than this one: what is wrong there is the marker,
	// not where it sits.
	if p.inDef {
		// ADR-0043: the allow-list is derived from the plan, so a def naming a file on
		// the operator's machine was adding itself to the set that bounds it (#403). The
		// message names the fix, since the def is one parameter away from correct.
		p.fail("%%\"…\" belongs in a plan, not in a def: take the path as a parameter and let the plan pass it marked (ADR-0043)")
	}
	v := p.tok.val
	p.adv()
	return ControlPath{Value: v}
}

func controlPrimitiveList() string {
	names := make([]string, 0, len(ControlPrimitives))
	for n := range ControlPrimitives {
		names = append(names, "~"+n)
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

// ControlPrimitives is the closed set a `~` may prefix (ADR-0036 §1). It is closed on
// purpose: if `%` could prefix a def, a def could run shell, and shell would run on the
// machine holding every SSH key and every secret. shellf runs shell on targets.
var ControlPrimitives = map[string]bool{
	"file.read":   true,
	"file.write":  true,
	"file.render": true,
	"dir.list":    true,
	"dir.sync":    true,
}

func (p *parser) primary() Expr {
	switch p.tok.kind {
	case tTilde:
		return p.primitiveCall()
	case tPercent:
		return p.controlPathLit()
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
			return p.shellExpr(false)
		case "unsafe": // `unsafe shell { … }` (ADR-0040 §3)
			p.adv()
			if p.tok.kind != tIdent || p.tok.val != "shell" {
				p.fail("`unsafe` marks a shell block: write `unsafe shell { … }`")
			}
			return p.shellExpr(true)
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

func (p *parser) shellExpr(unsafe bool) Expr {
	interp := p.shellInterp()         // optional `shell(<interp>)` (ADR-0012)
	body, err := p.lex.rawShellBody() // lexer sits right after "shell" / its interp
	if err != nil {
		panic(parseErr{err})
	}
	if !unsafe && !p.trusted {
		if msg := checkShellBody(body); msg != "" {
			p.fail("%s", msg)
		}
	}
	p.adv()
	e := ShellExpr{Cmd: body, Interp: interp}
	if p.tok.kind == tIdent && p.tok.val == "unless" {
		// Parsed and stored until #415, and read by nobody: the engine's guard is only
		// ever filled from a plan step, and plans refuse the keyword. So it held in
		// exactly one place, where it did nothing — a def doing
		// `shell { touch "$p" } unless { true }` ran the command with the guard holding.
		// The message is the plan's, so one construct has one answer.
		p.fail("`unless` was removed; use `if !shell { <guard> } { shell { <cmd> } }`")
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
