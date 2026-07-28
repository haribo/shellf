package engine

import (
	"os"
	"os/exec"
	"strings"
)

// ShellExecutor runs blocks through /bin/sh -c with `set -e` injected and
// language variables passed via the environment (injection-safe).
//
// Note: `set -o pipefail` is a bashism (dash lacks it); kept out for POSIX
// portability until the shell contract is settled.
type ShellExecutor struct{}

func (ShellExecutor) Shell(script string, env Env) ShellResult {
	full := "set -e\n" + script

	cmd := exec.Command("/bin/sh", "-c", full)
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
