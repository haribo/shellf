package lang

import (
	"strings"
	"testing"
)

func TestInterpolation(t *testing.T) {
	src := `
owner = "haribo"
on server {
  dir.owner("/opt", "${owner}:${owner}")
  file.write("/f", """OWNER=${owner} DB=${DATABASE_URL}""")
}
`
	plan, err := parsePlan(src, map[string]string{}, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := plan[0].Steps
	if got := steps[0].Args["owner"]; got != "haribo:haribo" {
		t.Fatalf("simple string interpolated: got %q, want haribo:haribo", got)
	}
	// triple-quote is raw: both ${owner} and ${DATABASE_URL} stay verbatim
	if got := steps[1].Args["content"]; !strings.Contains(got, "${owner}") || !strings.Contains(got, "${DATABASE_URL}") {
		t.Fatalf("triple-quote must stay raw, got %q", got)
	}
}

func TestInterpolationUndefined(t *testing.T) {
	if _, err := parsePlan(`on s { dir.owner("/opt", "${missing}") }`, map[string]string{}, nil, defaultSig, nil, nil); err == nil {
		t.Fatal("expected error for undefined interpolation variable")
	}
}

func TestInterpolationInBinding(t *testing.T) {
	// a binding's value may itself interpolate an earlier binding (at parse)
	base := map[string]string{}
	plan, err := parsePlan("owner = \"haribo\"\npair = \"${owner}:${owner}\"\non s { dir.owner(\"/opt\", pair) }", base, nil, defaultSig, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// the binding's value is interpolated at parse and stored in baseVars
	if base["pair"] != "haribo:haribo" {
		t.Fatalf("binding interpolation: %+v", base)
	}
	// used as a bare identifier → a ref to `pair`, resolved later
	if got := plan[0].Steps[0].Refs["owner"]; got != "pair" {
		t.Fatalf("ref: %+v", plan[0].Steps[0])
	}
}

// ADR-0052: `${inventory.<field>}` names a field of the host a step will run on, and that
// host is unknown while the plan is read — so the interpolation is left for the
// orchestrator instead of failing.
//
// The prefix is still checked here, which is what keeps a typo cheap: `${inventroy.domain}`
// is an undefined name and fails at parse. Only the *field* is late, and that is already
// true of a bare reference (ADR-0003 §4).
func TestInterpolate_DefersInventoryFields(t *testing.T) {
	globals := func(n string) (string, bool) {
		v, ok := map[string]string{"owner": "haribo"}[n]
		return v, ok
	}

	for name, tc := range map[string]struct {
		in       string
		want     string
		deferred bool
		err      string
	}{
		"a global still resolves at parse": {
			in: "${owner}:${owner}", want: "haribo:haribo",
		},
		"an inventory field is left verbatim": {
			in: "https://${inventory.domain}/healthz", want: "https://${inventory.domain}/healthz", deferred: true,
		},
		"globals and inventory fields mix": {
			in: "${owner}@${inventory.domain}", want: "haribo@${inventory.domain}", deferred: true,
		},
		"a typo in the prefix is a plain undefined name": {
			in: "${inventroy.domain}", err: `undefined variable "inventroy.domain"`,
		},
		"the private key path is refused": {
			in: "${inventory.key}", err: "path to a private key",
		},
		"the prefix alone names no field": {
			in: "${inventory.}", err: "names no field",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, deferred, err := InterpolateDeferring(tc.in, globals)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want an error containing %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if deferred != tc.deferred {
				t.Fatalf("deferred: got %v, want %v", deferred, tc.deferred)
			}
		})
	}
}
