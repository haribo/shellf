package agent

import (
	"path/filepath"
	"testing"
)

// #393: `filepath.Join(dst, rel)` *cleans* what it joins, so a manifest entry containing
// `..` produces a path outside the destination rather than an error. The control host
// composes these paths and is the trusted party, so this is not reachable today — which is
// exactly why it is asserted here rather than left to the day it becomes reachable.
func TestUnderDst(t *testing.T) {
	dst := filepath.FromSlash("/srv/site")
	for rel, want := range map[string]bool{
		"index.html":          true,
		"css/app.css":         true,
		"a/../b.txt":          true, // cleans to a sibling inside — legitimate
		"../../etc/passwd":    false,
		"../site-evil/x":      false, // a prefix of the destination is not the destination
		"..":                  false,
		"nested/../../../out": false,
		"ok/../ok2/../deep/x": true,
		"./fine.txt":          true,
	} {
		final := filepath.Join(dst, filepath.FromSlash(rel))
		if got := underDst(dst, final); got != want {
			t.Errorf("%q → %s: got %v, want %v", rel, final, got, want)
		}
	}
}
