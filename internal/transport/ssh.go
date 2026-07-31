package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSH pushes (or reuses a hash-cached binary) and runs the agent over a single
// golang.org/x/crypto/ssh connection (one TCP dial, multiple sessions). The
// agent process stays ephemeral; only its binary is cached on the target.
// Hardened over the earlier shell-out: dial timeout + an exec watchdog, no
// per-host subprocess fork, host-key verification against known_hosts.
type SSH struct {
	User        string
	Host        string
	Port        string        // empty = 22
	Key         string        // identity file
	Timeout     time.Duration // dial (connect) timeout; 0 = 10s
	ExecTimeout time.Duration // job watchdog (poll deadline); 0 = 30m
	AgentTTL    time.Duration // resident agent inactivity TTL; 0 = 2h
	KnownHosts  string        // known_hosts path; empty = ~/.ssh/known_hosts
	Insecure    bool          // bypass host-key verification (dev only)
}

// agentTTLSecs is the inactivity TTL passed to the launched resident agent.
func (s SSH) agentTTLSecs() int {
	if s.AgentTTL == 0 {
		return int((2 * time.Hour).Seconds())
	}
	return int(s.AgentTTL.Seconds())
}

// hashID identifies a build by a short hash of the binary. Paths are per-build:
// a new version reuses neither the old cached binary nor the old workdir.
func hashID(bin []byte) string {
	sum := sha256.Sum256(bin)
	return hex.EncodeToString(sum[:8])
}

// remotePath is the cached binary path; workDir is the resident agent's
// rendezvous directory (request/result/pid files). Both are scoped by the SSH
// user as well as the build hash: a resident agent belongs to the user that
// launched it, so a different user gets its own agent and never reuses one that
// would run its jobs under the wrong identity (issue #114).
func (s SSH) remotePath(bin []byte) string { return "/tmp/shellf-agent-" + s.pathID(bin) }
func (s SSH) workDir(bin []byte) string    { return "/tmp/shellf-" + s.pathID(bin) }

func (s SSH) pathID(bin []byte) string { return hashID(bin) + "-" + sanitizeUser(s.User) }

// sanitizeUser keeps the SSH user usable and injection-safe as a path segment.
func sanitizeUser(u string) string {
	if u == "" {
		return "nouser"
	}
	b := make([]byte, 0, len(u))
	for i := 0; i < len(u); i++ {
		switch c := u[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}

var jobCounter atomic.Uint64

// newJobID is unique per job across a control run.
func newJobID() string {
	return fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), jobCounter.Add(1))
}

func (s SSH) Run(agentBin string, req []byte) ([]byte, error) {
	bin, err := os.ReadFile(agentBin)
	if err != nil {
		return nil, fmt.Errorf("read agent: %w", err)
	}
	path, wd, jobid := s.remotePath(bin), s.workDir(bin), newJobID()
	deadline := time.Now().Add(s.execTimeout())

	// One connection: push (if not cached), ensure a resident agent, deposit the job.
	client, err := s.dial()
	if err != nil {
		return nil, err
	}
	if !s.cached(client, path) {
		if err := s.push(client, bin, path); err != nil {
			client.Close()
			return nil, err
		}
	}
	if err := s.deposit(client, wd, jobid, req); err != nil {
		client.Close()
		return nil, err
	}
	if !s.agentAlive(client, wd) {
		if err := s.launchAgent(client, path, wd); err != nil {
			client.Close()
			return nil, err
		}
	}
	client.Close()

	// Poll for the result, re-dialing on a dropped session, until the deadline.
	// The detached agent keeps running across drops, so a long job survives.
	return s.poll(wd, jobid, deadline)
}

// Clean kills any resident agent on the target and removes every shellf file
// from /tmp (binaries and workdirs). Safe: it only touches /tmp/shellf-* paths.
func (s SSH) Clean() error {
	client, err := s.dial()
	if err != nil {
		return err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	cmd := `for d in /tmp/shellf-*/; do [ -e "$d/agent.pid" ] && kill "$(cat "$d/agent.pid")" 2>/dev/null; done; rm -rf /tmp/shellf-* 2>/dev/null; true`
	if err := sess.Run(cmd); err != nil {
		return fmt.Errorf("clean: %v: %s", err, stderr.String())
	}
	return nil
}

// cached reports whether the agent binary is already present and executable on
// the target (a cache hit → skip the transfer).
func (s SSH) cached(client *ssh.Client, path string) bool {
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	defer sess.Close()
	return sess.Run("test -x "+path) == nil
}

func (s SSH) dial() (*ssh.Client, error) {
	signer, err := s.signer()
	if err != nil {
		return nil, err
	}
	hostKey, err := s.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            s.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKey,
		Timeout:         s.timeout(),
	}
	return ssh.Dial("tcp", net.JoinHostPort(s.Host, s.port()), cfg)
}

// hostKeyCallback verifies the target's key against known_hosts, distinguishing
// an unknown host from a CHANGED key (possible MITM). --insecure bypasses.
func (s SSH) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if s.Insecure {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	path := s.KnownHosts
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("known_hosts (%s): %w — use --insecure to bypass", path, err)
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		if err := cb(host, remote, key); err != nil {
			var ke *knownhosts.KeyError
			if errors.As(err, &ke) && len(ke.Want) > 0 {
				return fmt.Errorf("host key CHANGED for %s — possible MITM, refusing", host)
			}
			return fmt.Errorf("host key for %s not in known_hosts — add it (ssh-keyscan) or use --insecure", host)
		}
		return nil
	}, nil
}

