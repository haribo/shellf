package engine

import "testing"

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

const (
	cmpScript  = `cmp -s "$src" "$dst"`
	diffScript = `diff -u "$dst" "$src" 2>&1 || true`
	cpScript   = `cp "$src" "$dst"`
)

func TestFileCopy_ContentsMatch_Skips(t *testing.T) {
	f := &fcFake{responses: map[string]ShellResult{cmpScript: {Exit: 0}}}
	if got := Run(FileCopy{Src: "a", Dst: "b"}, f, Apply).String(); got != "ok.alreadyCopied" {
		t.Fatalf("got %s, want ok.alreadyCopied", got)
	}
	if f.calls[cpScript] {
		t.Fatal("apply ran despite matching contents")
	}
}

func TestFileCopy_Check_WouldCarriesDiff(t *testing.T) {
	f := &fcFake{responses: map[string]ShellResult{
		cmpScript:  {Exit: 1}, // differ
		diffScript: {Exit: 0, Stdout: "-old\n+new\n"},
	}}
	res := Run(FileCopy{Src: "a", Dst: "b"}, f, Check)
	if res.String() != "would.copied" {
		t.Fatalf("got %s, want would.copied", res)
	}
	if res.Shell == nil || res.Shell.Stdout == "" {
		t.Fatal("would.copied must carry the diff payload")
	}
	if f.calls[cpScript] {
		t.Fatal("check mode wrote the file")
	}
}

func TestFileCopy_Apply_Copies(t *testing.T) {
	f := &fcFake{responses: map[string]ShellResult{
		cmpScript: {Exit: 1},
		cpScript:  {Exit: 0},
	}}
	if got := Run(FileCopy{Src: "a", Dst: "b"}, f, Apply).String(); got != "ok.copied" {
		t.Fatalf("got %s, want ok.copied", got)
	}
}
