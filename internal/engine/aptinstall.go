package engine

// AptInstall is the first instruction, hardcoded in Go (not parsed).
// It mirrors the language form: guard (read-only) then apply.
type AptInstall struct {
	Pkg string
}

func (a AptInstall) Name() string       { return "apt-install" }
func (a AptInstall) ChangedTag() string { return "installed" }

func (a AptInstall) PreCheck() *Result {
	if a.Pkg == "" {
		r := Err("pkgMustNotBeNull")
		return &r
	}
	return nil
}

func (a AptInstall) Guard(ex Executor) *Result {
	r := ex.Shell(`dpkg -s "$pkg"`, Env{"pkg": a.Pkg})
	if r.OK() {
		res := Ok("alreadyInstalled")
		return &res
	}
	return nil
}

func (a AptInstall) Apply(ex Executor) Result {
	r := ex.Shell(`apt-get install -y "$pkg"`, Env{"pkg": a.Pkg})
	if !r.OK() {
		return ErrShell("runtime", r)
	}
	return Ok(a.ChangedTag())
}

// Preview: a binary install has nothing to diff — would.installed carries no payload.
func (a AptInstall) Preview(ex Executor) *ShellResult { return nil }
