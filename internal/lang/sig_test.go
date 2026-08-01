package lang

// The parser tests cannot import std (that would be an import cycle: std imports
// lang), so they supply the stdlib signatures here. Production derives the same
// from std.Lookup + builtins in cmd/shellf (#107).
func init() { defaultSig = testStdSig }

func testStdSig(name string) ([]string, bool) {
	m := map[string][]string{
		"apt.install":       {"pkg"},
		"file-copy":         {"src", "dst"},
		"service":           {"name", "running", "enabled"},
		"file-download":     {"url", "dst", "sha256"},
		"archive-extract":   {"src", "dst"},
		"git-clone":         {"url", "dst"},
		"dir-ensure":        {"path"},
		"dir-exists":        {"path"},
		"dir-owner":         {"path", "owner"},
		"file-exists":       {"path"},
		"user-group":        {"user", "group"},
		"file-write":        {"path", "content"},
		"file-line":         {"path", "line"},
		"file-delete":       {"path"},
		"docker.install":    {},
		"docker.network":    {"name"},
		"docker.compose-up": {"dir"},
		"ufw.open":          {"port", "proto"},
		"ufw.enable":        {},
	}
	p, ok := m[name]
	return p, ok
}
