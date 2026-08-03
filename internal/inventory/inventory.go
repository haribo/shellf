// Package inventory holds the hosts and groups (the orchestration plane's
// targets). Connection coordinates only — no business variables. Built in Go
// for now; a parsed shellf-language inventory comes later.
package inventory

// Host is connection coordinates under a logical alias.
type Host struct {
	Address     string
	User        string
	Port        string
	Key         string
	Interpreter string            // shell interpreter for unannotated shells (ADR-0012)
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

// Members expands a target (group name, or a single host alias) to aliases.
func (inv Inventory) Members(target string) []string {
	if g, ok := inv.Groups[target]; ok {
		return g
	}
	if _, ok := inv.Hosts[target]; ok {
		return []string{target} // a host is a singleton group
	}
	return nil
}
