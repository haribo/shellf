package lang

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"shellf/internal/engine"
)

// evalCheck runs a def in check mode with a controllable transfer, which is what
// `dir.copy` and `dir.sync` are made of. `n` is how many files the transfer says it would
// write; `extras` what it would delete.
func evalCheck(t *testing.T, src, entry string, args map[string]string, n int, extras []string, ex engine.Executor) engine.Result {
	t.Helper()
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	resolve := func(s string) (Def, bool) { d, ok := byName[s]; return d, ok }
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return []byte("body"), nil }
	sync := func(string, string, string, bool) (int, int, error) {
		t.Error("check mode must not transfer")
		return 0, 0, nil
	}
	preview := func(string, string, string) (int, []string, error) { return n, extras, nil }
	res, err := EvalDefFull(byName[entry], args, nil, []string{"s"}, ex, engine.Check, resolve,
		[]string{entry}, fetch, sync, preview)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

const copyLike = `def d(s: str, dst: str) {
	apply {
		n = ~dir.sync(s, dst, "false", "meta")
		if n == "0" { return ok.already }
		return ok.copied
	}
}`

// ADR-0041 §1: on a converged host the apply is evaluated, finds that nothing would
// change, and says so — instead of announcing a write that would not happen (#380).
func TestInert_ConvergedReportsAlready(t *testing.T) {
	res := evalCheck(t, copyLike, "d", map[string]string{"s": "tree", "dst": "/opt/x"}, 0, nil, noopExec{})
	if res.Category != engine.OK || res.Tag != "already" {
		t.Fatalf("a converged transfer must preview as ok.already, got %s.%s", res.Category, res.Tag)
	}
	if res.Changed {
		t.Error("nothing would change, so the result must not be marked changed")
	}
}

func TestInert_DriftStillReportsWould(t *testing.T) {
	res := evalCheck(t, copyLike, "d", map[string]string{"s": "tree", "dst": "/opt/x"}, 3, nil, noopExec{})
	if res.Category != engine.WOULD {
		t.Fatalf("drift must preview as would, got %s.%s", res.Category, res.Tag)
	}
	if res.Tag != "copied" {
		t.Errorf("the tag must come from the return the apply reached, got %q", res.Tag)
	}
	if !res.Changed {
		t.Error("a would-act result must be marked changed")
	}
}

// A deletion is a change even when no file is written — the case that makes `dir.sync`
// different from `dir.copy`, and the one an operator most needs previewed.
func TestInert_DeletionAloneIsDrift(t *testing.T) {
	src := strings.Replace(copyLike, `"false"`, `"true"`, 1)
	res := evalCheck(t, src, "d", map[string]string{"s": "tree", "dst": "/opt/x"}, 0, []string{"stale.txt"}, noopExec{})
	if res.Category != engine.WOULD {
		t.Fatalf("a transfer that would delete must preview as would, got %s.%s", res.Category, res.Tag)
	}
}

// ADR-0041 §2: a `shell { }` can do anything, so an apply holding one is not evaluated in
// check mode and keeps the conservative verdict. Silently — the def is written correctly,
// it merely costs dry-run precision.
func TestInert_ShellInApplyFallsBackToWould(t *testing.T) {
	src := `def d(s: str, dst: str) {
		apply {
			n = ~dir.sync(s, dst, "false", "meta")
			shell { logger delivered }
			return ok.copied
		}
	}`
	res := evalCheck(t, src, "d", map[string]string{"s": "tree", "dst": "/opt/x"}, 0, nil, noopExec{})
	if res.Category != engine.WOULD || res.Tag != "copied" {
		t.Fatalf("an apply with a shell must keep would.<tag>, got %s.%s", res.Category, res.Tag)
	}
}

// A def call is disqualifying rather than recursed into: resolving it needs the def table,
// and a callee with a shell three levels down would make the answer wrong.
func TestInert_DefCallInApplyDisqualifies(t *testing.T) {
	src := `def helper(p: str) { apply { shell { touch "$p" } return ok.done } }
def d(s: str, dst: str) {
	apply {
		helper(dst)
		return ok.copied
	}
}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name != "d" {
			continue
		}
		if _, ok := inertApply(def); ok {
			t.Fatal("an apply calling another def must not be evaluated in check mode")
		}
	}
}

// A def that declares an `observe` is untouched: convergence is already decided there, and
// re-deciding it in the apply is the duplication #378 was lost over.
func TestInert_ObserveStillDecides(t *testing.T) {
	src := `def d(s: str, dst: str) {
		observe { return state(present: shell { test -d "$dst" }.exit == 0) }
		apply {
			n = ~dir.sync(s, dst, "false", "meta")
			return ok.copied
		}
	}`
	defs, _ := ParseDefs(src)
	if _, ok := inertApply(defs[0]); ok {
		t.Fatal("a def with an observe must not have its apply evaluated in check mode")
	}
}

// The same decision for the one-file primitive: `~file.write` guards on content sha256, so
// check mode already knows. This executor answers as a target holding those exact bytes.
type shaExec struct{ sum string }

func (e shaExec) Shell(cmd string, _ engine.Env) engine.ShellResult {
	if strings.Contains(cmd, "sha256sum") {
		return engine.ShellResult{Stdout: e.sum + "\n"}
	}
	return engine.ShellResult{}
}
func (e shaExec) As(string) engine.Executor    { return e }
func (e shaExec) Using(string) engine.Executor { return e }

func TestInert_FileWriteConvergedReportsAlready(t *testing.T) {
	sum := sha256.Sum256([]byte("body"))
	src := `def d(s: str, dst: str) {
		apply {
			n = ~file.write(dst, ~file.read(s))
			if n == "0" { return ok.already }
			return ok.copied
		}
	}`
	args := map[string]string{"s": "conf.txt", "dst": "/opt/conf"}

	res := evalCheck(t, src, "d", args, 0, nil, shaExec{sum: hex.EncodeToString(sum[:])})
	if res.Category != engine.OK || res.Tag != "already" {
		t.Fatalf("a destination already holding the bytes must preview as already, got %s.%s", res.Category, res.Tag)
	}
	// …and a destination holding anything else is drift.
	res = evalCheck(t, src, "d", args, 0, nil, shaExec{sum: "0000"})
	if res.Category != engine.WOULD || res.Tag != "copied" {
		t.Fatalf("a differing destination must preview as would.copied, got %s.%s", res.Category, res.Tag)
	}
}
