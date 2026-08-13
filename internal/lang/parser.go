package lang

import (
	"fmt"
	"strings"

	"shellf/internal/inventory"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
)

// InstructionSig reports an instruction's positional parameter names and how
// many are required (the leading params without a default), so the parser can
// turn `apt.install("nginx")` into Step{Args:{"pkg":"nginx"}} and allow omitting
// trailing defaulted params. The caller supplies it (from `std.Lookup` +
// builtins), so signatures live with the defs, not a duplicated map here (#107).
type InstructionSig func(name string) (params []string, required int, ok bool)

// defaultSig backs ParsePlan (the no-vars convenience used by tests, which set
// it — see sig_test.go). Production goes through ParsePlanWithVars with an
// explicit std-backed sig.
var defaultSig InstructionSig

// ParseInventory parses an inventory file into an Inventory.
func ParseInventory(src string) (inv inventory.Inventory, err error) {
	defer catch(&err)
	p := newParser(src)
	inv = p.inventory()
	return
}

// ParsePlan parses a plan file into an orchestration Plan.
func ParsePlan(src string) (orchestrator.Plan, error) {
	return ParsePlanWithVars(src, map[string]string{}, nil, defaultSig)
}

// ParsePlanWithVars parses a plan with pre-loaded global variables. `baseVars`
// (from a --vars file) is the lower-precedence table; `setVars` (from --set) is
// the highest. `sig` supplies instruction parameter names. Plan-level bindings
// are appended to `baseVars` (mutated in place), so the caller can pass the
// enriched table to the orchestrator for per-host resolution of the Steps'
// Refs. Interpolation and binding values are resolved here; bare-identifier
// arguments are left as Refs.
func ParsePlanWithVars(src string, baseVars, setVars map[string]string, sig InstructionSig) (plan orchestrator.Plan, err error) {
	defer catch(&err)
	p := newParser(src)
	p.sig = sig
	if baseVars != nil {
		p.baseVars = baseVars
	}
	p.setVars = setVars
	plan = p.plan()
	return
}

// ParsePackage parses a plan together with its package's sibling def files
// (ADR-0014): the directory is the package, so user defs written in any file are
// resolved by name across it. Sibling `libs` (name → source) are def-only; the
// invoked `planSrc` carries the `on` blocks (and may also declare defs). Returns
// the plan and the collected user defs (to ship to the agent). Duplicate names,
// un-annotated stdlib shadowing, and overriding-nothing all error here — locally,
// before any host is touched.
func ParsePackage(planSrc string, libs map[string]string, imports map[string][]string, baseVars, setVars map[string]string, stdSig InstructionSig) (plan orchestrator.Plan, userDefs map[string]Def, err error) {
	defer catch(&err)
	defsByName := map[string]Def{}

	// 1. Sibling library files: defs only (an `on` block belongs to the plan).
	for fname, src := range libs {
		lp := newParser(src)
		lp.sig, lp.userDefs = stdSig, defsByName
		// A file keyed `<dir>/<file>` comes from a sub-package: its defs are
		// qualified `<dir>.<def>` (ADR-0033). The declaration itself never carries
		// a dot — the directory names, the author does not.
		if i := strings.IndexByte(fname, '/'); i > 0 {
			lp.defPrefix = fname[:i] + "."
		}
		for lp.tok.kind != tEOF {
			if !isDefStart(lp.tok) {
				lp.fail("package file %s may only contain defs, not %q", fname, lp.tok.val)
			}
			lp.registerDef(lp.defWithSource())
		}
	}

	// 2. The invoked plan file: imports (first), bindings, `on` blocks, inline defs.
	p := newParser(planSrc)
	p.sig, p.userDefs, p.imports = stdSig, defsByName, imports
	if baseVars != nil {
		p.baseVars = baseVars
	}
	p.setVars = setVars
	plan = p.plan()

	return plan, defsByName, nil
}

