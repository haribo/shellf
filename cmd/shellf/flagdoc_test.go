package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// Every flag the CLI accepts is documented in `README.md` (#646).
//
// #642 found four — `--json`, `-v`, `--parallel`, `--limit` — accepted by `run` and named in
// no document. They were not new: they had been shipping undocumented, and nothing would ever
// have said so. The check that found them was a throwaway written during that fix; this is it,
// kept, which is what should have shipped then.
//
// It walks the commands' own flag sets rather than parsing `main.go`: `runFlags`,
// `statusFlags` and `cleanFlags` are what the commands themselves call, so a flag cannot be
// added to one and missed by the other.
func TestEveryFlagIsDocumented(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(readme)

	// `--check` is the one exception, by name and with its reason: it is registered with an
	// empty usage string so it stays out of `-h`, and exists only to tell an operator the
	// flag is now `--dry-run` (ADR-0035). A documented `--check` would be the defect.
	const undocumentedOnPurpose = "check"

	runFS, _, _, _ := runFlags()
	statusFS, _ := statusFlags()
	cleanFS, _, _, _ := cleanFlags()

	for _, c := range []struct {
		cmd string
		fs  *flag.FlagSet
	}{{"run", runFS}, {"status", statusFS}, {"clean", cleanFS}} {
		c.fs.VisitAll(func(f *flag.Flag) {
			if f.Name == undocumentedOnPurpose {
				if f.Usage != "" {
					t.Errorf("--%s carries a usage string now, so it shows in -h: "+
						"either document it or keep it hidden", f.Name)
				}
				return
			}
			// The README writes a flag in backticks, either `--name` (with or without an
			// argument after it) or `-v` for the single-letter one.
			if strings.Contains(doc, "`--"+f.Name) || strings.Contains(doc, "`-"+f.Name+"`") {
				return
			}
			t.Errorf("shellf %s accepts -%s and README.md does not mention it", c.cmd, f.Name)
		})
	}
}

// The exemption is one name, not a habit: if a second hidden flag appears, this fails and the
// decision gets made deliberately rather than by adding a line to a skip list.
func TestOnlyOneFlagIsHidden(t *testing.T) {
	hidden := []string{}
	runFS, _, _, _ := runFlags()
	statusFS, _ := statusFlags()
	cleanFS, _, _, _ := cleanFlags()
	for _, fs := range []*flag.FlagSet{runFS, statusFS, cleanFS} {
		fs.VisitAll(func(f *flag.Flag) {
			if f.Usage == "" {
				hidden = append(hidden, f.Name)
			}
		})
	}
	if len(hidden) != 1 || hidden[0] != "check" {
		t.Fatalf("hidden flags: got %v, want [check] — a new one needs a decision, not a skip", hidden)
	}
}
