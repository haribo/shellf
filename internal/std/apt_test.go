package std

import (
	"strings"
	"testing"

	"shellf/internal/engine"
)

// #486: the observe read `dpkg -s "$pkg"`, whose exit code is 0 for a package in state
// `rc` — removed without `--purge`, so its dpkg record and config files survive while its
// binaries are gone. The def then reported `already` on a host where the package was not
// installed, forever.
//
// Reproduced on debian:stable-slim before the fix:
//
//	apt-get install nano && apt-get remove nano
//	test -x /usr/bin/nano                -> absent
//	dpkg -s nano                         -> exit 0        (what the def observed)
//	dpkg-query -W -f='${Status}' nano    -> deinstall ok config-files
//
// The failure converges — both runs report `already` — so the two-run e2e sweep is blind
// to it by construction. What catches it is the machine (test/e2e/run.sh step 23); what
// this test pins is the query, so the exit code of a dpkg *record* never comes back.
func TestAptInstall_ObservesTheInstallStatusNotTheRecord(t *testing.T) {
	f := &fakeExec{observe: converged, applyMatch: "apt-get install"}
	eval(t, "apt.install", map[string]string{"pkg": "nano"}, f, engine.Apply)

	if len(f.calls) == 0 {
		t.Fatal("no shell issued")
	}
	observe := f.calls[0]
	if strings.Contains(observe, "dpkg -s") {
		t.Fatalf("`dpkg -s` exits 0 for a purged package, so this observes a record and not an installation:\n%s", observe)
	}
	if !strings.Contains(observe, "install ok installed") {
		t.Fatalf("the observe must require the `install ok installed` status:\n%s", observe)
	}
}
