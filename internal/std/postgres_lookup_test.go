package std

import "testing"

// #545: the defs resolve under their qualified names. The embed glob (`*/*.shellf`) makes a
// new package appear on its own, which is what a package added to a list used to fail to do
// — the file was on disk, the def parsed, and Lookup returned nothing.
func TestPostgresDefsResolve(t *testing.T) {
	for _, name := range []string{"postgres.role", "postgres.database"} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("%s does not resolve", name)
		}
	}
}
