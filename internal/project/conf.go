package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"shellf/internal/lang"
	"shellf/internal/pathguard"
)

// ConfName is the project's policy file, at the project root beside the four directories of
// ADR-0038.
const ConfName = "shellf.conf"

// Conf is the policy layer of ADR-0057: the settings a project commits so that two people
// running it use the same ones. Every field is also a flag, and the flag wins.
//
// A zero field means "the file did not say", which is why `Parallel` is an int rather than a
// pointer: 0 is not a legal fan-out width (`checkParallel` refuses it), so it cannot be
// confused with a value somebody chose.
type Conf struct {
	Parallel   int
	AgentTTL   time.Duration
	KnownHosts string
}

// confKeys are the keys the file accepts. Spelled exactly like the flags — the lexer admits
// `-` in a name, so `agent-ttl` needs no second spelling to learn.
var confKeys = map[string]bool{"parallel": true, "agent-ttl": true, "known-hosts": true}

// flagOnly are keys a reader might reasonably try and that are refused **by name**, each with
// why. A bare "unknown key" would be true and useless: the author asked a reasonable question
// and deserves the answer, not a list to search.
var flagOnly = map[string]string{
	// ADR-0057 §3. This one is not an oversight and the message says so: committed to a
	// repository it disables host-key verification for everyone who clones it.
	"insecure": "it disables host-key verification, and committed to a repository it would do so " +
		"for everyone who clones it — it stays a flag so it is visible in the invocation (ADR-0057 §3)",
	"dry-run": "it says what *this* run does, not how the project is run (ADR-0057 §3)",
	"json":    "it says what *this* run does, not how the project is run (ADR-0057 §3)",
	"v":       "it says what *this* run does, not how the project is run (ADR-0057 §3)",
	"limit":   "it says what *this* run does, not how the project is run (ADR-0057 §3)",

	"inventory":   "it feeds the plan a value rather than setting policy (ADR-0057 §3)",
	"vars":        "it feeds the plan a value rather than setting policy (ADR-0057 §3)",
	"set":         "it feeds the plan a value rather than setting policy (ADR-0057 §3)",
	"secret-file": "it feeds the plan a value rather than setting policy (ADR-0057 §3)",
	"secret-env":  "it feeds the plan a value rather than setting policy (ADR-0057 §3)",
}

// LoadConf reads `<root>/shellf.conf`. A missing file is an empty Conf and no error: a
// project that sets no policy is the ordinary case.
//
// The format is the one `--vars` files already use — `name = "value"`, one per line — so this
// needs no grammar of its own (`lang.ParseVars`). What it adds is that the key set is
// **closed**: a file is read once and never again, so `paralel = "8"` silently doing nothing
// is a setting the author believes is in force. Refused, naming the key.
func LoadConf(root string) (Conf, error) {
	path := filepath.Join(root, ConfName)
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Conf{}, nil
	}
	if err != nil {
		return Conf{}, err
	}
	// It decides how a run behaves, and runs escalate — so the same question the agent binary
	// is asked (#391): could another local user have written this (ADR-0057 §5).
	if err := pathguard.OwnedAndUnwritable(path); err != nil {
		return Conf{}, fmt.Errorf("%s: %v", ConfName, err)
	}

	kv, err := lang.ParseVars(string(src))
	if err != nil {
		return Conf{}, fmt.Errorf("%s: %v", ConfName, err)
	}

	var c Conf
	for k, v := range kv {
		switch {
		case k == "parallel":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return Conf{}, fmt.Errorf("%s: parallel must be a whole number of at least 1, got %q", ConfName, v)
			}
			c.Parallel = n
		case k == "agent-ttl":
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return Conf{}, fmt.Errorf("%s: agent-ttl must be a positive duration like \"4h\" or \"90m\", got %q", ConfName, v)
			}
			c.AgentTTL = d
		case k == "known-hosts":
			// Relative to the **project root**, not the working directory: the file is
			// committed and shared, so it has to mean the same thing wherever it is run from.
			if v != "" && !filepath.IsAbs(v) {
				v = filepath.Join(root, v)
			}
			c.KnownHosts = v
		case flagOnly[k] != "":
			return Conf{}, fmt.Errorf("%s: %q is a flag on purpose — %s", ConfName, k, flagOnly[k])
		default:
			return Conf{}, fmt.Errorf("%s: unknown setting %q; this file accepts %s", ConfName, k, confKeyList())
		}
	}
	return c, nil
}

// ConfKeys are the settings the file accepts, sorted. Exported for the documentation gate:
// `TestEveryFlagIsDocumented` asserts every flag appears in `README.md`, and a config key can
// ship undocumented exactly the way four flags did (#642, #646).
func ConfKeys() []string {
	names := make([]string, 0, len(confKeys))
	for k := range confKeys {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func confKeyList() string {
	names := ConfKeys()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
