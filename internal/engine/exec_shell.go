package engine

import (
	"os"
	"os/exec"
	"sort"
	"strings"
)

// ShellExecutor runs blocks through /bin/sh -c with `set -e` injected and
// language variables passed via the environment (injection-safe). When Become
// is set it escalates via Method (sudo by default), preserving the injected
// variables across the escalation (ADR-0011).
//
// Note: `set -o pipefail` is a bashism (dash lacks it); kept out for POSIX
// portability until the shell contract is settled.
type ShellExecutor struct {
	Become string // "" = run as the current user; else escalate to this user
	Method string // "" defaults to sudo; also doas
	Interp string // "" = sh; else sh/bash/dash/nu/raw (ADR-0012)
}

// As returns a copy escalated to `user` (empty = unchanged).
func (s ShellExecutor) As(user string) Executor {
	if user == "" {
		return s
	}
	s.Become = user
	return s
}

// Using returns a copy that runs under `interp` (empty = unchanged, the sh default).
func (s ShellExecutor) Using(interp string) Executor {
	if interp == "" {
		return s
	}
	s.Interp = interp
	return s
}

// prelude is the error-handling injected before the script, per interpreter.
func (s ShellExecutor) prelude() string {
	switch s.Interp {
	case "bash":
		return "set -e\nset -o pipefail\n" // pipefail is legitimate under bash (ADR-0012)
	case "nu", "raw":
		return "" // nu halts by default; raw means "no net"
	default: // "", sh, dash
		return "set -e\n"
	}
}

// shellBin is the interpreter binary and its command flag.
func (s ShellExecutor) shellBin() (bin, flag string) {
	switch s.Interp {
	case "bash":
		return "/bin/bash", "-c"
	case "dash":
		return "/bin/dash", "-c"
	case "nu":
		return "nu", "-c"
	default: // "", sh, raw
		return "/bin/sh", "-c"
	}
}

func (s ShellExecutor) Shell(script string, env Env) ShellResult {
	full := s.prelude() + script
	bin, flag := s.shellBin()

	argv := s.escalate(env)
	argv = append(argv, bin, flag, full)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exit := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1 // could not launch (binary missing, etc.)
		}
	}

	return ShellResult{Exit: exit, Stdout: stdout.String(), Stderr: stderr.String()}
}

// escalate returns the command prefix that runs the shell as Become. The
// injected env vars are preserved across the escalation (never re-quoted into
// the script). sudo runs non-interactive (-n): with NOPASSWD it works, else it
// fails cleanly instead of hanging without a tty.
func (s ShellExecutor) escalate(env Env) []string {
	if s.Become == "" {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	method := s.Method
	if method == "" {
		method = "sudo"
	}
	switch method {
	case "doas":
		argv := []string{"doas"}
		if s.Become != "root" {
			argv = append(argv, "-u", s.Become)
		}
		return argv
	default: // sudo
		argv := []string{"sudo", "-n"}
		if len(keys) > 0 {
			argv = append(argv, "--preserve-env="+strings.Join(keys, ","))
		}
		if s.Become != "root" {
			argv = append(argv, "-u", s.Become)
		}
		return argv
	}
}
