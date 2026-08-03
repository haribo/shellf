package engine

// FileCopy is the second builtin instruction. Its guard is by content (hash/diff),
// not presence — and its Check mode shows the diff it would apply.
type FileCopy struct {
	Src string
	Dst string
}

func (f FileCopy) Name() string       { return "file-copy" }
func (f FileCopy) ChangedTag() string { return "copied" }

func (f FileCopy) PreCheck() *Result {
	if f.Src == "" || f.Dst == "" {
		r := Err("pathMustNotBeEmpty")
		return &r
	}
	return nil
}

// Guard: contents already match → skip.
func (f FileCopy) Guard(ex Executor) *Result {
	r := ex.Shell(`cmp -s "$src" "$dst"`, f.env())
	if r.OK() {
		res := Ok("alreadyCopied")
		return &res
	}
	return nil
}

// Preview: the unified diff dst→src, for Check mode. Read-only.
func (f FileCopy) Preview(ex Executor) *ShellResult {
	r := ex.Shell(`diff -u "$dst" "$src" 2>&1 || true`, f.env())
	return &r
}

func (f FileCopy) Apply(ex Executor) Result {
	r := ex.Shell(`cp "$src" "$dst"`, f.env())
	if !r.OK() {
		return ErrShell("runtime", r)
	}
	return Ok(f.ChangedTag())
}

func (f FileCopy) env() Env { return Env{"src": f.Src, "dst": f.Dst} }
