package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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

	// Channel serves the running job's requests for control-host resources
	// (ADR-0031). Nil — the common case — means the plan asks for nothing, and no
	// bridge is opened at all: a plan that needs nothing keeps today's behaviour
	// exactly, including surviving a dropped session while detached.
	Channel func(io.Reader, io.WriteCloser) error

	dialFn func() (conn, error) // test seam: overrides the real SSH dial
}

// conn is a live connection that runs one command per session. The real one
// wraps *ssh.Client; a fake drives the push/deposit/poll sequencing tests (#116).
type conn interface {
	run(cmd string, stdin []byte) (stdout []byte, err error) // like session.Run; nil err = exit 0
	start(cmd string) error                                  // like session.Start (detached agent)
	// duplex runs cmd with pipes on both ends, for the control channel bridge
	// (ADR-0031). The returned closer ends the session, which is what kills the
	// bridge on the target.
	duplex(cmd string) (io.Reader, io.WriteCloser, io.Closer, error)
	close() error
}

// clientConn is the real conn over a golang.org/x/crypto/ssh client.
type clientConn struct{ c *ssh.Client }

func (cc clientConn) run(cmd string, stdin []byte) ([]byte, error) {
	sess, err := cc.c.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	if stdin != nil {
		sess.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	if err := sess.Run(cmd); err != nil {
		return stdout.Bytes(), fmt.Errorf("%v: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// duplex opens a session with both pipes wired, for the bridge. The session — not the
// SSH client — is what closing ends, so the bridge dies with it and leaves no third
// process on the target (ADR-0005's "no trace").
func (cc clientConn) duplex(cmd string) (io.Reader, io.WriteCloser, io.Closer, error) {
	sess, err := cc.c.NewSession()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, nil, nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, nil, nil, err
	}
	if err := sess.Start(cmd); err != nil {
		_ = sess.Close()
		return nil, nil, nil, err
	}
	return stdout, stdin, sess, nil
}

// start runs cmd detached: Start (not Run) then close after a brief pause, so
// the setsid'd process survives while we do not block on the open channel.
func (cc clientConn) start(cmd string) error {
	sess, err := cc.c.NewSession()
	if err != nil {
		return err
	}
	if err := sess.Start(cmd); err != nil {
		_ = sess.Close()
		return err
	}
	time.Sleep(300 * time.Millisecond) // let the agent write agent.pid and detach
	_ = sess.Close()                   // best-effort: closing on a detached process may EOF
	return nil
}

func (cc clientConn) close() error { return cc.c.Close() }

// dialConn opens a conn — the injected fake in tests, else a real SSH client.
func (s SSH) dialConn() (conn, error) {
	if s.dialFn != nil {
		return s.dialFn()
	}
	c, err := s.dial()
	if err != nil {
		return nil, err
	}
	return clientConn{c}, nil
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
func (s SSH) remotePath(bin []byte) string           { return "/tmp/shellf-agent-" + s.pathID(bin) }
func (s SSH) workDir(base string, bin []byte) string { return base + "/shellf-" + s.pathID(bin) }

// workBase picks the workdir root. Prefer /dev/shm — a RAM-backed tmpfs — so a
// request's secret plaintext (and any secret a result echoes) never touches
// persistent disk, keeping it out of backups, snapshots, and undelete (ADR-0025).
// Fall back to /tmp when tmpfs is absent. Probed once over the live connection.
func workBase(cn conn) string {
	if _, err := cn.run(posix("test -w /dev/shm"), nil); err == nil {
		return "/dev/shm"
	}
	return "/tmp"
}

func (s SSH) pathID(bin []byte) string { return hashID(bin) + "-" + sanitizeUser(s.User) }

// posix wraps a transport command so the target runs it under /bin/sh, whatever
// its login shell is: a non-POSIX login shell (nushell, fish) cannot parse the
// `&&`/`$()`/`for … do`/`&` the transport uses (issue #241). Single-quote-escaped,
// so any script body is safe.
func posix(script string) string {
	return "sh -c '" + strings.ReplaceAll(script, "'", `'\''`) + "'"
}

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

// --- pure command/path builders (no network; unit-tested in ssh_test.go) ---

const notDone = "__NOTDONE__" // poll sentinel: the job's result is not ready yet

func donePath(wd, jobid string) string { return fmt.Sprintf("%s/done-%s", wd, jobid) }
func outPath(wd, jobid string) string  { return fmt.Sprintf("%s/out-%s.json", wd, jobid) }

// pushCmd streams stdin to a temp then renames onto path (atomic, +x).
func pushCmd(path string) string {
	tmp := path + ".tmp"
	return fmt.Sprintf("cat > %[1]s && chmod +x %[1]s && mv %[1]s %[2]s", tmp, path)
}

// depositCmd writes the request (stdin) atomically into the workdir. `umask 077`
// makes the workdir 0700 and the request file 0600, so a request that may carry
// a secret is not readable by other (non-root) users on the target (ADR-0018).
func depositCmd(wd, jobid string) string {
	tmp := fmt.Sprintf("%s/req-%s.json.tmp", wd, jobid)
	final := fmt.Sprintf("%s/req-%s.json", wd, jobid)
	return fmt.Sprintf("umask 077 && mkdir -p %[1]s && cat > %[2]s && mv %[2]s %[3]s", wd, tmp, final)
}

// launchCmd starts a detached resident agent with an inactivity TTL.
func launchCmd(path, wd string, ttlSecs int) string {
	return fmt.Sprintf(`setsid %[1]s __agent-resident %[2]s %[3]d >/dev/null 2>&1 </dev/null &`, path, wd, ttlSecs)
}

// agentAliveCmd checks OUR agent is running via /proc (not kill -0: a recycled
// pid would be a false positive).
func agentAliveCmd(wd string) string {
	return fmt.Sprintf(`p=$(cat %[1]s/agent.pid 2>/dev/null) && [ -n "$p" ] && grep -qa __agent-resident /proc/$p/cmdline 2>/dev/null`, wd)
}

func checkDoneCmd(wd, jobid string) string {
	return fmt.Sprintf("if test -f %s; then cat %s; else printf %s; fi", donePath(wd, jobid), outPath(wd, jobid), notDone)
}

func rmJobCmd(wd, jobid string) string {
	return fmt.Sprintf("rm -f %s %s", outPath(wd, jobid), donePath(wd, jobid))
}

// cleanCmd kills every resident agent (via its pid file) and removes all shellf
// files. Only touches shellf-* paths, in both roots: the /tmp binary cache and
// the /dev/shm tmpfs workdir (ADR-0025).
func cleanCmd() string {
	return `for d in /tmp/shellf-*/ /dev/shm/shellf-*/; do [ -e "$d/agent.pid" ] && kill "$(cat "$d/agent.pid")" 2>/dev/null; done; rm -rf /tmp/shellf-* /dev/shm/shellf-* 2>/dev/null; true`
}

// parseDone interprets a checkDone response: the sentinel means "not ready",
// anything else is the result payload.
func parseDone(stdout []byte) (out []byte, ready bool) {
	if bytes.Equal(stdout, []byte(notDone)) {
		return nil, false
	}
	return stdout, true
}

func (s SSH) Run(agentBin string, req []byte) ([]byte, error) {
	bin, err := os.ReadFile(agentBin)
	if err != nil {
		return nil, fmt.Errorf("read agent: %w", err)
	}
	path, jobid := s.remotePath(bin), newJobID()
	deadline := time.Now().Add(s.execTimeout())

	// One connection: push (if not cached), ensure a resident agent, deposit the job.
	cn, err := s.dialConn()
	if err != nil {
		return nil, err
	}
	// The workdir goes on tmpfs so secret plaintext stays off disk (ADR-0025);
	// probed on this connection since it depends on the target.
	wd := s.workDir(workBase(cn), bin)
	if !cached(cn, path) {
		if err := push(cn, bin, path); err != nil {
			_ = cn.close()
			return nil, err
		}
	}
	if err := deposit(cn, wd, jobid, req); err != nil {
		_ = cn.close()
		return nil, err
	}
	if !agentAlive(cn, wd) {
		if err := cn.start(posix(launchCmd(path, wd, s.agentTTLSecs()))); err != nil {
			_ = cn.close()
			return nil, err
		}
	}
	_ = cn.close()

	// Bridge the control channel for the duration of the job, when the plan needs it.
	// A separate session: it must be able to die (a dropped bridge is recoverable)
	// without taking the poll with it.
	if s.Channel != nil {
		stop := s.bridge(path, wd)
		defer stop()
	}

	// Poll for the result, re-dialing on a dropped session, until the deadline.
	// The detached agent keeps running across drops, so a long job survives.
	return s.poll(wd, jobid, deadline)
}

// bridge opens a session running `shellf __bridge` and serves the job's requests on it
// until stop() is called. Best-effort by design: if the session cannot be opened, the
// job simply gets a failure on its first request, naming the resource — which is a far
// better diagnostic than refusing to start a plan that may never ask anything.
func (s SSH) bridge(binPath, wd string) (stop func()) {
	var (
		mu       sync.Mutex
		sessions []io.Closer
		stopped  bool
	)
	isStopped := func() bool { mu.Lock(); defer mu.Unlock(); return stopped }

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Relaunch the bridge when its session drops, which is the property that makes a
		// socket worth having over a pipe (ADR-0031 §2): the agent stays detached and
		// keeps listening, so a dropped session costs a reconnection, not the job. Until
		// #347 this loop ran once, so a flaky link or an idle timeout killed every
		// remaining `~file.read` in the job.
		for attempt := 0; attempt <= bridgeRetries; attempt++ {
			if isStopped() {
				return
			}
			if attempt > 0 {
				// Back off, and re-check: a host that has genuinely gone away must not
				// keep the run alive by spinning on a dial that cannot succeed.
				time.Sleep(bridgeRetryWait)
				if isStopped() {
					return
				}
			}
			cn, err := s.dialConn()
			if err != nil {
				continue
			}
			out, in, sess, err := cn.duplex(posix(bridgeCmd(binPath, wd)))
			if err != nil {
				_ = cn.close()
				continue
			}
			mu.Lock()
			sessions = append(sessions, sess)
			mu.Unlock()
			_ = s.Channel(out, in)
			_ = cn.close()
			// Serving returned: either the session dropped, or stop() closed it. Only
			// the first deserves another bridge — mistaking our own shutdown for a drop
			// would reopen a session nobody will use, on a host we are done with.
			if isStopped() {
				return
			}
		}
	}()

	// stop closes the session, which kills the bridge on the target and unblocks the
	// serving goroutine. Closing is what ends it: waiting alone would hang, since
	// serving only returns when the channel closes.
	//
	// The wait that follows is bounded: the run must not hang because a bridge did not
	// notice its session went away — the job's result is already in hand by then.
	return func() {
		mu.Lock()
		stopped = true // set before closing, so the loop reads it as shutdown, not a drop
		for _, c := range sessions {
			_ = c.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

// bridgeCmd runs the bridge from the pushed agent binary (in /tmp, cached by hash)
// against the socket in the job workdir (on tmpfs).
func bridgeCmd(binPath, wd string) string {
	return fmt.Sprintf("%s __bridge %s/sock", binPath, wd)
}

// Clean kills any resident agent on the target and removes every shellf file
// from /tmp (binaries and workdirs). Safe: it only touches /tmp/shellf-* paths.
func (s SSH) Clean() error {
	cn, err := s.dialConn()
	if err != nil {
		return err
	}
	defer func() { _ = cn.close() }()
	if _, err := cn.run(posix(cleanCmd()), nil); err != nil {
		return fmt.Errorf("clean: %w", err)
	}
	return nil
}

// cached reports whether the agent binary is already present and executable on
// the target (a cache hit → skip the transfer).
func cached(cn conn, path string) bool {
	_, err := cn.run(posix("test -x "+path), nil)
	return err == nil
}

func (s SSH) dial() (*ssh.Client, error) {
	methods, closeAuth, err := s.authMethods()
	if err != nil {
		return nil, err
	}
	defer closeAuth() // the agent conn is only needed during the handshake (ADR-0026)
	hostKey, err := s.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            s.User,
		Auth:            methods,
		HostKeyCallback: hostKey,
		Timeout:         s.timeout(),
	}
	return ssh.Dial("tcp", net.JoinHostPort(s.Host, s.port()), cfg)
}

// authMethods builds the ordered SSH auth methods (ADR-0026): an explicit
// inventory `key:` (a pinned deploy key) is tried first, then the ssh-agent via
// SSH_AUTH_SOCK (the key never leaves the agent). Neither configured → a clear
// error. The returned cleanup closes any opened agent connection.
func (s SSH) authMethods() ([]ssh.AuthMethod, func(), error) {
	noop := func() {}
	var methods []ssh.AuthMethod

	if s.Key != "" {
		signer, err := s.signer()
		if err != nil {
			return nil, noop, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, noop, fmt.Errorf("connect ssh-agent (%s): %w", sock, err)
		}
		methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		noop = func() { _ = conn.Close() }
	}

	if len(methods) == 0 {
		return nil, noop, fmt.Errorf("no ssh authentication: set a key in the inventory or start an ssh-agent (SSH_AUTH_SOCK)")
	}
	return methods, noop, nil
}

// hostKeyCallback verifies the target's key against known_hosts, distinguishing
// an unknown host from a CHANGED key (possible MITM). --insecure bypasses.
func (s SSH) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if s.Insecure {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	path := expandTilde(s.KnownHosts)
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

var pollWait = time.Second // poll cadence; a var so tests can shrink it

// How hard the control host tries to put a bridge back after its session drops (#347).
// Bounded on purpose: a host that has genuinely gone away must let the run end, and the
// job then fails on its next ask, naming the resource it waited for (ADR-0031 §2) rather
// than hanging. Vars so a test need not sit through the waits.
var (
	bridgeRetries   = 5
	bridgeRetryWait = 500 * time.Millisecond
)

// push streams the binary (stdin) to a temp then renames onto path (atomic, +x):
// an interrupted push never leaves a partial binary, and a concurrent run sees
// either the old file or none.
func push(cn conn, bin []byte, path string) error {
	if _, err := cn.run(posix(pushCmd(path)), bin); err != nil {
		return fmt.Errorf("push agent: %w", err)
	}
	return nil
}

// deposit writes the request atomically (tmp + mv).
func deposit(cn conn, wd, jobid string, req []byte) error {
	if _, err := cn.run(posix(depositCmd(wd, jobid)), req); err != nil {
		return fmt.Errorf("deposit: %w", err)
	}
	return nil
}

// agentAlive reports whether OUR resident agent is running. It checks the pid's
// /proc/<pid>/cmdline, not just kill -0: a dead agent's pid can be recycled by
// an unrelated process (common in a container), which kill -0 would accept — a
// false positive that would skip the relaunch and hang the poll. The detached
// launch itself (cn.start of launchCmd) is in Run, since it needs s.agentTTLSecs.
func agentAlive(cn conn, wd string) bool {
	_, err := cn.run(posix(agentAliveCmd(wd)), nil)
	return err == nil
}

// poll waits for the job's result, re-dialing on a dropped session until the
// deadline. Because the agent is detached, the job keeps running across drops.
func (s SSH) poll(wd, jobid string, deadline time.Time) ([]byte, error) {
	var cn conn
	defer func() {
		if cn != nil {
			_ = cn.close()
		}
	}()

	for time.Now().Before(deadline) {
		if cn == nil {
			c, err := s.dialConn()
			if err != nil {
				time.Sleep(pollWait)
				continue // survive: retry the dial
			}
			cn = c
		}
		data, ready, err := checkDone(cn, wd, jobid)
		if err != nil {
			_ = cn.close()
			cn = nil // dropped → re-dial next iteration
			time.Sleep(pollWait)
			continue
		}
		if ready {
			rmJob(cn, wd, jobid)
			return data, nil
		}
		time.Sleep(pollWait)
	}
	return nil, fmt.Errorf("agent job %s timed out after %s", jobid, s.execTimeout())
}

// checkDone returns the result if done exists; a run error means a dropped
// connection (distinct from "not done yet").
func checkDone(cn conn, wd, jobid string) (out []byte, ready bool, err error) {
	stdout, err := cn.run(posix(checkDoneCmd(wd, jobid)), nil)
	if err != nil {
		return nil, false, err
	}
	out, ready = parseDone(stdout)
	return out, ready, nil
}

// rmJob removes the consumed result/done files (best-effort).
func rmJob(cn conn, wd, jobid string) {
	_, _ = cn.run(posix(rmJobCmd(wd, jobid)), nil)
}

func (s SSH) signer() (ssh.Signer, error) {
	if s.Key == "" {
		return nil, fmt.Errorf("no ssh key provided")
	}
	pem, err := os.ReadFile(expandTilde(s.Key))
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	return ssh.ParsePrivateKey(pem)
}

// expandTilde resolves a leading `~/` (or a bare `~`) to the user's home dir. Go's
// os.ReadFile does not expand `~` — only the shell does — so an inventory
// `key: "~/.ssh/id_ed25519"` must be expanded here. Absolute, relative, and
// `~user/` paths are returned unchanged (`~user` is not resolved).
func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
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
