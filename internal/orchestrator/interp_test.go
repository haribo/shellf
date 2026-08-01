package orchestrator

import (
	"testing"

	"shellf/internal/inventory"
	"shellf/internal/proto"
)

func TestInterpAgreement(t *testing.T) {
	inv := inventory.Inventory{Hosts: map[string]inventory.Host{
		"a": {Interpreter: "bash"},
		"b": {Interpreter: "sh"},
		"c": {Interpreter: "bash"},
	}}
	unannotated := Block{Steps: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "x"}}}}
	annotated := Block{Steps: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "x"}, Interp: "bash"}}}

	// Diverging interpreters + an unannotated shell → pre-flight error.
	if err := interpAgreement(inv, []string{"a", "b"}, unannotated); err == nil {
		t.Fatal("bash vs sh with an unannotated shell must error")
	}
	// Same interpreter → fine.
	if err := interpAgreement(inv, []string{"a", "c"}, unannotated); err != nil {
		t.Fatalf("same interpreter should agree: %v", err)
	}
	// An annotated shell is uniform by construction → fine even if hosts diverge.
	if err := interpAgreement(inv, []string{"a", "b"}, annotated); err != nil {
		t.Fatalf("annotated block must not require agreement: %v", err)
	}
}

func TestHasUnannotatedShell(t *testing.T) {
	if !hasUnannotatedShell([]proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "x"}}}) {
		t.Fatal("a bare shell is unannotated")
	}
	if hasUnannotatedShell([]proto.Step{{Instruction: "shell", Interp: "bash"}}) {
		t.Fatal("an annotated shell is not unannotated")
	}
	// nested inside an if/block still counts
	nested := []proto.Step{{Block: []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "x"}}}}}
	if !hasUnannotatedShell(nested) {
		t.Fatal("a nested unannotated shell must be found")
	}
	// a plan with no shell at all
	if hasUnannotatedShell([]proto.Step{{Instruction: "apt.install", Args: map[string]string{"pkg": "nginx"}}}) {
		t.Fatal("no shell → false")
	}
}
