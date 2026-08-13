package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shellf/internal/proto"
)

// file.template became a def over the primitives (#334), so the properties the Go
// transformation guaranteed move here. Each of these replaces a test that guarded
// `renderTemplates`, which no longer exists.

// Replaces TestRun_RendersTemplatesPerHost: substitution uses the host's variables, and
// two hosts with different values get different content. This is why rendering runs on
// the control host at all.
func TestTemplateDef_RendersPerHost(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "motd.tmpl"), []byte("host = @{who}"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(planDir, "motd.tmpl")

	for _, host := range []struct{ name, who string }{{"web1", "one"}, {"web2", "two"}} {
		t.Run(host.name, func(t *testing.T) {
			ch := wire(t, planDir, []string{"file.read:" + src}, map[string]string{"who": host.who})
			f := newComp()
			f.set(`printf '%s' "$c"`, 0)
			resp := serveCompCh(t, f, ch, proto.Request{
				Mode: "apply",
				Defs: map[string]string{
					"t": `def t(src: str) { apply { c = ~file.render(~file.read(src)) shell { printf '%s' "$c" } } }`,
				},
				Steps: []proto.Step{{Instruction: "t", Args: map[string]string{"src": src}, Control: []string{"src"}}},
			})
			if resp.Results[0].Category == "err" {
				t.Fatalf("%+v", resp.Results[0])
			}
			if got := f.envFor(`printf '%s' "$c"`)["c"]; got != "host = "+host.who {
				t.Fatalf("each host must render its own value: got %q", got)
			}
		})
	}
}

// Replaces the error cases of TestRenderTemplates: a template naming a variable the
// host does not have fails, rather than delivering a file with a hole in it.
func TestTemplateDef_UndefinedVariableFails(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "x.tmpl"), []byte("v = @{nope}"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(planDir, "x.tmpl")
	ch := wire(t, planDir, []string{"file.read:" + src}, map[string]string{"other": "1"})

	resp := serveCompCh(t, newComp(), ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"t": `def t(src: str) { apply { c = ~file.render(~file.read(src)) } }`,
		},
		Steps: []proto.Step{{Instruction: "t", Args: map[string]string{"src": src}, Control: []string{"src"}}},
	})
	if resp.Results[0].Category != "err" {
		t.Fatalf("an undefined variable must fail: %+v", resp.Results[0])
	}
	msg := ""
	if resp.Results[0].Shell != nil {
		msg = resp.Results[0].Shell.Stderr
	}
	if !strings.Contains(msg, "nope") {
		t.Fatalf("the failure must name the variable: %q", msg)
	}
}

// Replaces TestRenderTemplates_PreservesBindAndCaught (#246): a captured result and `?`
// still work now that file.template is a def, since it is an ordinary instruction.
func TestTemplateDef_CaptureAndCaught(t *testing.T) {
	planDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(planDir, "c.tmpl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(planDir, "c.tmpl")
	ch := wire(t, planDir, []string{"file.read:" + src}, nil)

	f := newComp()
	f.set(`echo handler`, 0)
	resp := serveCompCh(t, f, ch, proto.Request{
		Mode: "apply",
		Defs: map[string]string{
			"t": `def t(src: str, dst: str) { apply { ~file.write(dst, ~file.render(~file.read(src))) return ok.written } }`,
		},
		Steps: []proto.Step{
			{Instruction: "t", Bind: "r", Args: map[string]string{
				"src": src, "dst": filepath.Join(t.TempDir(), "out"),
			}, Control: []string{"src"}},
			{If: &proto.IfBlock{
				CondRef: &proto.ResultRef{Name: "r", Changed: true},
				Then:    []proto.Step{{Instruction: "shell", Args: map[string]string{"cmd": "echo handler"}}},
			}},
		},
	})
	if resp.Results[0].Category == "err" {
		t.Fatalf("%+v", resp.Results[0])
	}
	if !f.called(`echo handler`) {
		t.Fatal("a captured result must still gate a handler (#246)")
	}
}
