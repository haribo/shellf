package transport

import (
	"strings"
	"testing"
	"time"
)

func TestRemotePath_DeterministicByBytesAndUser(t *testing.T) {
	s := SSH{User: "deploy"}
	a := s.remotePath([]byte("binary-v1"))
	b := s.remotePath([]byte("binary-v1"))
	c := s.remotePath([]byte("binary-v2"))

	if a != b {
		t.Fatalf("same bytes+user must give the same cache path: %s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("different bytes must give different cache paths: %s == %s", a, c)
	}
	if !strings.HasPrefix(a, "/tmp/shellf-agent-") {
		t.Fatalf("unexpected path: %s", a)
	}
	// Different SSH user → different agent path (issue #114): no cross-user reuse.
	if root := (SSH{User: "root"}).remotePath([]byte("binary-v1")); root == a {
		t.Fatalf("different users must not share an agent path: %s", root)
	}
}

func TestPaths(t *testing.T) {
	s := SSH{User: "deploy"}
	// The workdir hangs off the chosen tmpfs base (ADR-0025), not /tmp/shellf-agent-.
	wd := s.workDir("/dev/shm", []byte("bin"))
	if !strings.HasPrefix(wd, "/dev/shm/shellf-") {
		t.Fatalf("workDir on tmpfs: %s", wd)
	}
	if fb := s.workDir("/tmp", []byte("bin")); !strings.HasPrefix(fb, "/tmp/shellf-") || strings.HasPrefix(fb, "/tmp/shellf-agent-") {
		t.Fatalf("workDir /tmp fallback: %s", fb)
	}
	if got := donePath("/w", "7"); got != "/w/done-7" {
		t.Fatalf("donePath: %s", got)
	}
	if got := outPath("/w", "7"); got != "/w/out-7.json" {
		t.Fatalf("outPath: %s", got)
	}
}

func TestCommandBuilders(t *testing.T) {
	cases := []struct{ got, want string }{
		{pushCmd("/p"), "cat > /p.tmp && chmod +x /p.tmp && mv /p.tmp /p"},
		{depositCmd("/w", "7"), "umask 077 && mkdir -p /w && cat > /w/req-7.json.tmp && mv /w/req-7.json.tmp /w/req-7.json"},
		{launchCmd("/p", "/w", 7200), "setsid /p __agent-resident /w 7200 >/dev/null 2>&1 </dev/null &"},
		{checkDoneCmd("/w", "7"), "if test -f /w/done-7; then cat /w/out-7.json; else printf __NOTDONE__; fi"},
		{rmJobCmd("/w", "7"), "rm -f /w/out-7.json /w/done-7"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Fatalf("builder:\n got %q\nwant %q", c.got, c.want)
		}
	}
	if !strings.Contains(agentAliveCmd("/w"), "/w/agent.pid") || !strings.Contains(agentAliveCmd("/w"), "__agent-resident") {
		t.Fatalf("agentAliveCmd: %s", agentAliveCmd("/w"))
	}
	if c := cleanCmd(); !strings.Contains(c, "/tmp/shellf-*") || !strings.Contains(c, "/dev/shm/shellf-*") || !strings.Contains(c, "kill") {
		t.Fatalf("cleanCmd must cover both roots: %s", c)
	}
}

func TestParseDone(t *testing.T) {
	if _, ready := parseDone([]byte("__NOTDONE__")); ready {
		t.Fatal("the sentinel must read as not-ready")
	}
	out, ready := parseDone([]byte(`{"ok":true}`))
	if !ready || string(out) != `{"ok":true}` {
		t.Fatalf("payload should be ready and passed through: %q ready=%v", out, ready)
	}
}

func TestSanitizeUser(t *testing.T) {
	// sanitizeUser works byte-wise, so a 2-byte rune yields 2 underscores.
	cases := map[string]string{"": "nouser", "deploy": "deploy", "a.b/c": "a_b_c", "a b": "a_b", "u-1_2": "u-1_2"}
	for in, want := range cases {
		if got := sanitizeUser(in); got != want {
			t.Fatalf("sanitizeUser(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaults(t *testing.T) {
	z := SSH{}
	if z.port() != "22" || z.timeout() != 10*time.Second || z.execTimeout() != 30*time.Minute || z.agentTTLSecs() != 7200 {
		t.Fatalf("defaults: port=%s timeout=%s exec=%s ttl=%d", z.port(), z.timeout(), z.execTimeout(), z.agentTTLSecs())
	}
	o := SSH{Port: "2222", Timeout: time.Second, ExecTimeout: time.Minute, AgentTTL: 90 * time.Second}
	if o.port() != "2222" || o.timeout() != time.Second || o.execTimeout() != time.Minute || o.agentTTLSecs() != 90 {
		t.Fatalf("overrides not honored: %+v", o)
	}
}

func TestNewJobID_Unique(t *testing.T) {
	a, b := newJobID(), newJobID()
	if a == b {
		t.Fatalf("job ids must be unique: %s == %s", a, b)
	}
	if strings.Count(a, "-") != 2 {
		t.Fatalf("job id format pid-nanos-counter: %s", a)
	}
}
