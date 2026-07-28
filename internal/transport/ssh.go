package transport

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSH pushes and runs the ephemeral agent over a single golang.org/x/crypto/ssh
// connection (one TCP dial, multiple sessions). Hardened over the earlier
// shell-out: dial timeout + an exec watchdog, no per-host subprocess fork,
// host-key verification against known_hosts.
type SSH struct {
	User        string
	Host        string
	Port        string        // empty = 22
	Key         string        // identity file
	Timeout     time.Duration // dial (connect) timeout; 0 = 10s
	ExecTimeout time.Duration // push+exec watchdog; 0 = 5m
	KnownHosts  string        // known_hosts path; empty = ~/.ssh/known_hosts
	Insecure    bool          // bypass host-key verification (dev only)
}

// remotePath is per-process to avoid ETXTBSY against a binary a prior (killed)
// run may have left executing on the target.
func remotePath() string {
	return fmt.Sprintf("/tmp/shellf-agent-%d", os.Getpid())
}

func (s SSH) Run(agentBin string, req []byte) ([]byte, error) {
	bin, err := os.ReadFile(agentBin)
	if err != nil {
		return nil, fmt.Errorf("read agent: %w", err)
	}

	client, err := s.dial()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	// Watchdog on the whole push+exec (an apt install can take minutes).
	stop := time.AfterFunc(s.execTimeout(), func() { client.Close() })
	defer stop.Stop()

	if err := s.push(client, bin); err != nil {
		return nil, err
	}
	return s.exec(client, req)
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

// push streams the binary to the target (stdin of a `cat` session).
func (s SSH) push(client *ssh.Client, bin []byte) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	sess.Stdin = bytes.NewReader(bin)
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	path := remotePath()
	if err := sess.Run("cat > " + path + " && chmod +x " + path); err != nil {
		return fmt.Errorf("push agent: %v: %s", err, stderr.String())
	}
	return nil
}

// exec runs the agent (stdin = req), captures stdout, then removes the binary.
func (s SSH) exec(client *ssh.Client, req []byte) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	sess.Stdin = bytes.NewReader(req)
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	cmd := fmt.Sprintf("%[1]s __agent; r=$?; rm -f %[1]s; exit $r", remotePath())
	if err := sess.Run(cmd); err != nil {
		return nil, fmt.Errorf("agent run: %v: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
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
		return 5 * time.Minute
	}
	return s.ExecTimeout
}
