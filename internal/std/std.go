// Package std is the embedded standard library: instructions written in shellf
// (def), shipped inside the binary — the Go-modules model (the stdlib travels
// with the language). The Go core keeps only `shell` + the engine; everything
// here is a def.
//
// Layout is by package: root `*.shellf` files are the unqualified `std` package
// (`file-download`, `archive-extract`, …); each subdirectory `<pkg>/` is a
// qualified package whose defs are prefixed `<pkg>.` (e.g. `docker/` →
// `docker.install`). Qualification is independent of location: an embedded
// package can move to an external repo later without changing how it is called.
package std

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"shellf/internal/lang"
)

//go:embed apt/*.shellf archive/*.shellf dir/*.shellf docker/*.shellf file/*.shellf
//go:embed git/*.shellf http/*.shellf service/*.shellf systemd/*.shellf ufw/*.shellf
//go:embed sshd/*.shellf sudo/*.shellf user/*.shellf
var files embed.FS

var (
	once sync.Once
	defs map[string]lang.Def
)

func load() {
	defs = map[string]lang.Def{}
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".shellf") {
			return nil
		}
		src, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		parsed, err := lang.ParseDefs(string(src))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		prefix := "" // root files are the unqualified `std` package
		if dir := path.Dir(p); dir != "." {
			prefix = path.Base(dir) + "." // subdirectory → qualified package
		}
		for _, def := range parsed {
			defs[prefix+def.Name] = def
		}
		return nil
	})
	if err != nil {
		panic("std: loading embedded defs: " + err.Error())
	}
}

// Lookup returns the def for an instruction name (bare `file-download` or
// qualified `docker.install`), if any.
func Lookup(name string) (lang.Def, bool) {
	once.Do(load)
	d, ok := defs[name]
	return d, ok
}
