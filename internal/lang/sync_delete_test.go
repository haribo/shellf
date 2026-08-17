package lang

import (
	"testing"

	"shellf/internal/engine"
)

// Regression test for #387: a transfer that only *removes* files reported `ok.already`.
// The count the primitive returned was files **written**, so a delete-only run answered
// "0", and `dir.sync`'s apply concluded there was nothing to do — over a target it had
// just deleted from. A destructive action announced as a no-op.
//
// Keep this: it guards the delete-only path, which no fixed asset can exercise. The e2e
// coverage plan creates its extra file and delivers a tree in the same call, so `written`
// is never zero there and this case is invisible to it.
func TestSync_DeleteOnlyIsNotAlready(t *testing.T) {
	src := `def sync(s: str, dst: str) {
		apply {
			n = ~dir.sync(s, dst, "true", "meta")
			if n == "0" { return ok.already }
			return ok.synced
		}
	}`
	defs, err := ParseDefs(src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Def{"sync": defs[0]}
	resolve := func(s string) (Def, bool) { d, ok := byName[s]; return d, ok }
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return nil, nil }

	// The tree is converged — nothing to write — and one file on the target is not in the
	// source, so the transfer removes it and writes nothing.
	sync := func(engine.Executor, string, string, string, bool) (int, int, error) { return 0, 1, nil }
	preview := func(engine.Executor, string, string, string) (int, []string, error) { return 0, nil, nil }

	res, err := EvalDefFull(byName["sync"], map[string]string{"s": "tree", "dst": "/opt/x"},
		nil, []string{"s"}, noopExec{}, engine.Apply, resolve, []string{"sync"}, fetch, sync, preview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tag == "already" {
		t.Fatal("a transfer that removed a file must not report already (#387)")
	}
	if !res.Changed {
		t.Error("a run that removed a file has changed the host")
	}
}

// The other half, which the fix must not cost: a genuinely converged transfer — nothing
// written, nothing removed — still reports `already` and is not marked changed.
func TestSync_ConvergedStillReportsAlready(t *testing.T) {
	src := `def sync(s: str, dst: str) {
		apply {
			n = ~dir.sync(s, dst, "true", "meta")
			if n == "0" { return ok.already }
			return ok.synced
		}
	}`
	defs, _ := ParseDefs(src)
	byName := map[string]Def{"sync": defs[0]}
	resolve := func(s string) (Def, bool) { d, ok := byName[s]; return d, ok }
	fetch := func(string, []byte, map[string]string) ([]byte, error) { return nil, nil }
	sync := func(engine.Executor, string, string, string, bool) (int, int, error) { return 0, 0, nil }
	preview := func(engine.Executor, string, string, string) (int, []string, error) { return 0, nil, nil }

	res, err := EvalDefFull(byName["sync"], map[string]string{"s": "tree", "dst": "/opt/x"},
		nil, []string{"s"}, noopExec{}, engine.Apply, resolve, []string{"sync"}, fetch, sync, preview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tag != "already" {
		t.Fatalf("a converged transfer must report already, got %q", res.Tag)
	}
	if res.Changed {
		t.Error("a converged transfer changed nothing")
	}
}