// push streams the binary to the target (stdin of a `cat` session), writing to
// a temp then renaming: an interrupted push never leaves a partial binary at
// path, and a concurrent run sees either the old file or none (atomic mv).
func (s SSH) push(client *ssh.Client, bin []byte, path string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	sess.Stdin = bytes.NewReader(bin)
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	tmp := path + ".tmp"
	cmd := fmt.Sprintf("cat > %[1]s && chmod +x %[1]s && mv %[1]s %[2]s", tmp, path)
	if err := sess.Run(cmd); err != nil {
		return fmt.Errorf("push agent: %v: %s", err, stderr.String())
	}
	return nil
}

const pollWait = time.Second

// deposit writes the request atomically (tmp + mv) — a blocking Run that closes
// cleanly (no backgrounded process to hold the channel open).
func (s SSH) deposit(client *ssh.Client, wd, jobid string, req []byte) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = bytes.NewReader(req)
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	reqTmp := fmt.Sprintf("%s/req-%s.json.tmp", wd, jobid)
	reqFinal := fmt.Sprintf("%s/req-%s.json", wd, jobid)
	cmd := fmt.Sprintf("mkdir -p %[1]s && cat > %[2]s && mv %[2]s %[3]s", wd, reqTmp, reqFinal)
	if err := sess.Run(cmd); err != nil {
		return fmt.Errorf("deposit: %v: %s", err, stderr.String())
	}
	return nil
}

// agentAlive reports whether OUR resident agent is running. It checks the pid's
// /proc/<pid>/cmdline, not just kill -0: a dead agent's pid can be recycled by
// an unrelated process (common in a container), which kill -0 would accept — a
// false positive that would skip the relaunch and hang the poll.
func (s SSH) agentAlive(client *ssh.Client, wd string) bool {
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	defer sess.Close()
	cmd := fmt.Sprintf(`p=$(cat %[1]s/agent.pid 2>/dev/null) && [ -n "$p" ] && grep -qa __agent-resident /proc/$p/cmdline 2>/dev/null`, wd)
	return sess.Run(cmd) == nil
}

// launchAgent starts a detached resident agent. It uses Start (not Run) and
// closes our side after a brief pause: a detached process keeps the exec
// channel open, so Run would block waiting for it. The setsid'd agent survives
// the Close (it is in its own session/process-group). No lock: a rare double
// launch (concurrent runs) is harmless — the agent claims each request
// atomically, so no request is run twice, and idle duplicates self-kill.
func (s SSH) launchAgent(client *ssh.Client, path, wd string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(
		`setsid %[1]s __agent-resident %[2]s %[3]d >/dev/null 2>&1 </dev/null &`,
		path, wd, s.agentTTLSecs())
	if err := sess.Start(cmd); err != nil {
		sess.Close()
		return err
	}
	time.Sleep(300 * time.Millisecond) // let the agent write agent.pid and detach
	sess.Close()                        // best-effort: closing a channel with a detached process may EOF
	return nil
}

// poll waits for the job's result, re-dialing on a dropped session until the
// deadline. Because the agent is detached, the job keeps running across drops.
func (s SSH) poll(wd, jobid string, deadline time.Time) ([]byte, error) {
	donePath := fmt.Sprintf("%s/done-%s", wd, jobid)
	outPath := fmt.Sprintf("%s/out-%s.json", wd, jobid)

	var client *ssh.Client
	defer func() {
		if client != nil {
			client.Close()
		}
	}()

	for time.Now().Before(deadline) {
		if client == nil {
			c, err := s.dial()
			if err != nil {
				time.Sleep(pollWait)
				continue // survive: retry the dial
			}
			client = c
		}
		data, ready, err := s.checkDone(client, donePath, outPath)
		if err != nil {
			client.Close()
			client = nil // dropped → re-dial next iteration
			time.Sleep(pollWait)
			continue
		}
		if ready {
			s.rmJob(client, wd, jobid)
			return data, nil
		}
		time.Sleep(pollWait)
	}
	return nil, fmt.Errorf("agent job %s timed out after %s", jobid, s.execTimeout())
}

// checkDone returns the result if done exists; a session error means a dropped
// connection (distinct from "not done yet").
func (s SSH) checkDone(client *ssh.Client, donePath, outPath string) (out []byte, ready bool, err error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, false, err
	}
	defer sess.Close()
	var stdout bytes.Buffer
	sess.Stdout = &stdout
	cmd := fmt.Sprintf("if test -f %s; then cat %s; else printf __NOTDONE__; fi", donePath, outPath)
	if err := sess.Run(cmd); err != nil {
		return nil, false, err
	}
	data := stdout.Bytes()
	if bytes.Equal(data, []byte("__NOTDONE__")) {
		return nil, false, nil
	}
	return data, true, nil
}

// rmJob removes the consumed result/done files (best-effort).
func (s SSH) rmJob(client *ssh.Client, wd, jobid string) {
	sess, err := client.NewSession()
	if err != nil {
		return
	}
	defer sess.Close()
	_ = sess.Run(fmt.Sprintf("rm -f %s/out-%s.json %s/done-%s", wd, jobid, wd, jobid))
}

func (s SSH) signer() (ssh.Signer, error) {
	if s.Key == "" {
		return nil, fmt.Errorf("no ssh key provided")
	}
	pem, err := os.ReadFile(s.Key)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	return ssh.ParsePrivateKey(pem)
}

func (s SSH) port() string {
	if s.Port == "" {
		return "22"
	}
	return s.Port
}

func (s SSH) timeout() time.Duration {
	if s.Timeout == 0 {
		return 10 * time.Second
	}
	return s.Timeout
}

func (s SSH) execTimeout() time.Duration {
	if s.ExecTimeout == 0 {
		return 30 * time.Minute // detached agent survives drops, so allow long jobs
	}
	return s.ExecTimeout
}
