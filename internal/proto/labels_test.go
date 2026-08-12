package proto

import "testing"

func TestResultRef_PatternAndLabel(t *testing.T) {
	cases := []struct {
		ref            ResultRef
		pattern, label string
	}{
		{ResultRef{Name: "s", Category: "err", Tag: "dbLocked"}, "err.dbLocked", "s == err.dbLocked"},
		{ResultRef{Name: "s", Category: "ok"}, "ok", "s == ok"}, // no tag → category only
		{ResultRef{Name: "s", Changed: true}, "", "s.changed"},  // changed shortcut ignores pattern
	}
	for _, c := range cases {
		if !c.ref.Changed {
			if got := c.ref.Pattern(); got != c.pattern {
				t.Errorf("Pattern(%+v) = %q, want %q", c.ref, got, c.pattern)
			}
		}
		if got := c.ref.Label(); got != c.label {
			t.Errorf("Label(%+v) = %q, want %q", c.ref, got, c.label)
		}
	}
}

func TestIfBlock_CondLabel(t *testing.T) {
	// Captured-ref condition → the ref's label.
	refIf := &IfBlock{CondRef: &ResultRef{Name: "x", Category: "err", Tag: "runtime"}}
	if got := refIf.CondLabel(); got != "x == err.runtime" {
		t.Errorf("condRef label: %q", got)
	}
	// Inline instruction with a match pattern → `call() == pattern`.
	inlineIf := &IfBlock{
		Cond:  &Step{Instruction: "apt.install", Args: map[string]string{"pkg": "nginx"}},
		Match: &ResultRef{Category: "err", Tag: "runtime"},
	}
	if got := inlineIf.CondLabel(); got != "apt.install(pkg=nginx) == err.runtime" {
		t.Errorf("inline+match label: %q", got)
	}
	// Inline instruction, no match → bare instruction label.
	bare := &IfBlock{Cond: &Step{Instruction: "dir.exists", Args: map[string]string{"path": "/opt"}}}
	if got := bare.CondLabel(); got != "dir.exists(path=/opt)" {
		t.Errorf("bare inline label: %q", got)
	}
}

func TestStep_Label(t *testing.T) {
	// Args are sorted by key and joined.
	s := Step{Instruction: "service.ensure", Args: map[string]string{"name": "nginx", "enable": "true"}}
	if got := s.Label(); got != "service.ensure(enable=true, name=nginx)" { // sorted by key
		t.Errorf("instruction label: %q", got)
	}
	if got := (Step{Parallel: []Step{{}}}).Label(); got != "parallel" {
		t.Errorf("parallel label: %q", got)
	}
	if got := (Step{Instruction: "shell", Args: map[string]string{"cmd": "id"}}).Label(); got != "shell(id)" {
		t.Errorf("shell label: %q", got)
	}
	ifStep := Step{If: &IfBlock{CondRef: &ResultRef{Name: "s", Changed: true}}}
	if got := ifStep.Label(); got != "if(s.changed)" {
		t.Errorf("if label: %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  hello  "); got != "hello" { // trimmed
		t.Errorf("trim: %q", got)
	}
	if got := firstLine("line one\nline two"); got != "line one …" { // multi-line elided
		t.Errorf("multiline: %q", got)
	}
	long := "0123456789012345678901234567890123456789012345678901234567890" // 61 chars
	if got := firstLine(long); got != long[:50]+"…" {
		t.Errorf("long value must be truncated to 50 chars + ellipsis: %q", got)
	}
}

func TestResolveRefs_RecursesIntoBlockAndIf(t *testing.T) {
	env := map[string]string{"pkg": "nginx"}
	steps := []Step{
		{Become: "root", Block: []Step{
			{Instruction: "apt.install", Refs: map[string]string{"pkg": "pkg"}},
		}},
		{If: &IfBlock{
			Cond: &Step{Instruction: "dir.exists", Refs: map[string]string{"path": "pkg"}},
			Then: []Step{{Instruction: "shell", Args: map[string]string{"cmd": "echo hi"}}},
			Else: []Step{{Instruction: "apt.install", Refs: map[string]string{"pkg": "pkg"}}},
		}},
	}
	out, err := ResolveRefs(steps, env, "bash")
	if err != nil {
		t.Fatal(err)
	}
	// Block preserves Become and resolves its inner ref.
	if out[0].Become != "root" || out[0].Block[0].Args["pkg"] != "nginx" {
		t.Fatalf("block not resolved: %+v", out[0])
	}
	// If cond, then, else all resolved; the shell inside `then` inherits the interp.
	if out[1].If.Cond.Args["path"] != "nginx" || out[1].If.Else[0].Args["pkg"] != "nginx" {
		t.Fatalf("if branches not resolved: %+v", out[1].If)
	}
	if out[1].If.Then[0].Interp != "bash" {
		t.Fatalf("shell in then-branch should inherit host interp: %+v", out[1].If.Then[0])
	}
}

func TestResolveRefs_UndefinedInNestedBlock(t *testing.T) {
	steps := []Step{{Parallel: []Step{
		{Instruction: "apt.install", Refs: map[string]string{"pkg": "missing"}},
	}}}
	if _, err := ResolveRefs(steps, map[string]string{}, "sh"); err == nil {
		t.Fatal("an undefined ref inside a parallel block must error")
	}
}

// Regression for #258: args are labelled name=value, not bare values in sorted
// order — the latter reads as swapped, e.g. file.mode(755, /path).
func TestStep_Label_NameValue(t *testing.T) {
	s := Step{Instruction: "file.mode", Args: map[string]string{"path": "/opt/backup.sh", "mode": "755"}}
	if got := s.Label(); got != "file.mode(mode=755, path=/opt/backup.sh)" {
		t.Fatalf("label should be name=value (sorted by name): %q", got)
	}
}
