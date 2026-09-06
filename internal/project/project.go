// Package project resolves the shellf project around a plan: the layout it must sit in
// (ADR-0038), the package's sibling defs (ADR-0014), the imports they name
// (ADR-0015/0016), and the def table the whole thing resolves against.
//
// Named for the project and not the plan because that is what it owns — and because
// `plan` is what the loaded value is called at every call site.
//
// Split out of `cmd/shellf` (#491/#587), which also parsed flags and exited the process.
// The def table is the argument for a package of its own rather than a bigger main:
// `lang` cannot import `std` — that is why CheckCycles takes a resolver — so something has
// to see both sets, and that something should not be the layer that calls os.Exit.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shellf/internal/lang"
	"shellf/internal/module"
	"shellf/internal/orchestrator"
	"shellf/internal/proto"
	"shellf/internal/std"
)

// ValidateArgs holds a host's resolved steps to what the defs declare. It is built where
// the def table is (loadPlanPackage) and handed to the orchestrator, which calls it after
// each host's expansion — the earliest moment a `${inventory.…}` value exists (#582).
type ValidateArgs func([]proto.Step) error

// Load reads the plan file together with its package — every other
// `*.shellf` file in the same directory (ADR-0014), so user defs written in a
// sibling file resolve by name. Returns the plan, the concatenated user def source to
// ship to the agent, and the per-host argument validator described below. baseVars is
// enriched in place with plan bindings.
func Load(planPath, invPath string, baseVars, setVars map[string]string) (orchestrator.Plan, map[string]string, ValidateArgs, error) {
	planSrc, err := os.ReadFile(planPath)
	if err != nil {
		return nil, nil, nil, err
	}
	root, err := Root(planPath)
	if err != nil {
		return nil, nil, nil, err
	}
	libs, err := packageLibs(root)
	if err != nil {
		return nil, nil, nil, err
	}
	imports, err := readImports(planPath, string(planSrc))
	if err != nil {
		return nil, nil, nil, err
	}
	plan, defs, err := lang.ParsePackage(string(planSrc), libs, imports, baseVars, setVars, stdSignatures())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %v", planPath, err)
	}
	// A call cycle is a writing error, refused from reading the files (ADR-0030 §6). Here
	// and not in `lang`, because the graph spans two sets no single package sees: these
	// user defs, and the stdlib, which `lang` cannot import. The evaluator keeps its own
	// guard, but it fires on the target, after earlier steps have already acted (#311).
	if err := lang.CheckCycles(defs, cycleResolver(defs)); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %v", planPath, err)
	}
	// A def's own argument guards, answered here when they can be (ADR-0056). Here for the
	// same reason as the cycles above: it needs the plan and both def sets. Only a `check`
	// that reaches nothing is evaluated, and only an `err` decides — everything else is
	// left to the evaluator on the target, which still runs every check.
	for _, b := range plan {
		if err := lang.CheckArguments(b.Steps, cycleResolver(defs)); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %v", planPath, err)
		}
	}
	// `file.template` steps are NOT resolved here: they render per host, in the
	// orchestrator, over each host's env (ADR-0024). See templateRenderer.
	//
	// `dir.copy` IS resolved here: its bytes are control-side and identical for
	// every host, so it expands once into dir-ensure + file-put steps (ADR-0028).
	// No control-side expansion left: `file.template` stopped being one in #334, and
	// `dir.copy` is a def over `~dir.sync` since #335. A plan now reaches the agent as
	// written.
	// Held to the defs again once each host has supplied its values: a `${inventory.…}`
	// argument does not exist until then, so the pass above deliberately left it alone
	// (#582, ADR-0056 §4). Same def table, same refusals, later.
	validate := func(steps []proto.Step) error {
		return lang.CheckResolvedArguments(steps, cycleResolver(defs))
	}
	return plan, defSource(defs), validate, nil
}

// readSubPackage reads one sub-package directory into libs, keyed `<name>/<file>`.
// A directory holding no `.shellf` file is ignored (it is content, not code — a
// `templates/` or `html/` tree next to a plan is ordinary). A directory that does hold
// code but nests another one is refused: ADR-0032 fixes exactly one dot per name, so a
// second level would produce `a.b.c`.
func readSubPackage(parent, name string, libs map[string]string) error {
	sub := filepath.Join(parent, name)
	entries, err := os.ReadDir(sub)
	if err != nil {
		return err
	}
	var code []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".shellf") {
			code = append(code, e)
		}
	}
	if len(code) == 0 {
		return nil // content directory, not a sub-package
	}
	for _, e := range entries {
		if e.IsDir() {
			return fmt.Errorf("%s: a sub-package may not contain a directory (%q) — one level only, ADR-0033", sub, e.Name())
		}
	}
	for _, e := range code {
		src, err := os.ReadFile(filepath.Join(sub, e.Name()))
		if err != nil {
			return err
		}
		libs[name+"/"+e.Name()] = string(src)
	}
	return nil
}

