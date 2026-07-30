package fleet

import (
	"testing"

	"shellf/internal/transport"
)

// fakeTransport returns a canned agent-response body per target.
type fakeTransport struct{ body []byte }

func (f fakeTransport) Run(_ string, _ []byte) ([]byte, error) { return f.body, nil }

var _ transport.Transport = fakeTransport{}

func TestRun_CollectsPerHostInOrder(t *testing.T) {
	bodies := map[string][]byte{
		"h1": []byte(`{"results":[{"label":"apt-install(nginx)","category":"would","tag":"installed"}]}`),
		"h2": []byte(`{"results":[{"label":"apt-install(nginx)","category":"ok","tag":"alreadyInstalled"}]}`),
		"h3": []byte(`{"results":[{"label":"apt-install(nginx)","category":"would","tag":"installed"}]}`),
	}
	dial := func(target string) transport.Transport {
		return fakeTransport{body: bodies[target]}
	}

	reqFor := func(string) ([]byte, error) { return []byte(`{}`), nil }
	res := Run([]string{"h1", "h2", "h3"}, "/bin/agent", reqFor, dial)

	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
	if res[0].Target != "h1" || res[1].Target != "h2" || res[2].Target != "h3" {
		t.Fatalf("order not preserved: %+v", res)
	}

	tally := map[string]int{}
	for _, hr := range res {
		s := hr.Response.Results[0]
		tally[s.Category+"."+s.Tag]++
	}
	if tally["would.installed"] != 2 || tally["ok.alreadyInstalled"] != 1 {
		t.Fatalf("bad tally: %v", tally)
	}
}
