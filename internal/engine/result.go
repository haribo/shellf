package engine

// Category is the top level of a Result: idempotence-aware, not an exit code.
type Category int

const (
	OK Category = iota
	ERR
	WOULD
)

func (c Category) String() string {
	switch c {
	case OK:
		return "ok"
	case ERR:
		return "err"
	case WOULD:
		return "would"
	default:
		return "?"
	}
}

// Result is an instruction's semantic outcome — a tagged union, NOT a
// ShellResult. It may CARRY a ShellResult in its payload (e.g. err.runtime),
// but flattening Result to exit/stdout/stderr would be plain bash again.
type Result struct {
	Category Category
	Tag      string
	Changed  bool         // the action actually acted (apply ran); false on a converged skip or err
	Shell    *ShellResult // optional diagnostics payload
	Fields   []FieldDiff  // status mode: the observed-vs-desired state of a resource (ADR-0013)
	Preview  string       // check mode: what an action would do, from the `preview` phase (ADR-0029)
}

// FieldDiff is one observed field of a resource in Status mode: its current
// value, the desired value, and whether they agree.
type FieldDiff struct {
	Name      string
	Current   string
	Desired   string
	Converged bool
}

// String renders "ok", "ok.installed", "would.installed", etc.
func (r Result) String() string {
	if r.Tag == "" {
		return r.Category.String()
	}
	return r.Category.String() + "." + r.Tag
}

func Ok(tag string) Result    { return Result{Category: OK, Tag: tag} }
func Err(tag string) Result   { return Result{Category: ERR, Tag: tag} }
func Would(tag string) Result { return Result{Category: WOULD, Tag: tag} }

// ErrShell attaches a ShellResult payload to an error outcome.
func ErrShell(tag string, s ShellResult) Result {
	return Result{Category: ERR, Tag: tag, Shell: &s}
}