// ScanImports extracts the leading `import <alias> "<path>"` statements of a
// plan file, so the CLI knows which directories to load (ADR-0015). Imports must
// come first; scanning stops at the first non-import top-level construct.
func ScanImports(src string) (imports []Import, err error) {
	defer catch(&err)
	p := newParser(src)
	for p.tok.kind == tIdent && p.tok.val == "import" {
		imports = append(imports, p.importStmt())
	}
	return imports, nil
}

// importStmt parses `import <alias> "<path>"` (ADR-0015).
func (p *parser) importStmt() Import {
	p.expect(tIdent, "'import'")
	alias := p.expect(tIdent, "import alias").val
	path := p.expect(tString, "import path (a string)").val
	return Import{Alias: alias, Path: path}
}

// registerImport qualifies an imported package's defs under its alias
// (`alias.def`) and registers them (ADR-0015). The def sources come from the
// caller (the CLI reads the directory); each must be a def-only library.
func (p *parser) registerImport(imp Import) {
	if p.importedAliases[imp.Alias] {
		p.fail("duplicate import alias %q", imp.Alias)
	}
	srcs, ok := p.imports[imp.Alias]
	if !ok {
		p.fail("import %q (%s): package not loaded", imp.Alias, imp.Path)
	}
	p.importedAliases[imp.Alias] = true
	for _, src := range srcs {
		defs, derr := parseLibrary(src)
		if derr != nil {
			p.fail("import %q (%s): %v", imp.Alias, imp.Path, derr)
		}
		for _, d := range defs {
			qname := imp.Alias + "." + d.Name
			if _, dup := p.userDefs[qname]; dup {
				p.fail("duplicate imported instruction %q", qname)
			}
			if p.stdHas(qname) {
				p.fail("imported %q collides with a stdlib instruction", qname)
			}
			p.userDefs[qname] = d
		}
	}
}

// parseLibrary parses a def-only file (an imported package file), capturing each
// def's source. It rejects `on` blocks and nested `import`s (no transitive deps,
// ADR-0015).
func parseLibrary(src string) (defs []Def, err error) {
	defer catch(&err)
	p := newParser(src)
	for p.tok.kind != tEOF {
		if p.tok.kind == tIdent && p.tok.val == "import" {
			p.fail("an imported package may not itself import (no transitive deps)")
		}
		if !isDefStart(p.tok) {
			p.fail("an imported package may only contain defs, not %q", p.tok.val)
		}
		defs = append(defs, p.defWithSource())
	}
	return defs, nil
}

// defWithSource parses a def and captures its own source text (from the `def`/
// `override` token to just before the next token), so the package's user defs
// can be shipped to the agent as text (ADR-0014).
func (p *parser) defWithSource() Def {
	start := p.lex.tokStart
	d := p.def()
	d.Source = strings.TrimSpace(p.lex.src[start:p.lex.tokStart])
	return d
}

// registerDef adds a user def to the package, enforcing the ADR-0014 rules:
// no duplicate name, no un-annotated shadowing of a stdlib def, and `override`
// only where a stdlib def actually exists.
func (p *parser) registerDef(d Def) {
	if p.userDefs == nil {
		p.userDefs = map[string]Def{}
	}
	// The name a caller writes: bare in the package root, `<dir>.<def>` in a
	// sub-package (ADR-0033). Errors name it too, so the message matches what the
	// author would have to type at the call site.
	name := p.defPrefix + d.Name
	if _, dup := p.userDefs[name]; dup {
		p.fail("duplicate def %q in the package", name)
	}
	std := p.stdHas(name)
	switch {
	case d.Override && !std:
		p.fail("override def %q overrides nothing (no stdlib def by that name)", name)
	case !d.Override && std:
		p.fail("def %q shadows a stdlib def; use `override def` to replace it", name)
	}
	p.userDefs[name] = d
}

