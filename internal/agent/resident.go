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
// cleans up (workdir + its own binary) and exits.
func ServeResident(workdir string, ex engine.Executor, ttl time.Duration) error {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(workdir, "agent.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	_ = os.Remove(filepath.Join(workdir, "lock")) // release the launch lock once up

	last := time.Now()
	for {
		if req := nextRequest(workdir); req != "" {
			processJob(workdir, req, ex)
			last = time.Now()
			continue
		}
		if time.Since(last) > ttl {
			cleanup(workdir)
			return nil
		}
		time.Sleep(pollInterval)
	}
}

// nextRequest returns one ready request path (req-*.json, ignoring .tmp), or "".
func nextRequest(workdir string) string {
	entries, _ := os.ReadDir(workdir)
	for _, e := range entries {
		if name := e.Name(); strings.HasPrefix(name, "req-") && strings.HasSuffix(name, ".json") {
			return filepath.Join(workdir, name)
		}
	}
	return ""
}

// processJob runs a request and writes out-<id>.json (atomically) + done-<id>,
// then removes the request. The control polls done-<id> and reads out-<id>.
func processJob(workdir, reqPath string, ex engine.Executor) {
	id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(reqPath), "req-"), ".json")

	var resp proto.Response
	data, err := os.ReadFile(reqPath)
	if err != nil {
		os.Remove(reqPath)
		return
	}
	var req proto.Request
	if err := json.Unmarshal(data, &req); err != nil {
		resp.Error = "decode: " + err.Error()
	} else {
		results, halted := runSteps(req.Steps, ex, mode(req.Mode), map[string]engine.Result{})
		resp = proto.Response{Results: results, Halted: halted}
	}

	out, _ := json.Marshal(resp)
	tmp := filepath.Join(workdir, "out-"+id+".json.tmp")
	_ = os.WriteFile(tmp, out, 0o600)
	_ = os.Rename(tmp, filepath.Join(workdir, "out-"+id+".json")) // atomic
	_ = os.WriteFile(filepath.Join(workdir, "done-"+id), []byte("0"), 0o600)
	_ = os.Remove(reqPath) // consumed
}

// cleanup removes the workdir (residues) on self-kill. The cached BINARY is
// deliberately KEPT — deleting it would force a re-transfer on the next run
// (e.g. a morning run then an evening run). The binary is purged separately
// (an explicit `clean`, or reboot clearing /tmp). See ADR-0005.
func cleanup(workdir string) {
	os.RemoveAll(workdir)
}
