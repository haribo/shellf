package engine

// Mode selects whether the engine acts (Apply), only decides (Check), or only
// reports observed vs desired state without acting (Status, ADR-0013).
type Mode int

const (
	Apply Mode = iota
	Check
	Status
)

// Instruction is phase-structured. Separability — guard (read-only) distinct
// from apply (the only effectful phase) — is what makes Check mode possible.
type Instruction interface {
	Name() string
	// PreCheck: pure/local validation. Non-nil = halt with this Result.
	PreCheck() *Result
	// Guard: read-only. Non-nil = state already reached (skip, ok.already…).
	Guard(ex Executor) *Result
	// Apply: the only effectful phase.
	Apply(ex Executor) Result
	// ChangedTag: the subject ("installed"). The engine derives ok/would/already
	// from it, so the author never writes `would`.
	ChangedTag() string
	// Preview: read-only description of what Apply would change, for Check mode.
	// nil when there is nothing to show (binary install/skip). file-copy returns
	// the diff here.
	Preview(ex Executor) *ShellResult
}

// Run drives an instruction through the engine.
//
// Invariant: in Check **and Status** mode, Apply is NEVER called (the read-only
// contract). Status was missing from that list and fell through to Apply, so
// `shellf status` — documented as reporting state without acting (ADR-0013) — wrote
// files and ran shells on the target (#338). Both modes are named here because the
// invariant was stated for one and enforced for neither.
func Run(inst Instruction, ex Executor, mode Mode) Result {
	if r := inst.PreCheck(); r != nil {
		return *r
	}
	if skip := inst.Guard(ex); skip != nil {
		return *skip // identical in both modes
	}
	if mode == Check || mode == Status {
		res := Would(inst.ChangedTag()) // derived, never authored
		res.Changed = true              // it would act
		// Preview is a `--dry-run` concern (ADR-0029): it describes an action the
		// operator asked to see. `status` asks a different question — is this in sync —
		// and the guard above already answered it.
		if mode == Check {
			if p := inst.Preview(ex); p != nil {
				res.Shell = p // e.g. the diff a file-copy would apply
			}
		}
		return res
	}
	r := inst.Apply(ex)
	if r.Category == OK {
		r.Changed = true // apply ran and succeeded
	}
	return r
}
