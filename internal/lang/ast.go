package lang

// AST for `def` declarations. Instruction bodies are phase-structured
// (check / observe / preview / apply), plus a final default outcome.

type Def struct {
	Name     string
	Params   []Param
	Become   string // intrinsic escalation user from `def … as <user>` (ADR-0011)
	Interp   string // def-declared interpreter from `def … using <interp>` (ADR-0012)
	Override bool   // `override def` — deliberately shadows a stdlib def (ADR-0014)
	Phases   []Phase
	Return   *Outcome // derived: apply's trailing `return`, for `would` in check (ADR-0007)
	Source   string   // the def's own source text, to ship to the agent (ADR-0014)
}

// Import binds an alias to a local package directory (ADR-0015): the alias
// qualifies calls to that package's defs, e.g. `import web "../shared"` →
// `web.deploy(...)`.
type Import struct {
	Alias string
	Path  string
}

type Param struct {
	Name    string
	Type    string // "str" | "bool"
	Default Expr   // optional `= <literal>` default (nil if none); ADR-0013 intent params
}

type Phase struct {
	Name  string // check | observe | preview | apply (ADR-0035)
	Stmts []Stmt
}

// --- statements ---

type Stmt interface{ isStmt() }

// LetStmt: `name = value` (immutable binding, no keyword)
type LetStmt struct {
	Name  string
	Value Expr
}

// EffectStmt: a bare expression evaluated for its effect (a `shell { … }`).
type EffectStmt struct {
	Expr Expr
}

// IfStmt: `if <cond> { <body> }` (ADR-0006).
type IfStmt struct {
	Cond Expr
	Body []Stmt
}

// ReturnStmt: `return <outcome>` (ADR-0006).
type ReturnStmt struct {
	Outcome Outcome
}

// StateReturnStmt: `return state(field: expr, …)` — the observed-state record an
// `observe` phase yields (ADR-0013). Distinct from ReturnStmt: it carries named
// values, not an ok/err/would outcome.
type StateReturnStmt struct {
	Fields []StateField
}

type StateField struct {
	Name  string
	Value Expr
}

func (LetStmt) isStmt()         {}
func (EffectStmt) isStmt()      {}
func (IfStmt) isStmt()          {}
func (ReturnStmt) isStmt()      {}
func (StateReturnStmt) isStmt() {}

// Outcome: `category.tag` or `category.tag(payload)` or bare `category`.
type Outcome struct {
	Category string // ok | err | would
	Tag      string // "" for a bare category
	Payload  Expr   // nil if none
}

// --- expressions ---

type Expr interface{ isExpr() }

type StrLit struct{ Value string }

// ControlPath is `%"conf.j2"` — a path on the control host, not the target (ADR-0034).
// A distinct node, not a string with a marker inside: the prefix stays visible when the
// value is read, and the set of files a plan needs is extractable before the run.
type ControlPath struct{ Value string }
type BoolLit struct{ Value bool }
type IntLit struct{ Raw string } // parsed to int by the evaluator
type Ident struct{ Name string } // pkg, r, and the when-shorthands ok/err
type Field struct {              // r.exit
	Recv Expr
	Name string
}
type Binary struct { // a == b, a != b
	Op   string // "==" | "!="
	L, R Expr
}
type Unary struct { // !x — negate truthiness (ADR-0010)
	Op string // "!"
	X  Expr
}
type Call struct { // apt-cache-show(pkg)
	Name string
	Args []Expr
	// Control marks `~file.read(…)`: a primitive evaluated on the control host rather
	// than an instruction run on the target (ADR-0034). Only a name from the closed set
	// may carry it — `%` before a def is a parse error, because a def can run shell and
	// shell prefixed by `%` would run on the operator's machine.
	Control bool
}
type ShellExpr struct { // shell(<interp>) { … } [unless { … }] or shell <line>
	Cmd    string
	Unless string
	Interp string // shell(<interp>) block annotation (ADR-0012)
}

func (StrLit) isExpr()      {}
func (ControlPath) isExpr() {}
func (BoolLit) isExpr()     {}
func (IntLit) isExpr()      {}
func (Ident) isExpr()       {}
func (Field) isExpr()       {}
func (Binary) isExpr()      {}
func (Unary) isExpr()       {}
func (Call) isExpr()        {}
func (ShellExpr) isExpr()   {}