func (p *parser) stdHas(name string) bool {
	if p.sig == nil {
		return false
	}
	_, _, ok := p.sig(name)
	return ok
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
	caught   map[string]bool   // vars bound with `?` in the current `on` block (ADR-0009)
	sig      InstructionSig    // stdlib/builtin instruction parameter names (#107)
	userDefs map[string]Def    // package + imported defs, resolved before the stdlib (ADR-0014/0015)
	defPrefix string        // sub-package prefix for defs declared in this file (ADR-0033)

	imports         map[string][]string // alias → imported package's def sources (ADR-0015)
	importedAliases map[string]bool     // aliases already imported (duplicate check)
}

// resolveSig looks up an instruction's parameter names and required count: a
// package user def first (ADR-0014), then the stdlib/builtins.
func (p *parser) resolveSig(name string) ([]string, int, bool) {
	if d, ok := p.userDefs[name]; ok {
		return paramNames(d), requiredCount(d), true
	}
	if p.sig != nil {
		return p.sig(name)
	}
	return nil, 0, false
}

// requiredCount is the number of leading parameters without a default (ADR-0013).
func requiredCount(d Def) int {
	n := 0
	for _, par := range d.Params {
		if par.Default == nil {
			n++
		}
	}
	return n
}

// isDefStart reports whether a token begins a def declaration (`def` or the
// `override` that precedes it).
func isDefStart(t token) bool {
	return t.kind == tIdent && (t.val == "def" || t.val == "override")
}

func paramNames(d Def) []string {
	names := make([]string, len(d.Params))
	for i, par := range d.Params {
		names[i] = par.Name
	}
	return names
}

func newParser(src string) *parser {
	p := &parser{lex: newLexer(src), baseVars: map[string]string{}, caught: map[string]bool{}, importedAliases: map[string]bool{}}
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
		case "local":
			if v != "true" && v != "false" {
				p.fail("local must be \"true\" or \"false\", got %q", v)
			}
			h.Local = v == "true"
		case "interpreter":
			if !validInterp(v) {
				p.fail("unknown interpreter %q (want sh/bash/dash/nu/raw)", v)
			}
			h.Interpreter = v
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
	seenNonImport := false
	for p.tok.kind != tEOF {
		// `import <alias> "<path>"` — must come first, before any def/on/binding.
		if p.tok.kind == tIdent && p.tok.val == "import" {
			if seenNonImport {
				p.fail("imports must come before defs, bindings, and `on` blocks")
			}
			p.registerImport(p.importStmt())
			continue
		}
		seenNonImport = true
		// `def …` / `override def …` — a package-local instruction (ADR-0014).
		if isDefStart(p.tok) {
			p.registerDef(p.defWithSource())
			continue
		}
		kw := p.expect(tIdent, "'on', 'def', or a binding").val
		if kw != "on" {
			// Top-level binding: `name = value`. Appended to baseVars; --set still
			// wins at resolution (via lookup order), so no pinning is needed here.
			p.expect(tEq, "=")
			p.baseVars[kw] = p.arg()
			continue
		}
		target := p.expect(tIdent, "group or host").val
		become := ""
		if p.tok.kind == tIdent && p.tok.val == "as" { // `on <target> as <user> { … }` (ADR-0011)
			p.adv()
			become = p.expect(tIdent, "user after 'as'").val
		}
		p.caught = map[string]bool{} // caught vars are scoped to their `on` block
		steps := p.block()
		if become != "" { // escalate the whole block by wrapping it
			steps = []proto.Step{{Become: become, Block: steps}}
		}
		plan = append(plan, orchestrator.Block{Target: target, Steps: steps})
	}
	return plan
}

func (p *parser) block() []proto.Step {
	p.expect(tLBrace, "{")
	steps := p.blockBody()
	p.expect(tRBrace, "}")
	return steps
}

