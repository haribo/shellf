package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConf(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ConfName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A project that sets no policy is the ordinary case, not an error.
func TestLoadConf_MissingFileIsEmpty(t *testing.T) {
	c, err := LoadConf(t.TempDir())
	if err != nil {
		t.Fatalf("a missing %s must not be an error: %v", ConfName, err)
	}
	if c != (Conf{}) {
		t.Fatalf("want a zero Conf, got %+v", c)
	}
}

func TestLoadConf_ReadsThePolicyKeys(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "parallel = \"8\"\nagent-ttl = \"4h\"\nknown-hosts = \"keys/known\"\n")
	c, err := LoadConf(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Parallel != 8 {
		t.Errorf("parallel: got %d", c.Parallel)
	}
	if c.AgentTTL != 4*time.Hour {
		t.Errorf("agent-ttl: got %v", c.AgentTTL)
	}
	// Relative to the project root, not the working directory: the file is committed and
	// shared, so it has to mean the same thing wherever it is run from.
	if want := filepath.Join(root, "keys/known"); c.KnownHosts != want {
		t.Errorf("known-hosts: got %q, want %q", c.KnownHosts, want)
	}
}

func TestLoadConf_AbsoluteKnownHostsIsLeftAlone(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "known-hosts = \"/etc/ssh/ssh_known_hosts\"\n")
	c, err := LoadConf(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.KnownHosts != "/etc/ssh/ssh_known_hosts" {
		t.Errorf("got %q", c.KnownHosts)
	}
}

// A file is read once and never again, so a setting that silently does nothing is one the
// author believes is in force. Every refusal names what it found.
func TestLoadConf_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"a typo is not ignored":  {"paralel = \"8\"\n", `unknown setting "paralel"`},
		"the list is offered":    {"paralel = \"8\"\n", "agent-ttl, known-hosts, parallel"},
		"insecure says why":      {"insecure = \"true\"\n", "host-key verification"},
		"a mode is named as one": {"dry-run = \"true\"\n", "what *this* run does"},
		"an input is named too":  {"inventory = \"x\"\n", "feeds the plan a value"},
		"parallel must be a int": {"parallel = \"lots\"\n", "whole number of at least 1"},
		"parallel must be >= 1":  {"parallel = \"0\"\n", "whole number of at least 1"},
		"a ttl must be duration": {"agent-ttl = \"soon\"\n", "positive duration"},
		"a ttl must be positive": {"agent-ttl = \"-1h\"\n", "positive duration"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConf(t, root, tc.body)
			_, err := LoadConf(root)
			if err == nil {
				t.Fatalf("%q must be refused", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal must say %q, got: %v", tc.want, err)
			}
		})
	}
}

// It decides how a run behaves, and runs escalate — so the same question the agent binary is
// asked (#391, ADR-0057 §5).
func TestLoadConf_RefusesAWorldWritableDirectory(t *testing.T) {
	root := t.TempDir()
	writeConf(t, root, "parallel = \"8\"\n")
	if err := os.Chmod(root, 0o777); err != nil {
		t.Skip("cannot make the directory world-writable here")
	}
	defer func() { _ = os.Chmod(root, 0o700) }()
	if os.Getuid() == 0 {
		t.Skip("running as root: every path is ours and writable by design")
	}
	_, err := LoadConf(root)
	if err == nil {
		t.Fatal("a config file anyone can replace must be refused")
	}
	if !strings.Contains(err.Error(), "writable by another user") {
		t.Fatalf("the refusal must say what it found, got: %v", err)
	}
}
