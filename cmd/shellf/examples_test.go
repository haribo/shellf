package main

import (
	"path/filepath"
	"testing"

	"shellf/internal/proto"
)

// Regression for #256 (guards #255): every example must load through the real package
// path. Since #355 it must also **resolve for every host it targets**: loading proved
// nothing about whether the example works, and `examples/plans/blog.shellf` shipped
// calling `ufw.open(port, "tcp")` — the bare form of a loop variable, which
// docs/language.md forbids — because nothing ever went further than parsing it.
//
// Reference resolution is where that failed (`undefined variable "port"`), it is what a
// real run does first, and it needs no executor, no channel and no host.
func TestExamplesResolvePerHost(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	plans, err := filepath.Glob(filepath.Join(root, "plans", "*.shellf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) == 0 {
		t.Fatal("no example plans found under examples/plans/")
	}
	invs, err := filepath.Glob(filepath.Join(root, "inventories", "*.shellf"))
	if err != nil || len(invs) == 0 {
		t.Fatalf("no example inventory found under examples/inventories/: %v", err)
	}

	for _, p := range plans {
		t.Run(filepath.Base(p), func(t *testing.T) {
			base := map[string]string{}
			plan, _, err := loadPlanPackage(p, invs[0], base, map[string]string{})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			inv, err := loadInventory(invs[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, blk := range plan {
				hosts, _ := inv.Members(blk.Target)
				if len(hosts) == 0 {
					t.Fatalf("target %q resolves to no host in the example inventory", blk.Target)
				}
				for _, h := range hosts {
					host, _ := inv.Resolve(h)
					env := mergeVars(base, host.Vars, map[string]string{})
					if _, err := proto.ResolveRefs(blk.Steps, env, host.Interpreter); err != nil {
						t.Errorf("%s on %s: %v", filepath.Base(p), h, err)
					}
				}
			}
		})
	}

	for _, iv := range invs {
		if _, err := loadInventory(iv); err != nil {
			t.Errorf("%s: %v", iv, err)
		}
	}
}
