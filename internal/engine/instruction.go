package engine

// Mode selects whether the engine actually acts (Apply) or only decides (Check).
type Mode int

const (
	Apply Mode = iota
	Check
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
// Invariant: in Check mode, Apply is NEVER called (the read-only contract).
func Run(inst Instruction, ex Executor, mode Mode) Result {
	if r := inst.PreCheck(); r != nil {
		return *r
	}
	if skip := inst.Guard(ex); skip != nil {
		return *skip // identical in both modes
	}
	if mode == Check {
		res := Would(inst.ChangedTag()) // derived, never authored
		if p := inst.Preview(ex); p != nil {
			res.Shell = p // e.g. the diff a file-copy would apply
		}
		return res
	}
	return inst.Apply(ex)
}
