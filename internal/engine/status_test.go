package engine

import "testing"

// spyInstruction records which phases the engine drove, so a test can assert on what was
// *called* rather than on what was returned. Apply's effect is the thing under test here,
// and a Result cannot tell you whether it ran.
type spyInstruction struct {
	preCheck, guard, apply, preview int
	guardSkips                      bool
}

func (s *spyInstruction) Name() string      { return "spy" }
func (s *spyInstruction) PreCheck() *Result { s.preCheck++; return nil }

func (s *spyInstruction) Guard(Executor) *Result {
	s.guard++
	if s.guardSkips {
		r := Ok("already")
		return &r
	}
	return nil
}

func (s *spyInstruction) Apply(Executor) Result { s.apply++; return Ok("written") }
func (s *spyInstruction) ChangedTag() string    { return "written" }
func (s *spyInstruction) Preview(Executor) *ShellResult {
	s.preview++
	return nil
}

// #338: `shellf status` reports observed state without acting (ADR-0013), and its usage
// line says so. It did not: Run handled Check and fell through to Apply, so every
// remaining Go instruction — shell, file.put, file.copy — ran for real. Against a fresh
// target the harness showed `file.put(...) ok.written` and `shell(echo … > …) ok.ran`
// during `shellf status`, before any apply.
//
// The invariant is the same one Check has, and it is asserted on the call, not the
// result: a Result cannot say whether the target was written.
func TestRun_StatusNeverApplies(t *testing.T) {
	for _, drift := range []bool{true, false} {
		spy := &spyInstruction{guardSkips: !drift}
		Run(spy, ShellExecutor{}, Status)
		if spy.apply != 0 {
			t.Fatalf("status must never apply (drift=%v): Apply called %d time(s)", drift, spy.apply)
		}
	}
}

// Guard is read-only and is what lets status tell converged from drifted, so it must
// still run — the fix must not be "return early and observe nothing".
func TestRun_StatusStillDecides(t *testing.T) {
	spy := &spyInstruction{guardSkips: true}
	r := Run(spy, ShellExecutor{}, Status)
	if spy.guard == 0 {
		t.Fatal("status must still run Guard: it is how a converged resource is recognised")
	}
	if r.Category != OK || r.Tag != "already" {
		t.Fatalf("a converged resource must report its guard outcome, got %s", r)
	}
}

// The Check invariant that was already documented, asserted rather than trusted — it is
// the sibling of the one above and nothing tested it either.
func TestRun_CheckNeverApplies(t *testing.T) {
	spy := &spyInstruction{}
	Run(spy, ShellExecutor{}, Check)
	if spy.apply != 0 {
		t.Fatalf("check must never apply: Apply called %d time(s)", spy.apply)
	}
}
