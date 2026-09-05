package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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

	"shellf/internal/agentbin"
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

	// Trace, when set, receives one line per control-host decision: where it connected,
	// which agent it pushed or reused, which workdir it chose, how long the job took.
	// Nil — the default — costs nothing.
	//
	// It is a callback rather than an io.Writer because masking is the caller's job: the
	// CLI knows the run's secrets, the transport does not, and a diagnostic channel that
	// prints what the report masks is worse than no diagnostic channel (#461).
	Trace func(format string, a ...any)

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

// posix wraps a transport command so the target runs it under /bin/sh, whatever its login
// shell is: a non-POSIX login shell cannot parse the `&&`/`$()`/`for … do` the transport
// uses (#241). nushell is the one measured to break on these scripts; no CI target covers
// it, since it is not packaged in Debian.
//
// The script travels base64-encoded, and that is the whole point. Escaping it for POSIX sh
// does not work, because the **login shell** reads the command line before `sh` exists —
// asking the shell being worked around to understand POSIX quoting is asking it to be
// POSIX (#439). base64's alphabet is `A-Za-z0-9+/=`, nothing any shell treats as syntax,
// so what the login shell sees has nothing to reinterpret and `sh` receives the script
// byte for byte. `base64 -d` is coreutils and busybox alike, alongside the `find`,
// `stat -c` and `sha256sum` the transport already requires of a target.
//
// The pipe costs stdin: `sh` inherits the decoder's, so a script reading stdin gets the
// spent pipe instead of what the caller sent. The two commands that do read it — pushing
// the binary, depositing a request — go through posixKeepingStdin below.
func posix(script string) string {
	return "echo " + base64.StdEncoding.EncodeToString([]byte(script)) + " | base64 -d | sh"
}

// posixKeepingStdin wraps a script that reads its input from stdin, where the pipe above
// cannot be used: the decoder would own stdin and the script would read an empty one —
// measured, a `cat > file` received nothing.
//
// The bridge takes this path too: it *is* a stdin/stdout conversation with the agent
// (ADR-0031), so a decoder owning stdin leaves the control host unreachable — measured,
// every `~file.read` answered `no control host attached`.
//
// It embeds the script single-quoted, with **no escaping**, and that is only safe because
// the script holds no single quote of its own: `'…'` with nothing to escape is a literal
// string in every shell tried, POSIX or not (verified under nushell and plan9 rc). Escaping
// is what broke #439, not quoting.
//
// The invariant is the caller's, and it is asserted by a test over every command that
// takes this path rather than left to a comment: a quote added to one of them turns the
// build red instead of making a host unreachable.
func posixKeepingStdin(script string) string {
	return "sh -c '" + script + "'"
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

// agentFor picks the bytes to push: this binary's own when the target shares its
// architecture, the embedded peer otherwise (ADR-0048).
//
// A target that cannot say what it runs keeps the behaviour it has always had — its own
// bytes. Refusing there would break hosts that work today over a question they cannot
// answer, and every Linux target that shellf supports has `uname`. A target that *does*
// answer, with something this build cannot serve, is refused by name.
func (s SSH) agentFor(cn conn, self []byte) ([]byte, error) {
	out, err := cn.run(posix("uname -m"), nil)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return self, nil // silent target: unchanged behaviour
	}
	arch, err := agentbin.ArchFromUname(string(out))
	if err != nil {
		return nil, err
	}
	s.trace("%s architecture %s", s.Host, arch)
	return agentbin.For(arch, self)
}

// --- pure command/path builders (no network; unit-tested in ssh_test.go) ---

const notDone = "__NOTDONE__" // poll sentinel: the job's result is not ready yet

func donePath(wd, jobid string) string { return fmt.Sprintf("%s/done-%s", wd, jobid) }
func outPath(wd, jobid string) string  { return fmt.Sprintf("%s/out-%s.json", wd, jobid) }

// pushCmd streams stdin to a temp then renames onto path (atomic, +x).
// 700 and not `+x`: `chmod +x` keeps whatever the account's umask left, which on a target
// with umask 0002 is 775 — any member of the SSH user's group can then rewrite the binary
// shellf is about to execute. It also makes the mode an invariant the cache probe can
// check, instead of something that varies per account (#391).
func pushCmd(path string) string {
	tmp := path + ".tmp"
	return fmt.Sprintf("cat > %[1]s && chmod 700 %[1]s && mv %[1]s %[2]s", tmp, path)
}

