package engine

// Service is the third builtin: a multi-dimension resource. Two orthogonal
// desired states — running now, and enabled at boot — checked and converged
// together. Both are parameters (the guard is "current == desired", not
// "present?").
type Service struct {
	Unit    string
	Running bool
	Enabled bool
}

func (s Service) Name() string       { return "service" }
func (s Service) ChangedTag() string { return "converged" }

func (s Service) PreCheck() *Result {
	if s.Unit == "" {
		r := Err("unitMustNotBeEmpty")
		return &r
	}
	return nil
}

// Guard: both dimensions already in the desired state → skip.
func (s Service) Guard(ex Executor) *Result {
	active := ex.Shell(`systemctl is-active --quiet "$unit"`, s.env()).OK()
	enabled := ex.Shell(`systemctl is-enabled --quiet "$unit"`, s.env()).OK()
	if active == s.Running && enabled == s.Enabled {
		r := Ok("alreadyConverged")
		return &r
	}
	return nil
}

func (s Service) Apply(ex Executor) Result {
	env := s.env()
	env["act"] = boolCmd(s.Running, "start", "stop")
	env["en"] = boolCmd(s.Enabled, "enable", "disable")
	// systemctl is idempotent per verb, so applying both unconditionally is safe.
	r := ex.Shell(`systemctl "$act" "$unit" && systemctl "$en" "$unit"`, env)
	if !r.OK() {
		return ErrShell("runtime", r)
	}
	return Ok(s.ChangedTag())
}

func (s Service) Preview(ex Executor) *ShellResult { return nil }

func (s Service) env() Env { return Env{"unit": s.Unit} }

func boolCmd(want bool, yes, no string) string {
	if want {
		return yes
	}
	return no
}
