package agent

import (
	"testing"

	"shellf/internal/proto"
)

// #323: content validation lives in the `check` phase of an instruction that knows the
// format, which then composes with file.write (ADR-0030). This is what replaces the
// `validate` parameter, so it is asserted rather than assumed.
func TestValidationViaCheckPhase(t *testing.T) {
	// `sudo.write`-shaped: its check refuses the content, its apply would call
	// file.write. A refused check must mean nothing is written.
	sudoDef := `def sw(name: str, content: str) {
	    check {
	        r = shell { visudo-stub }
	        if !r { return err.validation(r) }
	    }
	    apply { shell { would-write "$name" } return ok.done }
	}`

	t.Run("a refused content never reaches the write", func(t *testing.T) {
		f := newComp()
		f.set(`visudo-stub`, 1) // the checker rejects it
		resp := serveComp(t, f, proto.Request{
			Mode:  "apply",
			Defs:  map[string]string{"sw": sudoDef},
			Steps: []proto.Step{{Instruction: "sw", Args: map[string]string{"name": "x", "content": "bad"}}},
		})
		if resp.Results[0].Category != "err" {
			t.Fatalf("a refused check must fail the step: %+v", resp.Results[0])
		}
		if f.called(`would-write "$name"`) {
			t.Fatal("apply must not run when check refused the content")
		}
	})

	t.Run("an accepted content is written", func(t *testing.T) {
		f := newComp()
		f.set(`visudo-stub`, 0)
		f.set(`would-write "$name"`, 0)
		resp := serveComp(t, f, proto.Request{
			Mode:  "apply",
			Defs:  map[string]string{"sw": sudoDef},
			Steps: []proto.Step{{Instruction: "sw", Args: map[string]string{"name": "x", "content": "ok"}}},
		})
		if resp.Results[0].Category != "ok" {
			t.Fatalf("an accepted content must proceed: %+v", resp.Results[0])
		}
		if !f.called(`would-write "$name"`) {
			t.Fatal("apply must run once the content is accepted")
		}
	})

	// The point of #313, now free: `check` already runs in check mode, so a bad config
	// is caught before any real run — with nothing written to the target.
	t.Run("validation happens in check mode too", func(t *testing.T) {
		f := newComp()
		f.set(`visudo-stub`, 1)
		resp := serveComp(t, f, proto.Request{
			Mode:  "check",
			Defs:  map[string]string{"sw": sudoDef},
			Steps: []proto.Step{{Instruction: "sw", Args: map[string]string{"name": "x", "content": "bad"}}},
		})
		if resp.Results[0].Category != "err" {
			t.Fatalf("--check must surface a refused content: %+v", resp.Results[0])
		}
		if f.called(`would-write "$name"`) {
			t.Fatal("check mode must not run apply")
		}
	})
}
