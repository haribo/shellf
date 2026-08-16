package lang

import (
	"strings"
)

// The detector of ADR-0040 §2: a `shell { }` block whose command has an **exact** def
// equivalent does not parse. The def is re-runnable by construction — it observes, it
// reports, it converges — which is what ADR-0040 §1 requires of every step, and the
// cheapest moment to say so is before the run rather than on the second one.
//
// It is a heuristic and is recorded as one. `$CMD`, `eval`, `install -d`, `tar x` and
// `find -exec cp` defeat it, and no practical analysis of shell changes that. What keeps a
// heuristic honest is the admission rule below, not its coverage.

// shellRule is one command whose def equivalent is exact. `why` is not documentation of
// the rule — it is the rule's justification, and a rule that cannot state one is removed
// rather than qualified (ADR-0040 §5).
type shellRule struct {
	cmd  string
	hint string
	why  string
}

var shellRules = []shellRule{
	{
		cmd:  "mkdir",
		hint: "dir.ensure(path) is idempotent and previewable",
		// `dir.ensure` *is* `mkdir -p`, with an observe on presence. `mkdir` without `-p`
		// has no def — as an atomic lock (`mkdir /var/lock/x || exit`) its whole value is
		// failing when the directory exists — so it reaches `unsafe shell`, which is the
		// right answer for it and not a false positive (ADR-0040 §3).
		why: "dir.ensure is mkdir -p, and observes presence",
	},
	{
		cmd: "cp",
		hint: "file.copy(src, dst) for a file, dir.copy(%\"src\", dst) for a tree — " +
			"neither carries mode or ownership: use file.mode / dir.owner",
		// Both deliver bytes and report copied/already. The hint says what they do *not*
		// do, because `cp -p` and `cp -a` are not equivalent and a message claiming they
		// are would send the author to a def that silently drops what they asked for.
		why: "file.copy/dir.copy deliver the same bytes and report the outcome",
	},
}

// checkShellBody returns the parse error for the first offending command, or "" when the
// body is clear. The message always ends with the way out: an error with no way out is a
// dead end, and the author's only remaining move is to stop using shellf.
func checkShellBody(body string) string {
	for _, w := range commandWords(body) {
		for _, r := range shellRules {
			if w == r.cmd {
				return r.cmd + " here — " + r.hint + ".\n" +
					"Write `unsafe shell { … }` to keep the shell."
			}
		}
	}
	return ""
}

// commandWords returns the words that sit in *command position*: the first word of the
// body and of every segment a shell operator opens. Matching anywhere in the body instead
// would flag `mkdir` in a comment, in a message, or as an argument (`echo "run mkdir"`),
// and a detector that cries wolf is one the author learns to route around.
func commandWords(body string) []string {
	var out []string
	for _, seg := range splitSegments(body) {
		if w := leadingCommand(seg); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// separators is what opens a new command position. `$(` and a backtick are here because a
// substitution runs a command like any other.
var separators = []string{"\n", ";", "&&", "||", "|", "$(", "`", "(", "{", "}", ")"}

func splitSegments(body string) []string {
	s := body
	for _, sep := range separators {
		s = strings.ReplaceAll(s, sep, "\n")
	}
	return strings.Split(s, "\n")
}

// skippable words sit before the command without being it. `sudo mkdir` is still a mkdir,
// and so is `then mkdir`; missing those would make the rule trivially avoidable by an
// author who was not even trying to avoid it.
var skippable = map[string]bool{
	"sudo": true, "command": true, "then": true, "else": true, "elif": true,
	"do": true, "!": true, "time": true, "exec": true,
}

func leadingCommand(seg string) string {
	for _, f := range strings.Fields(seg) {
		switch {
		case strings.HasPrefix(f, "#"): // a comment: nothing after it runs
			return ""
		case skippable[f]:
			continue
		case strings.ContainsAny(f, "$\"'"): // interpolated or quoted — unknowable, and
			return "" // ADR-0040 records that the detector does not chase it
		case strings.Contains(f, "="): // FOO=bar cmd — an assignment, not the command
			continue
		}
		// A path names the same command: `/bin/mkdir` is `mkdir`. `cpio` keeps its own
		// basename, so the exact match below still spares it.
		if i := strings.LastIndex(f, "/"); i >= 0 {
			f = f[i+1:]
		}
		return f
	}
	return ""
}
