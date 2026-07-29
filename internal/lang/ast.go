package lang

// AST for `def` declarations. Instruction bodies are phase-structured
// (pre-check / check / guard / apply / post), plus a final default outcome.

type Def struct {
	Name   string
	Params []Param
	Phases []Phase
	Return *Outcome // the bare outcome at the end of the def, if any
}

type Param struct {
	Name string
	Type string // "str" | "bool"
}

type Phase struct {
	Name  string // pre-check | check | guard | apply | post
	Stmts []Stmt
}

// --- statements ---

type Stmt interface{ isStmt() }

// LetStmt: `name = value` (immutable binding, no keyword)
type LetStmt struct {
	Name  string
	Value Expr
}

// GuardStmt: `when cond -> outcome`
type GuardStmt struct {
	Cond    Expr
	Outcome Outcome
}

// EffectStmt: `expr`, or `expr -> outcome`, or `expr -> outcome when cond`.
// The bare form (Outcome nil) is an effect like a raw `shell { … }`.
type EffectStmt struct {
	Expr    Expr
	Outcome *Outcome
	When    Expr // nil = unconditional
}

func (LetStmt) isStmt()    {}
func (GuardStmt) isStmt()  {}
func (EffectStmt) isStmt() {}

// Outcome: `category.tag` or `category.tag(payload)` or bare `category`.
type Outcome struct {
	Category string // ok | err | would
	Tag      string // "" for a bare category
	Payload  Expr   // nil if none
}

// --- expressions ---

type Expr interface{ isExpr() }

type StrLit struct{ Value string }
type BoolLit struct{ Value bool }
type IntLit struct{ Raw string } // parsed to int by the evaluator
type Ident struct{ Name string }        // pkg, r, and the when-shorthands ok/err
type Field struct {                     // r.exit
	Recv Expr
	Name string
}
type Binary struct { // a == b, a != b
	Op   string // "==" | "!="
	L, R Expr
}
type Call struct { // apt-cache-show(pkg)
	Name string
	Args []Expr
}
type ShellExpr struct { // shell { … } [unless { … }] or shell <line>
	Cmd    string
	Unless string
}

func (StrLit) isExpr()    {}
func (BoolLit) isExpr()   {}
func (IntLit) isExpr()    {}
func (Ident) isExpr()     {}
func (Field) isExpr()     {}
func (Binary) isExpr()    {}
func (Call) isExpr()      {}
func (ShellExpr) isExpr() {}
