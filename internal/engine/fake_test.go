package engine

// The shared fake executor for this package's tests: a lookup table, so a test states the
// exact script it expects and gets a deterministic answer.
//
// It lived in filecopy_test.go until #445 deleted that file with the unreachable
// instruction it tested. It is here rather than in one of the remaining test files because
// it belongs to none of them in particular.
type fcFake struct {
	responses map[string]ShellResult
	calls     map[string]bool
}

func (f *fcFake) As(string) Executor    { return f }
func (f *fcFake) Using(string) Executor { return f }

func (f *fcFake) Shell(script string, _ Env) ShellResult {
	if f.calls == nil {
		f.calls = map[string]bool{}
	}
	f.calls[script] = true
	if r, ok := f.responses[script]; ok {
		return r
	}
	return ShellResult{Exit: 2}
}
