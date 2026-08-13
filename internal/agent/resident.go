package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shellf/internal/engine"
	"shellf/internal/proto"
)

const pollInterval = 200 * time.Millisecond

// ServeResident runs the agent as a detached resident (ADR-0005): it watches
// workdir for request files, processes each, and after `ttl` of inactivity
// erases everything (workdir + its own binary at binPath) and exits — leaving
// no trace of shellf. binPath is injectable so tests don't delete themselves.
func ServeResident(workdir, binPath string, ex engine.Executor, ttl time.Duration) error {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(workdir, "agent.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)

	// The control channel (ADR-0031). Best-effort: a target where the socket cannot be
	// created still runs every plan that asks nothing of the control host, which is
	// almost all of them. Failing the agent outright would trade a working majority for
	// a feature the plan may never use.
	ch, cherr := Listen(workdir)
	if cherr == nil {
		defer func() { _ = ch.Close() }()
	}

	last := time.Now()
	for {
		if req := nextRequest(workdir); req != "" {
			processJob(workdir, req, ex, ch)
			last = time.Now()
			continue
		}
		if time.Since(last) > ttl {
			cleanup(workdir, binPath)
			return nil
		}
		time.Sleep(pollInterval)
	}
}

// nextRequest atomically claims one ready request (req-*.json → .claiming via
// rename) and returns the claimed path, or "". The rename makes the claim safe
// if two agents ever race: only one wins, so no request runs twice.
func nextRequest(workdir string) string {
	entries, _ := os.ReadDir(workdir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "req-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		src := filepath.Join(workdir, name)
		claimed := src + ".claiming"
		if os.Rename(src, claimed) == nil {
			return claimed
		}
		// lost the race (another agent claimed it) — try the next
	}
	return ""
}

// processJob runs a claimed request (req-<id>.json.claiming) and writes
// out-<id>.json (atomically) + done-<id>, then removes the claimed file.
func processJob(workdir, reqPath string, ex engine.Executor, ch *Channel) {
	base := strings.TrimSuffix(filepath.Base(reqPath), ".claiming")
	id := strings.TrimSuffix(strings.TrimPrefix(base, "req-"), ".json")

	var resp proto.Response
	data, err := os.ReadFile(reqPath)
	if err != nil {
		_ = os.Remove(reqPath)
		return
	}
	var req proto.Request
	if err := json.Unmarshal(data, &req); err != nil {
		resp.Error = "decode: " + err.Error()
	} else {
		resp = runRequest(req, ex, ch) // shared path: pre-flight + run (ADR-0012)
	}

	out, _ := json.Marshal(resp)
	tmp := filepath.Join(workdir, "out-"+id+".json.tmp")
	_ = os.WriteFile(tmp, out, 0o600)
	_ = os.Rename(tmp, filepath.Join(workdir, "out-"+id+".json")) // atomic
	_ = os.WriteFile(filepath.Join(workdir, "done-"+id), []byte("0"), 0o600)
	_ = os.Remove(reqPath) // consumed
}

// cleanup erases everything on self-kill: the workdir (residues) and the agent
// binary → zero trace of shellf after inactivity (ADR-0005). The long, settable
// TTL — not a kept binary — is what avoids re-transfer during activity.
func cleanup(workdir, binPath string) {
	_ = os.RemoveAll(workdir)
	if binPath != "" {
		_ = os.Remove(binPath)
	}
}
