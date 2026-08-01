package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The end-to-end wiring of `runCmd`/`cleanCmd` (flag parsing → transport → exit
// code) is covered by the real-SSH harness in test/e2e/. These unit tests lock
// the risk-bearing pure logic: variable precedence and signature resolution.

func TestLoadGlobals_SetOnly(t *testing.T) {
	base, set, err := loadGlobals("", kvFlags{"pkg=nginx", "env=prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 0 {
		t.Fatalf("no --vars file → empty base, got %v", base)
	}
	if set["pkg"] != "nginx" || set["env"] != "prod" {
		t.Fatalf("--set not collected: %v", set)
	}
}

func TestLoadGlobals_VarsFileThenSet(t *testing.T) {
	dir := t.TempDir()
	vf := filepath.Join(dir, "vars.shellf")
	if err := os.WriteFile(vf, []byte("pkg = \"apache\"\nregion = \"eu\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, set, err := loadGlobals(vf, kvFlags{"pkg=nginx"})
	if err != nil {
		t.Fatal(err)
	}
	// The file feeds baseVars; --set stays separate (higher precedence, layered
	// later by the orchestrator). loadGlobals must not merge them.
	if base["pkg"] != "apache" || base["region"] != "eu" {
		t.Fatalf("vars file not parsed into base: %v", base)
	}
	if set["pkg"] != "nginx" {
		t.Fatalf("--set override not kept distinct: %v", set)
	}
}

func TestLoadGlobals_MalformedSet(t *testing.T) {
	for _, bad := range []string{"noequals", "=novalue"} {
		if _, _, err := loadGlobals("", kvFlags{bad}); err == nil {
			t.Fatalf("--set %q must error", bad)
		}
	}
}

func TestLoadGlobals_MissingVarsFile(t *testing.T) {
	if _, _, err := loadGlobals("/does/not/exist.shellf", nil); err == nil {
		t.Fatal("a missing --vars file must error")
	}
}

func TestStdSignatures(t *testing.T) {
	sig := stdSignatures()

	if params, ok := sig("file-copy"); !ok || len(params) != 2 || params[0] != "src" || params[1] != "dst" {
		t.Fatalf("builtin file-copy signature: %v ok=%v", params, ok)
	}
	// A stdlib def resolves its params from the embedded source (self-hosting).
	if params, ok := sig("dir-ensure"); !ok || len(params) != 1 || params[0] != "path" {
		t.Fatalf("stdlib dir-ensure signature: %v ok=%v", params, ok)
	}
	if _, ok := sig("no-such-instruction"); ok {
		t.Fatal("an unknown instruction must not resolve")
	}
}

func TestKVFlags(t *testing.T) {
	var k kvFlags
	if err := k.Set("a=1"); err != nil {
		t.Fatal(err)
	}
	_ = k.Set("b=2")
	if k.String() != "a=1,b=2" {
		t.Fatalf("String(): %q", k.String())
	}
}
