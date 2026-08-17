package lang

import (
	"strings"
	"testing"
)

// The detector refuses shell that a def already does (ADR-0040 §2), and the message names
// both the replacement and the way out — an error with no way out leaves the author
// nowhere to go.
func TestShellRules_RefusesAndNamesTheWayOut(t *testing.T) {
	_, err := ParsePlan("on web {\n  shell { mkdir -p /opt/app }\n}\n")
	if err == nil {
		t.Fatal("mkdir in a shell block must not parse")
	}
	for _, want := range []string{"mkdir", "dir.ensure(path)", "unsafe shell"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must contain %q, got: %v", want, err)
		}
	}
}

func TestShellRules_CpNamesBothDefsAndWhatTheyDoNotCarry(t *testing.T) {
	_, err := ParsePlan("on web {\n  shell { cp /a /b }\n}\n")
	if err == nil {
		t.Fatal("cp in a shell block must not parse")
	}
	// `cp -p` carries mode and ownership; file.copy does not. A message that omitted this
	// would send the author to a def that silently drops what they asked for.
	for _, want := range []string{"file.copy(src, dst)", "dir.copy", "file.mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must contain %q, got: %v", want, err)
		}
	}
}

// `unsafe shell` is the escape hatch of ADR-0040 §3 and must reach every position a shell
// block can occupy — a hatch with a hole is not a hatch.
func TestShellRules_UnsafeShellIsAccepted(t *testing.T) {
	for name, src := range map[string]string{
		"statement": "on web {\n  unsafe shell { mkdir /var/lock/deploy || exit 1 }\n}\n",
		"condition": "on web {\n  if !unsafe shell { mkdir /var/lock/deploy } { shell { logger held }\n }\n}\n",
		"interp":    "on web {\n  unsafe shell(bash) { mkdir /var/lock/deploy }\n}\n",
	} {
		if _, err := ParsePlan(src); err != nil {
			t.Errorf("%s: unsafe shell must parse: %v", name, err)
		}
	}
}

func TestShellRules_UnsafeInADef(t *testing.T) {
	src := `def lock(path: str) { apply { r = unsafe shell { mkdir "$path" } if !r { return err.held(r) } return ok.taken } }`
	if _, err := ParseDefs(src); err != nil {
		t.Fatalf("unsafe shell must parse inside a def: %v", err)
	}
	// …and the same def without the marker does not.
	if _, err := ParseDefs(strings.Replace(src, "unsafe shell", "shell", 1)); err == nil {
		t.Fatal("a def's shell block must be checked like a plan's")
	}
}

func TestShellRules_UnsafeAloneIsARefusalThatSaysWhat(t *testing.T) {
	_, err := ParsePlan("on web {\n  unsafe file.copy(%\"a\", \"/b\")\n}\n")
	if err == nil || !strings.Contains(err.Error(), "unsafe shell") {
		t.Fatalf("`unsafe` on a non-shell must name the form, got: %v", err)
	}
}

// The false-positive floor. Every entry here is legitimate shell that the rules must not
// touch: a detector that cries wolf is one an author learns to route around, and the
// audit value of `grep -r 'unsafe shell'` dies with it.
func TestShellRules_DoesNotFireOnLegitimateShell(t *testing.T) {
	for name, body := range map[string]string{
		"longer command name":  "mkdirhier /opt/app",
		"different command":    "cpio -o < list",
		"word in an argument":  `logger "run mkdir by hand"`,
		"word in a comment":    "# use mkdir here\ntest -d /opt",
		"word as a filename":   "test -f /usr/bin/mkdir",
		"substring of a path":  "ls /opt/mkdir-helper",
		"assignment then safe": "TMP=/tmp test -d /opt",
	} {
		if _, err := ParsePlan("on web {\n  shell { " + body + " }\n}\n"); err != nil {
			t.Errorf("%s: must parse, got: %v", name, err)
		}
	}
}

// The rules do fire where the command really runs, including forms an author would reach
// for without meaning to evade anything.
func TestShellRules_FiresInEveryCommandPosition(t *testing.T) {
	for name, body := range map[string]string{
		"after &&":       "test -d /opt && mkdir /opt/app",
		"after ;":        "cd /opt ; mkdir app",
		"second line":    "cd /opt\nmkdir app",
		"under sudo":     "sudo mkdir /opt/app",
		"by full path":   "/bin/mkdir /opt/app",
		"in a then":      "if test -d /opt; then mkdir /opt/app; fi",
		"in a pipeline":  "mkdir /opt/app | tee /dev/null",
		"in a subshell":  "(mkdir /opt/app)",
		"in a substitut": "echo $(mkdir /opt/app)",
	} {
		if _, err := ParsePlan("on web {\n  shell { " + body + " }\n}\n"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// The documented misses. ADR-0040 records that the detector is a heuristic; these are the
// forms it does not catch, asserted so a reader who assumes coverage is corrected by the
// test suite rather than by a production incident. A miss that becomes catchable moves to
// the test above; a miss that is silently believed to be covered is how a heuristic starts
// being trusted as a proof.
func TestShellRules_KnownMisses(t *testing.T) {
	for name, body := range map[string]string{
		"through xargs":      "echo /opt | xargs mkdir",
		"through find":       "find . -type d -exec cp {} /backup \\;",
		"through a variable": "CMD=mkdir; $CMD /opt/app",
		"through eval":       "eval \"mkdir /opt/app\"",
		"a different tool":   "install -d /opt/app",
	} {
		if _, err := ParsePlan("on web {\n  shell { " + body + " }\n}\n"); err != nil {
			t.Errorf("%s: this is a documented miss — if it now fires, move it to the fires-test: %v", name, err)
		}
	}
}

// The stdlib is exempt, and it must be: it holds 5 mkdir, 3 cp and 9 systemctl, because it
// is the layer that reaches the system (ADR-0040 §6). If the exemption breaks, every
// stdlib def stops parsing at once — so this asserts the exemption, not a sample of it.
func TestShellRules_StdlibIsExempt(t *testing.T) {
	src := `def ensure(path: str) { apply { r = shell { mkdir -p "$path" } if !r { return err.runtime(r) } return ok.created } }`
	if _, err := ParseStdlibDefs(src); err != nil {
		t.Fatalf("the stdlib must parse unchecked: %v", err)
	}
	if _, err := ParseDefs(src); err == nil {
		t.Fatal("the same source as user code must be refused")
	}
}