// readImports resolves each `import <alias> "<spec>"` in the plan to the def
// sources of that package. A spec with `@version` is a remote git module
// (ADR-0016), resolved through shellf.lock + the module cache; otherwise it is a
// local directory relative to the plan file (ADR-0015).
func readImports(planPath, planSrc string) (map[string][]string, error) {
	imps, err := lang.ScanImports(planSrc)
	if err != nil {
		return nil, err
	}
	if len(imps) == 0 {
		return nil, nil
	}
	// A local import path is relative to the plan file (ADR-0015, unchanged). The lock is
	// not: it pins what the *project* depends on, like go.sum, so it belongs at the root
	// rather than among the plans. ADR-0038 did not foresee this; recorded in the PR.
	dir := filepath.Dir(planPath)
	lockDir := dir
	if root, err := Root(planPath); err == nil {
		lockDir = root
	}
	lock, err := module.LoadLock(lockDir)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	lockChanged := false
	for _, imp := range imps {
		if spec, remote := module.ParseSpec(imp.Path); remote {
			srcs, changed, err := module.ResolveLocked(spec, moduleCache(), lock)
			if err != nil {
				return nil, fmt.Errorf("import %q: %v", imp.Alias, err)
			}
			out[imp.Alias] = srcs
			lockChanged = lockChanged || changed
			continue
		}
		importDir := filepath.Join(dir, imp.Path)
		info, err := os.Stat(importDir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("import %q: %q is not a directory", imp.Alias, imp.Path)
		}
		srcs, err := shellfSources(importDir)
		if err != nil {
			return nil, err
		}
		out[imp.Alias] = srcs
	}
	if lockChanged {
		if err := module.SaveLock(lockDir, lock); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// moduleCache is where fetched remote modules live, content-addressed by SHA
// (ADR-0016): ~/.cache/shellf/modules.
func moduleCache() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "shellf", "modules")
}

// shellfSources reads every `*.shellf` file in a directory (an imported package).
func shellfSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var srcs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".shellf") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, string(src))
	}
	return srcs, nil
}

// packageLibs reads every def package under `<root>/defs/` (ADR-0038 §2). Each
// `defs/<name>/` is one package, and its files are keyed `<name>/<file>` — the same key
// shape ADR-0033 already uses for sub-packages, so the parser qualifies the defs
// `<name>.<def>` with no further work.
//
// A plan's siblings are no longer defs. That is the convenience ADR-0014 §1 bought and
// this layout removes: a def is addressed by name, so it lives where the name says.
func packageLibs(root string) (map[string]string, error) {
	defsDir := filepath.Join(root, DirDefs)
	entries, err := os.ReadDir(defsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil // a project may have no defs of its own
		}
		return nil, err
	}
	libs := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			if strings.HasSuffix(e.Name(), ".shellf") {
				return nil, fmt.Errorf("%s: a def belongs to a package directory — "+
					"move it to defs/<package>/%s (ADR-0038)", filepath.Join(defsDir, e.Name()), e.Name())
			}
			continue
		}
		if err := readSubPackage(defsDir, e.Name(), libs); err != nil {
			return nil, err
		}
	}
	return libs, nil
}

// Project layout (ADR-0038). A plan lives in `<root>/plans/`, so the root is its parent —
// and `defs/`, `assets/` and `inventories/` hang off the same root. Directory names, not
// a marker file: the layout already identifies the project, and a second mechanism to say
// the same thing is one to keep in sync.
const (
	DirPlans  = "plans"
	DirDefs   = "defs"
	DirAssets = "assets"
)

// projectRoot returns the directory holding the layout, given the invoked plan.
//
// The error is most of this function's value: it is the first thing anyone running shellf
// outside a project will see, so it names the layout rather than reporting a file that
// could not be found somewhere inside the loader.
func Root(planPath string) (string, error) {
	abs, err := filepath.Abs(planPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if filepath.Base(dir) != DirPlans {
		return "", fmt.Errorf("%s is not inside a shellf project: a plan lives in `plans/`, "+
			"beside `defs/`, `assets/` and `inventories/` (ADR-0038)", planPath)
	}
	return filepath.Dir(dir), nil
}

// cycleResolver is the lookup a run uses — a package user def first, so an
// `override def` wins, then the stdlib (ADR-0014). Giving the cycle check the same order
// is the point: it must see the graph the run will walk, or it misses the case where a
// user def redirects a stdlib one back into itself.
func cycleResolver(defs map[string]lang.Def) lang.DefResolver {
	return func(name string) (lang.Def, bool) {
		if d, ok := defs[name]; ok {
			return d, true
		}
		return std.Lookup(name)
	}
}

// defSource maps each resolved instruction name to its def source, for the
// per-host Request — bare for the local package, qualified `alias.def` for
// imports (ADR-0014/0015).
func defSource(defs map[string]lang.Def) map[string]string {
	m := make(map[string]string, len(defs))
	for name, d := range defs {
		m[name] = d.Source
	}
	return m
}

// stdSignatures resolves an instruction's parameter names from the embedded stdlib —
// signatures live with the defs, self-hosting, so adding a def needs no parser-side edit
// (#107).
//
// There is no builtin table beside it any more. It held `file.copy` and `dir.copy` from
// when both were Go transformations; both became defs, and the stale entry shadowed the
// real signature — `dir.copy(%"src", dst, "sha256")` was documented and refused, because
// the table still said two parameters while the def had grown `compare` (#414). A
// signature written in two places is a signature that drifts.
func stdSignatures() lang.InstructionSig {
	return func(name string) ([]lang.Param, int, bool) {
		if def, ok := std.Lookup(name); ok {
			required := 0
			for _, p := range def.Params {
				if p.Default == nil {
					required++
				}
			}
			// The def's own parameters, types included: a signature written twice is a
			// signature that drifts (#414), and the type is what ADR-0045 checks against.
			return def.Params, required, true
		}
		return nil, 0, false
	}
}