// depositCmd writes the request (stdin) atomically into the workdir. `umask 077`
// makes the workdir 0700 and the request file 0600, so a request that may carry
// a secret is not readable by other (non-root) users on the target (ADR-0018).
func depositCmd(wd, jobid string) string {
	tmp := fmt.Sprintf("%s/req-%s.json.tmp", wd, jobid)
	final := fmt.Sprintf("%s/req-%s.json", wd, jobid)
	// No `mkdir` here: the workdir was created and vetted by workdirEnsureCmd, and creating
	// it again with `-p` was what accepted a directory somebody else had put there (#413).
	return fmt.Sprintf("umask 077 && cat > %[1]s && mv %[1]s %[2]s", tmp, final)
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

// trace emits one diagnostic line when the caller asked for them.
func (s SSH) trace(format string, a ...any) {
	if s.Trace != nil {
		s.Trace(format, a...)
	}
}

func (s SSH) Run(agentBin string, req []byte) ([]byte, error) {
	self, err := os.ReadFile(agentBin)
	if err != nil {
		return nil, fmt.Errorf("read agent: %w", err)
	}
	jobid := newJobID()
	deadline := time.Now().Add(s.execTimeout())

	// One connection: push (if not cached), ensure a resident agent, deposit the job.
	started := time.Now()
	s.trace("%s@%s:%s connecting", s.User, s.Host, s.port())
	cn, err := s.dialConn()
	if err != nil {
		return nil, err
	}
	// Which bytes to push is a question about the *target*, so it cannot be answered
	// before this connection exists (ADR-0048). Pushing our own bytes at a host of
	// another architecture put an unrunnable binary on it and surfaced as
	// `exec format error` from a process shellf did not write (#453).
	bin, err := s.agentFor(cn, self)
	if err != nil {
		_ = cn.close()
		return nil, fmt.Errorf("target %s: %w", s.Host, err)
	}
	path := s.remotePath(bin)
	// The workdir goes on tmpfs so secret plaintext stays off disk (ADR-0025);
	// probed on this connection since it depends on the target.
	wd := s.workDir(workBase(cn), bin)
	// Fail closed, and name what was found: a foreign binary at our path is not a cache
	// miss to paper over by re-pushing — the file is not ours to replace, and executing it
	// is the whole of #391.
	sum := sha256.Sum256(bin)
	st, err := agentState(cn, path, hex.EncodeToString(sum[:]))
	if err != nil {
		_ = cn.close()
		return nil, err
	}
	s.trace("%s workdir %s", s.Host, wd)
	switch {
	case st == "ok": // ours, unchanged → skip the transfer
		s.trace("%s agent cached at %s", s.Host, path)
	case strings.HasPrefix(st, "foreign"):
		_ = cn.close()
		return nil, fmt.Errorf("refusing to run %s: it is not ours (%s) — remove it on the target",
			path, strings.TrimSpace(strings.TrimPrefix(st, "foreign")))
	default: // absent, or ours with different bytes → (re)transfer
		s.trace("%s push %d bytes to %s (%s)", s.Host, len(bin), path, st)
		if err := push(cn, bin, path); err != nil {
			_ = cn.close()
			return nil, err
		}
	}
	wst, err := ensureWorkdir(cn, wd)
	if err != nil {
		_ = cn.close()
		return nil, err
	}
	if strings.HasPrefix(wst, "unsafe") {
		_ = cn.close()
		return nil, fmt.Errorf("refusing to use %s: another user can write there (%s) — the agent runs any request it finds",
			wd, strings.TrimSpace(strings.TrimPrefix(wst, "unsafe")))
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
	out, err := s.poll(wd, jobid, deadline)
	s.trace("%s job %s finished in %s", s.Host, jobid, time.Since(started).Round(time.Millisecond))
	return out, err
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
			out, in, sess, err := cn.duplex(posixKeepingStdin(bridgeCmd(binPath, wd)))
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

// agentStateCmd asks what sits at the cached agent path. `test -x` used to answer this,
// and it answers none of the questions that matter: the path is
// `/tmp/shellf-agent-<digest of the binary>-<user>`, both halves public, so any local user
// can create that file first and have it executed — often to run work under `as root`
// (#391).
//
// Three questions, one round trip. `find … ! -perm /022` is the "nobody else can write
// it" test; `-user` is the "it is ours" test; the digest is the "it is the binary we would
// have sent" test. It always exits 0 and answers in a word, so a refusal can name what it
// found rather than surfacing a shell exit code.
func agentStateCmd(path, wantSum string) string {
	return fmt.Sprintf(`if [ ! -e %[1]s ]; then echo absent; exit 0; fi
if [ -z "$(find %[1]s -maxdepth 0 -type f -user "$(id -un)" ! -perm /022 2>/dev/null)" ]; then
echo "foreign $(stat -c '%%U:%%a' %[1]s 2>/dev/null)"; exit 0; fi
s=$(sha256sum %[1]s 2>/dev/null | cut -d' ' -f1)
if [ "$s" = "%[2]s" ]; then echo ok; else echo "stale $s"; fi`, path, wantSum)
}

// workdirEnsureCmd creates the rendezvous directory and answers what it found: `ok`, or
// `unsafe …`.
//
// The agent runs **any** `req-*.json` it finds there without asking who wrote it
// (internal/agent/resident.go), so a directory another user can write to is a way to have
// a request of their choosing executed, `as root` steps included. The path is derived from
// the binary's digest and the SSH user, so it is calculable by anyone (#391).
//
// Creating and checking must therefore be **one** command (#413): a separate probe
// answering "absent" leaves a window for somebody else to create the path first, and the
// `mkdir -p` that followed would happily accept their directory, changing neither its
// owner nor its mode.
//
// `mkdir` without `-p` is the whole fix: it fails when the path exists, so whoever created
// the directory is the one that owns it. On the sticky /dev/shm or /tmp a local user cannot
// remove ours to put theirs in its place, so winning the creation is winning outright.
// `umask 077` makes what we create private without a second `chmod` to race against — it
// does not cover the pre-creation case, since it only sets the mode of a directory it
// creates itself.
func workdirEnsureCmd(wd string) string {
	return fmt.Sprintf(`umask 077
if mkdir %[1]s 2>/dev/null; then echo ok; exit 0; fi
if [ ! -d %[1]s ]; then echo "unsafe $(stat -c '%%U:%%a' %[1]s 2>/dev/null) (not a directory)"; exit 0; fi
if [ -z "$(find %[1]s -maxdepth 0 -type d -user "$(id -un)" ! -perm /022 2>/dev/null)" ]; then
echo "unsafe $(stat -c '%%U:%%a' %[1]s 2>/dev/null)"; exit 0; fi
echo ok`, wd)
}

// agentState returns the word agentStateCmd answered: absent | ok | foreign … | stale …
//
// A probe that cannot answer refuses. Returning "absent" here would have been the natural
// reflex and is the wrong one: a guard that fails open reads as protection while granting
// the same execution, which is worse than having none. The three tools it needs —
// `find`, `stat -c`, `sha256sum` — are the ones the stdlib already requires of a target.
func agentState(cn conn, path, wantSum string) (string, error) {
	out, err := cn.run(posix(agentStateCmd(path, wantSum)), nil)
	if err != nil {
		return "", fmt.Errorf("cannot check the cached agent at %s: %w", path, err)
	}
	st := strings.TrimSpace(string(out))
	if st == "" {
		return "", fmt.Errorf("cannot check the cached agent at %s: the probe answered nothing", path)
	}
	return st, nil
}

func ensureWorkdir(cn conn, wd string) (string, error) {
	out, err := cn.run(posix(workdirEnsureCmd(wd)), nil)
	if err != nil {
		return "", fmt.Errorf("cannot check the workdir %s: %w", wd, err)
	}
	st := strings.TrimSpace(string(out))
	if st == "" {
		return "", fmt.Errorf("cannot check the workdir %s: the probe answered nothing", wd)
	}
	return st, nil
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

// pollWait is the FIRST interval between two "is the job done?" asks; every
// later wait doubles it, up to pollWaitMax. A plan that converges in tens of
// milliseconds must not be billed a whole second before anyone observes it
// (#573, measured in #464), and a plan that runs for minutes must not cost one
// SSH round trip per tick — the ramp buys both. Vars so tests can shrink them.
var (
	pollWait    = 25 * time.Millisecond
	pollWaitMax = time.Second
)

// nextPollWait widens an interval towards the ceiling; a zero interval — the
// state before the first ask — starts at pollWait.
func nextPollWait(d time.Duration) time.Duration {
	if d == 0 {
		d = pollWait
	} else {
		d *= 2
	}
	if d > pollWaitMax {
		return pollWaitMax
	}
	return d
}

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
	if _, err := cn.run(posixKeepingStdin(pushCmd(path)), bin); err != nil {
		return fmt.Errorf("push agent: %w", err)
	}
	return nil
}

// deposit writes the request atomically (tmp + mv).
func deposit(cn conn, wd, jobid string, req []byte) error {
	if _, err := cn.run(posixKeepingStdin(depositCmd(wd, jobid)), req); err != nil {
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

	var wait time.Duration
	backoff := func() {
		wait = nextPollWait(wait)
		time.Sleep(wait)
	}

	for time.Now().Before(deadline) {
		if cn == nil {
			c, err := s.dialConn()
			if err != nil {
				backoff()
				continue // survive: retry the dial
			}
			cn = c
		}
		data, ready, err := checkDone(cn, wd, jobid)
		if err != nil {
			_ = cn.close()
			cn = nil // dropped → re-dial next iteration
			backoff()
			continue
		}
		if ready {
			rmJob(cn, wd, jobid)
			return data, nil
		}
		backoff()
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
