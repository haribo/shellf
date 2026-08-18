// Package inventory holds the hosts and groups (the orchestration plane's
// targets). Connection coordinates only — no business variables. Built in Go
// for now; a parsed shellf-language inventory comes later.
package inventory

import (
	"fmt"
	"sort"
)

// Host is connection coordinates under a logical alias.
type Host struct {
	Address     string
	User        string
	Port        string
	Key         string
	Interpreter string            // shell interpreter for unannotated shells (ADR-0012)
	Local       bool              // reached by the local transport, not SSH (ADR-0027)
	Vars        map[string]string // free-form per-host variables
}

// Inventory maps aliases to hosts and names groups of aliases. Defaults fill
// fields a host omits (two-level precedence: host over defaults).
type Inventory struct {
	Defaults Host
	Hosts    map[string]Host
	Groups   map[string][]string
}

// Resolve returns the host for an alias with defaults applied.
func (inv Inventory) Resolve(alias string) (Host, bool) {
	h, ok := inv.Hosts[alias]
	if !ok {
		return Host{}, false
	}
	if h.User == "" {
		h.User = inv.Defaults.User
	}
	if h.Port == "" {
		h.Port = inv.Defaults.Port
	}
	if h.Key == "" {
		h.Key = inv.Defaults.Key
	}
	if h.Interpreter == "" {
		h.Interpreter = inv.Defaults.Interpreter
	}
	if !h.Local {
		h.Local = inv.Defaults.Local
	}
	// Merge vars: defaults first, host overrides.
	merged := map[string]string{}
	for k, v := range inv.Defaults.Vars {
		merged[k] = v
	}
	for k, v := range h.Vars {
		merged[k] = v
	}
	h.Vars = merged
	return h, true
}

// Members expands a target (group name, or a single host alias) to aliases, and
// reports whether the target is declared at all.
//
// The second return is not a convenience: without it a caller cannot tell a group
// declared empty from a name nobody declared, and both arrive as an empty slice. The
// orchestrator read that as "no host to run on" and exited 0, so a typo in a group
// name was a deployment that never happened, reported green (#451). Returning the
// answer alongside the members is what makes the question impossible to skip.
func (inv Inventory) Members(target string) (aliases []string, known bool) {
	if g, ok := inv.Groups[target]; ok {
		return g, true
	}
	if _, ok := inv.Hosts[target]; ok {
		return []string{target}, true // a host is a singleton group
	}
	return nil, false
}

// Validate reports the first structural fault in the inventory.
//
// Today that is a group listing an alias no `host` declares. Left through, that alias
// reached the transport with an empty address and failed as
// `host key for :22 not in known_hosts` — an inventory typo reported as an SSH
// problem, on a connection that should never have been attempted (#451).
func (inv Inventory) Validate() error {
	groups := make([]string, 0, len(inv.Groups))
	for g := range inv.Groups {
		groups = append(groups, g)
	}
	sort.Strings(groups) // a map walk would report a different fault each run
	for _, g := range groups {
		for _, alias := range inv.Groups[g] {
			if _, ok := inv.Hosts[alias]; !ok {
				return fmt.Errorf("group %q lists %q, which no host declares", g, alias)
			}
		}
	}
	return nil
}
