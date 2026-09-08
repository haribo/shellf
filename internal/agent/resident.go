package agent

import (
	"encoding/json"
	"fmt"
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
	// Defence in depth (#413). The control host creates this directory exclusively and
	// vets it before launching us, so reaching here with a workdir somebody else can write
	// means that guard was bypassed or the directory changed under it. MkdirAll above does
	// not fix an existing directory's mode, and this agent executes every `req-*.json` it
	// finds — including steps that escalate. Refuse rather than serve.
	if err := ownedAndUnwritable(workdir); err != nil {
		return fmt.Errorf("refusing to serve from %s: %v", workdir, err)
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

	// The marker follows the result, and only if the result is there. Both writes used to
	// discard their error and `done-<id>` was written unconditionally, so a workdir that
	// could not take the result still announced one: the control host `cat`s a file that is
	// not there and reports `unexpected end of JSON input` for a job that ran (#600). The
	// workdir is a tmpfs (ADR-0025), where a full filesystem takes a kilobyte-sized result
	// and still accepts a one-byte marker — which is exactly how the two come apart.
	if err := writeResult(workdir, id, resp); err != nil {
		// The full result did not fit or could not be written. A short one carrying the
		// reason usually still can, and it turns a timeout into an answer that names the
		// cause. If even that fails, no marker is written: the job stays un-done and the
		// run ends on its own deadline, which is slow but never wrong.
		short := proto.Response{Error: "the agent could not write its result: " + err.Error()}
		if err2 := writeResult(workdir, id, short); err2 != nil {
			_ = os.Remove(reqPath)
			return
		}
	}
	_ = os.WriteFile(filepath.Join(workdir, "done-"+id), []byte("0"), 0o600)
	_ = os.Remove(reqPath) // consumed
}

// writeResult stores a response as out-<id>.json, atomically: the control host reads that
// file the moment the marker appears, so it must never see a half-written one.
func writeResult(workdir, id string, resp proto.Response) error {
	out, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	tmp := filepath.Join(workdir, "out-"+id+".json.tmp")
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(workdir, "out-"+id+".json")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
