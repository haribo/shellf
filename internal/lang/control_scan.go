package lang

import (
	"sort"

	"shellf/internal/proto"
)

// ControlResources lists what a plan will ask the control host for (ADR-0034 §5).
//
// The occurrences are syntactic, which is what makes ADR-0031's allow-list possible: the
// control host derives the set before sending, and refuses anything outside it. A `%`
// path built from a value the target produces is therefore not resolvable here — and is
// refused when the plan is read rather than mid-deploy.
//
// The plan's steps are the only source. Def bodies used to be scanned too, which let a
// def — an imported one included — add to the list that bounds it; a `%"…"` in a def is
// now a parse error, so there is nothing left there to find (#403, ADR-0043).
func ControlResources(steps []proto.Step) []string {
	seen := map[string]bool{}
	scanSteps(steps, seen)
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// scanSteps collects the `%"path"` arguments a plan wrote. This is the case that
// matters in practice: `file.template(%"conf.j2", dst)` puts the path at the call site,
// while the def reading it only sees a parameter — so scanning defs alone would miss it
// and the request would be refused at runtime for a file the plan legitimately needs.
//
// Every read primitive is allowed for such a path: the plan says "this is mine to serve",
// and which one reads it is the def's business — `file.template` reads it, `dir.copy`
// syncs it, and both are the same declaration seen from the plan.
func scanSteps(steps []proto.Step, seen map[string]bool) {
	for _, s := range steps {
		for _, arg := range s.Control {
			if p, ok := s.Args[arg]; ok {
				seen[resourceKey("file.read", p)] = true
				seen[resourceKey("file.render", p)] = true
				seen[resourceKey("dir.list", p)] = true
				seen[resourceKey("dir.sync", p)] = true
			}
		}
		scanSteps(s.Block, seen)
		scanSteps(s.Parallel, seen)
		if s.If != nil {
			if s.If.Cond != nil {
				scanSteps([]proto.Step{*s.If.Cond}, seen)
			}
			scanSteps(s.If.Then, seen)
			scanSteps(s.If.Else, seen)
		}
	}
}
