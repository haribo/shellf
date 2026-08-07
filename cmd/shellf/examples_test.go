package main

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// Regression for #256 (guards #255): every example must load through the real
// package path — a plan's directory is its def package (ADR-0014), so a stray
// inventory or a renamed stdlib def turns this red. Zero network.
func TestExamplesParse(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	var plans, invs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch d.Name() {
		case "plan.shellf":
			plans = append(plans, p)
		case "inventory.shellf":
			invs = append(invs, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) == 0 {
		t.Fatal("no example plans found under examples/")
	}
	// Each plan loads as a package via the production loader (no inventory skip:
	// the package dir must be defs-only).
	for _, p := range plans {
		if _, _, err := loadPlanPackage(p, "", map[string]string{}, map[string]string{}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// Each example inventory parses.
	for _, iv := range invs {
		if _, err := loadInventory(iv); err != nil {
			t.Errorf("%s: %v", iv, err)
		}
	}
}
