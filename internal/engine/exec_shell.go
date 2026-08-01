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
}

// As returns a copy escalated to `user` (empty = unchanged).
func (s ShellExecutor) As(user string) Executor {
	if user == "" {
		return s
	}
	s.Become = user
	return s
}

func (s ShellExecutor) Shell(script string, env Env) ShellResult {
	full := "set -e\n" + script

	argv := s.escalate(env)
	argv = append(argv, "/bin/sh", "-c", full)

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