// blockBody parses a sequence of steps until `}` or EOF, expanding `for` loops
// inline (ADR-0017). Used by `block()` and by a loop's re-parsed body.
func (p *parser) blockBody() []proto.Step {
	var steps []proto.Step
	for p.tok.kind != tRBrace && p.tok.kind != tEOF {
		if p.tok.kind == tIdent && p.tok.val == "for" {
			steps = append(steps, p.forLoop()...)
			continue
		}
		steps = append(steps, p.step())
	}
	return steps
}

// forLoop parses `for <var> in [<str>, …] { <body> }` and unrolls it at parse
// time: the body is captured once and re-parsed per item with `${var}` bound to
// that item (ADR-0017). No runtime loop, no list value.
func (p *parser) forLoop() []proto.Step {
	p.expect(tIdent, "'for'") // val is "for" (checked by the caller)
	varName := p.expect(tIdent, "loop variable").val
	if kw := p.expect(tIdent, "'in'").val; kw != "in" {
		p.fail("expected 'in' after the loop variable, got %q", kw)
	}
	p.expect(tLBrack, "[")
	var items []string
	for p.tok.kind != tRBrack {
		items = append(items, p.interpolate(p.expect(tString, "list item (a string)").val))
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	if p.tok.kind != tRBrack {
		p.fail("expected ',' or ']' in the list, got %q", p.tok.val)
	}
	// p.tok is ']'; the lexer sits right after it, before the body brace.
	body, err := p.lex.rawBracesRequired()
	if err != nil {
		panic(parseErr{err})
	}
	p.adv() // consume the token after the body's '}'

	var steps []proto.Step
	for _, item := range items {
		bp := newParser(body)
		bp.sig, bp.userDefs, bp.imports = p.sig, p.userDefs, p.imports
		bp.importedAliases, bp.setVars, bp.caught = p.importedAliases, p.setVars, p.caught
		bp.baseVars = copyVars(p.baseVars)
		bp.baseVars[varName] = item // `${var}` resolves to this item in the body
		steps = append(steps, bp.blockBody()...)
	}
	return steps
}

func copyVars(m map[string]string) map[string]string {
	c := make(map[string]string, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (p *parser) step() proto.Step {
	// `shell` and `if` are special forms, handled before the call() path.
	if p.tok.kind == tIdent && p.tok.val == "shell" {
		return p.shellStep()
	}
	if p.tok.kind == tIdent && p.tok.val == "if" {
		return p.ifStep()
	}
	if p.tok.kind == tIdent && p.tok.val == "as" {
		return p.asBlock()
	}
	name := p.expect(tIdent, "instruction or 'parallel'").val
	if p.tok.kind == tEq { // capture: name = <call>
		p.adv()
		rhs := p.step()
		if rhs.If != nil || len(rhs.Parallel) > 0 {
			p.fail("cannot capture an if/parallel block into a variable")
		}
		rhs.Bind = name
		if rhs.Caught { // `x = call()?` — x's errors are handled, not auto-halts
			p.caught[name] = true
		}
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
	if p.tok.kind == tBang { // `if !cond`
		p.adv()
		ib.Negate = true
	}
	cond, pat, neg := p.condition()
	if neg { // `!=` flips the branch truth, composing with a leading `!`
		ib.Negate = !ib.Negate
	}
	if cond != nil { // inline instruction; pat (if any) is its outcome match
		ib.Cond = cond
		ib.Match = pat
	} else {
		ib.CondRef = pat
	}
	ib.Then = p.block()
	if p.tok.kind == tIdent && p.tok.val == "else" {
		p.adv()
		ib.Else = p.block()
	}
	return proto.Step{If: ib}
}

// condition parses an if condition and reports whether it is negated (`!=`).
// It returns either an inline step (with an optional outcome Match, when the
// second result is non-nil) or a captured-result ref (when the step is nil).
// Forms: an instruction (`call()`, `call()? == err.tag`) or shell run inline; an
// outcome test on a captured Result (`s == ok`, `s != err.dbLocked`); the
// `.changed` flag; or a bare capture (`s` = `s == ok`). See ADR-0008/0009.
func (p *parser) condition() (*proto.Step, *proto.ResultRef, bool) {
	if p.tok.kind == tIdent && p.tok.val == "shell" {
		s := p.shellStep()
		return &s, nil, false
	}
	name := p.expect(tIdent, "condition").val

	// Outcome-pattern test on a captured result: `s == ok`, `s != err.dbLocked`.
	if p.tok.kind == tEqEq || p.tok.kind == tNotEq {
		cat, tag, neg := p.outcomeMatch()
		if !neg && cat == "err" && !p.caught[name] {
			p.fail("unreachable error test: %q is not caught — mark its instruction with `?` (ADR-0009)", name)
		}
		return nil, &proto.ResultRef{Name: name, Category: cat, Tag: tag}, neg
	}

	if p.tok.kind == tDot {
		p.adv()
		second := p.expect(tIdent, "field or instruction").val
		if p.tok.kind == tLParen { // qualified inline call: name.second(...)
			return p.inlineCond(p.call(name + "." + second))
		}
		if second == "changed" {
			return nil, &proto.ResultRef{Name: name, Changed: true}, false
		}
		if second == "ok" || second == "err" {
			p.fail("`.%s` on a result was removed; test it with `== %s` (ADR-0008)", second, second)
		}
		p.fail("unknown result field %q (want .changed, or == ok/err)", second)
	}
	if p.tok.kind == tLParen { // inline call: name(...)
		return p.inlineCond(p.call(name))
	}
	return nil, &proto.ResultRef{Name: name, Category: "ok"}, false // `if s {` → s == ok
}

// outcomeMatch parses `== cat[.tag]` / `!= cat[.tag]` (tok is at the operator).
func (p *parser) outcomeMatch() (cat, tag string, neg bool) {
	neg = p.tok.kind == tNotEq
	p.adv()
	cat = p.expect(tIdent, "outcome category").val
	if cat != "ok" && cat != "err" && cat != "would" {
		p.fail("unknown outcome category %q (want ok/err/would)", cat)
	}
	if p.tok.kind == tDot { // optional tag: `== err.dbLocked`
		p.adv()
		tag = p.expect(tIdent, "outcome tag").val
	}
	return
}

// inlineCond finishes an inline-call condition with an optional outcome match
// (`call()? == err.tag`). A positive `== err[.tag]` requires the call be caught,
// else the branch is unreachable under halt-on-err (ADR-0009).
func (p *parser) inlineCond(s proto.Step) (*proto.Step, *proto.ResultRef, bool) {
	if p.tok.kind != tEqEq && p.tok.kind != tNotEq {
		return &s, nil, false
	}
	cat, tag, neg := p.outcomeMatch()
	if !neg && cat == "err" && !s.Caught {
		p.fail("unreachable error test: mark the instruction with `?` (ADR-0009)")
	}
	return &s, &proto.ResultRef{Category: cat, Tag: tag}, neg
}

// shellStep parses `shell <line>` or `shell { … }` with an optional
// `unless { … }` guard. When p.tok is the `shell`/`unless` keyword, the lexer
// sits right after it, so raw capture reads from there.
func (p *parser) shellStep() proto.Step {
	// `shell(<interp>) as <user> { … }` — both read from the raw stream, since
	// the shell body itself is raw-captured (ADR-0012 / ADR-0011).
	interp := p.shellInterp()
	become := ""
	if p.lex.tryRawWord("as") {
		if become = p.lex.rawIdent(); become == "" {
			p.fail("expected a user after `as`")
		}
	}
	body, err := p.lex.rawShellBody()
	if err != nil {
		panic(parseErr{err})
	}
	p.adv() // resync to the token after the shell body

	args := map[string]string{"cmd": body}
	if p.tok.kind == tIdent && p.tok.val == "unless" {
		p.fail("`unless` was removed from plans; use `if !shell { <guard> } { shell { <cmd> } }`")
	}
	return proto.Step{Instruction: "shell", Args: args, Become: become, Interp: interp, With: p.parseWith()}
}

// parseWith parses an optional `with { k = value, … }` block after a call
// (ADR-0022): a per-call variable override. Each value is a string interpolated
// with the global variables at parse; the bindings add or override variables for
// that call only. Returns nil when no `with` follows.
func (p *parser) parseWith() map[string]string {
	if p.tok.kind != tIdent || p.tok.val != "with" {
		return nil
	}
	p.adv()
	p.expect(tLBrace, "{")
	with := map[string]string{}
	for p.tok.kind != tRBrace {
		k := p.expect(tIdent, "a binding name").val
		p.expect(tEq, "=")
		with[k] = p.arg()
		if p.tok.kind == tComma {
			p.adv()
		}
	}
	p.expect(tRBrace, "}")
	if len(with) == 0 {
		p.fail("`with { }` must bind at least one variable")
	}
	return with
}

// shellInterp parses an optional `(<interp>)` after `shell` (ADR-0012).
func (p *parser) shellInterp() string {
	if !p.lex.tryRawByte('(') {
		return ""
	}
	name := p.lex.rawIdent()
	if !p.lex.tryRawByte(')') {
		p.fail("expected `)` after shell interpreter")
	}
	if !validInterp(name) {
		p.fail("unknown shell interpreter %q (want sh/bash/dash/nu/raw)", name)
	}
	return name
}

func validInterp(name string) bool {
	switch name {
	case "sh", "bash", "dash", "nu", "raw":
		return true
	}
	return false
}

// asBlock parses `as <user> { <steps> }` — a sequential block whose shells run
// escalated to <user> (ADR-0011).
func (p *parser) asBlock() proto.Step {
	p.adv() // consume 'as'
	user := p.expect(tIdent, "user after 'as'").val
	return proto.Step{Become: user, Block: p.block()}
}

// Renamed maps every pre-ADR-0032 instruction name to its package form. It exists
// only to turn "unknown instruction" into an actionable message — the old names are
// NOT accepted (ADR-0032 §4: no aliases, no transition). A plan using one still fails;
// it just says what to write instead.
var Renamed = map[string]string{
	"file-write": "file.write", "file-mode": "file.mode", "file-line": "file.line",
	"file-delete": "file.delete", "file-replace": "file.replace",
	"file-exists": "file.exists", "file-download": "file.download",
	"file-copy": "file.copy", "template": "file.template",
	"dir-ensure": "dir.ensure", "dir-exists": "dir.exists", "dir-owner": "dir.owner",
	"dir-copy": "dir.copy",
	"archive-extract": "archive.extract", "archive-extract-member": "archive.extract-member",
	"git-clone": "git.clone", "git-sync": "git.sync",
	"service": "service.ensure", "service-restart": "service.restart",
	"service-reload": "service.reload",
	"systemd-daemon-reload": "systemd.daemon-reload",
	"user-ensure":           "user.ensure", "user-group": "user.group",
	"http-check": "http.check", "wait-for": "http.wait-for",
}

func (p *parser) call(name string) proto.Step {
	argNames, required, ok := p.resolveSig(name)
	if !ok {
		if to, was := Renamed[name]; was {
			p.fail("unknown instruction %q — renamed to %q (ADR-0032)", name, to)
		}
		p.fail("unknown instruction %q", name)
	}
	p.expect(tLParen, "(")
	type argv struct {
		val, ref string
		control  bool
	}
	var vals []argv
	for p.tok.kind != tRParen {
		v, r, ctl := p.callArg()
		vals = append(vals, argv{v, r, ctl})
		if p.tok.kind == tComma {
			p.adv()
		} else {
			break
		}
	}
	p.expect(tRParen, ")")
	caught := false
	if p.tok.kind == tQuestion { // `call()?` — mark failible-but-caught (ADR-0009)
		p.adv()
		caught = true
	}

	if len(vals) < required || len(vals) > len(argNames) {
		if required == len(argNames) {
			p.fail("%s expects %d argument(s), got %d", name, required, len(vals))
		}
		p.fail("%s expects %d–%d argument(s), got %d", name, required, len(argNames), len(vals))
	}
	// Map the supplied args positionally; omitted trailing params (which must be
	// defaulted) are filled by the def at eval (ADR-0013).
	args := map[string]string{}
	var refs map[string]string
	var control []string
	for i := 0; i < len(vals); i++ {
		n := argNames[i]
		if vals[i].ref != "" {
			if refs == nil {
				refs = map[string]string{}
			}
			refs[n] = vals[i].ref
		} else {
			args[n] = vals[i].val
			if vals[i].control {
				control = append(control, n)
			}
		}
	}
	return proto.Step{Instruction: name, Args: args, Refs: refs, Control: control, Caught: caught, With: p.parseWith()}
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
func (p *parser) callArg() (value, ref string, control bool) {
	switch {
	case p.tok.kind == tPercent:
		// `%"conf.j2"` — a path on the control host (ADR-0034 §1). The value travels as
		// an ordinary string; the marker is recorded on the step, so the set of files
		// the plan needs can be derived before anything is sent (ADR-0031 §3).
		p.adv()
		if p.tok.kind != tString && p.tok.kind != tRawString {
			p.fail("%% must be followed by a path string in an argument, got %q", p.tok.val)
		}
		v := p.interpolate(p.tok.val)
		p.adv()
		return v, "", true
	case p.tok.kind == tString:
		v := p.interpolate(p.tok.val)
		p.adv()
		return v, "", false
	case p.tok.kind == tRawString:
		v := p.tok.val
		p.adv()
		return v, "", false
	case p.tok.kind == tIdent && (p.tok.val == "true" || p.tok.val == "false"):
		v := p.tok.val
		p.adv()
		return v, "", false
	case p.tok.kind == tIdent:
		name := p.tok.val
		p.adv()
		return "", name, false
	default:
		p.fail("expected a string, bool, or variable argument, got %q", p.tok.val)
		return "", "", false // unreachable
	}
}

// interpolate replaces every `${name}` in a simple string with the bound value,
// failing on an unterminated `${` or an undefined name. Raw triple-quoted
// strings never reach here, so their `${VAR}` (shell/compose) stay verbatim.
func (p *parser) interpolate(s string) string {
	out, err := Interpolate(s, p.lookup)
	if err != nil {
		p.fail("%v", err)
	}
	return out
}

// Template renders a template file (ADR-0021): `@{name}` is interpolated (a
// different sigil from the plan's `${}`, so a config file's own `${…}`/`{{ }}`
// pass through verbatim), and `@@` is a literal `@`. An unterminated `@{` or an
// undefined name is an error.
func Template(s string, lookup func(string) (string, bool)) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '@' && i+1 < len(s) && s[i+1] == '@' { // @@ -> @
			out.WriteByte('@')
			i += 2
			continue
		}
		if s[i] == '@' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated @{...} in template")
			}
			name := s[i+2 : i+2+end]
			v, ok := lookup(name)
			if !ok {
				return "", fmt.Errorf("undefined variable %q in template", name)
			}
			out.WriteString(v)
			i += 2 + end + 1
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String(), nil
}

// Interpolate replaces every `${name}` in s with lookup(name), erroring on an
// unterminated `${` or an undefined name. Used for the plan's string arguments.
func Interpolate(s string, lookup func(string) (string, bool)) (string, error) {
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
		if end < 0 {
			return "", fmt.Errorf("unterminated ${...} interpolation")
		}
		name := rest[:end]
		v, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("undefined variable %q in interpolation", name)
		}
		out.WriteString(v)
		s = rest[end+1:]
	}
}
