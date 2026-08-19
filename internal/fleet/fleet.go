// Package fleet is the orchestration plane: run the same request on many
// targets concurrently and collect one result per host. Inter-host parallelism
// is conflict-free — each target is independent.
package fleet

import (
	"encoding/json"
	"sync"

	"shellf/internal/proto"
	"shellf/internal/transport"
)

// defaultConcurrent caps in-flight SSH sessions so a large fleet does not spawn thousands
// of processes at once. It is a default, not a law: the right number depends on the
// control host and the link, neither of which shellf can see (#462).
const defaultConcurrent = 16

// width resolves the requested fan-out. Anything at or below zero means "unset" and takes
// the default — never "unlimited": an empty flag value must not turn into a fork bomb
// against somebody's fleet.
func width(requested int) int {
	if requested <= 0 {
		return defaultConcurrent
	}
	return requested
}

// Dial builds a transport for one target (host varies; port/key are shared).
type Dial func(target string) transport.Transport

// HostResult is the outcome for a single target.
type HostResult struct {
	Target   string
	Response proto.Response
	Err      error // transport/decode failure (host unreachable, etc.)
}

// Run fans out over targets and returns results in the same order. reqFor builds the
// per-host Request (it may differ per host once variables are resolved per host); a reqFor
// error marks that host failed without dialing it. parallel caps how many run at once; 0
// takes the default.
func Run(targets []string, agentBin string, reqFor func(target string) ([]byte, error), dial Dial, parallel int) []HostResult {
	results := make([]HostResult, len(targets))
	sem := make(chan struct{}, width(parallel))
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			hr := HostResult{Target: t}
			req, err := reqFor(t)
			if err != nil {
				hr.Err = err
				results[i] = hr
				return
			}
			raw, err := dial(t).Run(agentBin, req)
			if err != nil {
				hr.Err = err
			} else if err := json.Unmarshal(raw, &hr.Response); err != nil {
				hr.Err = err
			}
			results[i] = hr // distinct index per goroutine — no race
		}(i, t)
	}

	wg.Wait()
	return results
}
