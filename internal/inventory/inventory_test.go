package inventory

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestResolve_DefaultsFillOmittedFields(t *testing.T) {
	inv := Inventory{
		Defaults: Host{User: "deploy", Port: "22", Key: "/k", Interpreter: "bash"},
		Hosts: map[string]Host{
			"web": {Address: "10.0.0.1"}, // omits everything but the address
		},
	}
	h, ok := inv.Resolve("web")
	if !ok {
		t.Fatal("web must resolve")
	}
	if h.User != "deploy" || h.Port != "22" || h.Key != "/k" || h.Interpreter != "bash" {
		t.Fatalf("defaults not applied: %+v", h)
	}
	if h.Address != "10.0.0.1" {
		t.Fatalf("address must be preserved: %+v", h)
	}
}

func TestResolve_HostOverridesDefaults(t *testing.T) {
	inv := Inventory{
		Defaults: Host{User: "deploy", Port: "22", Key: "/default", Interpreter: "sh"},
		Hosts: map[string]Host{
			"db": {Address: "10.0.0.2", User: "root", Port: "2222", Key: "/db", Interpreter: "bash"},
		},
	}
	h, _ := inv.Resolve("db")
	if h.User != "root" || h.Port != "2222" || h.Key != "/db" || h.Interpreter != "bash" {
		t.Fatalf("host values must win over defaults: %+v", h)
	}
}

func TestResolve_UnknownAlias(t *testing.T) {
	inv := Inventory{Hosts: map[string]Host{"web": {Address: "x"}}}
	if _, ok := inv.Resolve("nope"); ok {
		t.Fatal("an unknown alias must not resolve")
	}
}

func TestResolve_VarsMergeHostOverDefaults(t *testing.T) {
	inv := Inventory{
		Defaults: Host{Vars: map[string]string{"pkg": "nginx", "env": "prod"}},
		Hosts: map[string]Host{
			"web": {Address: "x", Vars: map[string]string{"pkg": "apache", "webroot": "/srv"}},
		},
	}
	h, _ := inv.Resolve("web")
	want := map[string]string{"pkg": "apache", "env": "prod", "webroot": "/srv"}
	if !reflect.DeepEqual(h.Vars, want) {
		t.Fatalf("vars merge (host over defaults): got %v want %v", h.Vars, want)
	}
}

func TestResolve_NilVars_YieldsEmptyMap(t *testing.T) {
	inv := Inventory{Hosts: map[string]Host{"web": {Address: "x"}}}
	h, _ := inv.Resolve("web")
	if h.Vars == nil || len(h.Vars) != 0 {
		t.Fatalf("resolve must yield a non-nil empty var map, got %v", h.Vars)
	}
}

func TestMembers(t *testing.T) {
	inv := Inventory{
		Hosts:  map[string]Host{"a": {}, "b": {}, "c": {}},
		Groups: map[string][]string{"web": {"a", "b"}},
	}
	if got, _ := inv.Members("web"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("group expands to its aliases: %v", got)
	}
	if got, _ := inv.Members("c"); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("a lone host is a singleton group: %v", got)
	}
	if got, _ := inv.Members("ghost"); got != nil {
		t.Fatalf("an unknown target expands to nothing: %v", got)
	}
}

// A target nobody declared and a group declared empty both expand to nothing, and
// the caller must still be able to tell them apart: the first is a typo to refuse,
// the second is a legitimate no-op. Collapsing them into one `nil` is how `on nope`
// reported success while touching no host (#451).
func TestMembers_UnknownIsNotEmpty(t *testing.T) {
	inv := Inventory{
		Hosts:  map[string]Host{"a": {}},
		Groups: map[string][]string{"empty": {}},
	}
	if _, known := inv.Members("empty"); !known {
		t.Fatal("a declared group is known, even with no members")
	}
	if _, known := inv.Members("ghost"); known {
		t.Fatal("a target nobody declared must not be reported as known")
	}
	if _, known := inv.Members("a"); !known {
		t.Fatal("a declared host is a known target")
	}
}

func TestMembers_GroupWinsOverSameNamedHost(t *testing.T) {
	// A name that is both a group and a host resolves as the group (checked first).
	inv := Inventory{
		Hosts:  map[string]Host{"all": {}, "x": {}},
		Groups: map[string][]string{"all": {"x"}},
	}
	got, _ := inv.Members("all")
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("group must win over a same-named host: %v", got)
	}
}

// A group listing an alias no `host` declares used to reach the transport with an
// empty address and fail as `host key for :22 not in known_hosts` — an inventory
// error reported as an SSH one (#451). It is caught at load, naming both ends.
func TestValidate_GroupMemberMustBeADeclaredHost(t *testing.T) {
	inv := Inventory{
		Hosts:  map[string]Host{"a": {}},
		Groups: map[string][]string{"ghosts": {"a", "nobody"}},
	}
	err := inv.Validate()
	if err == nil {
		t.Fatal("an undeclared alias in a group must be refused at load")
	}
	if !strings.Contains(err.Error(), "nobody") || !strings.Contains(err.Error(), "ghosts") {
		t.Fatalf("the error must name the alias and the group holding it: %v", err)
	}
}

func TestValidate_AcceptsAWellFormedInventory(t *testing.T) {
	inv := Inventory{
		Hosts:  map[string]Host{"a": {}, "b": {}},
		Groups: map[string][]string{"web": {"a", "b"}, "empty": {}},
	}
	if err := inv.Validate(); err != nil {
		t.Fatalf("a well-formed inventory must validate, including an empty group: %v", err)
	}
}
