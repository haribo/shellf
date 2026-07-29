// Package std is the embedded standard library: instructions written in shellf
// (def), shipped inside the binary — the Go-modules model (the stdlib travels
// with the language, no import). The Go core keeps only `shell` + the engine;
// everything here is a def.
package std

import (
	_ "embed"
	"fmt"
	"sync"

	"shellf/internal/lang"
)

//go:embed apt.shellf
var aptSrc string

//go:embed file.shellf
var fileSrc string

//go:embed archive.shellf
var archiveSrc string

var (
	once sync.Once
	defs map[string]lang.Def
)

func load() {
	defs = map[string]lang.Def{}
	for _, src := range []string{aptSrc, fileSrc, archiveSrc} {
		parsed, err := lang.ParseDefs(src)
		if err != nil {
			panic(fmt.Sprintf("std: parsing embedded def: %v", err))
		}
		for _, d := range parsed {
			defs[d.Name] = d
		}
	}
}

// Lookup returns the stdlib def for an instruction name, if any.
func Lookup(name string) (lang.Def, bool) {
	once.Do(load)
	d, ok := defs[name]
	return d, ok
}
