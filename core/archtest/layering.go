// file: core/archtest/layering.go
// version: 1.0.0
// guid: 68203cee-7b76-4b17-b97c-0ff1f83e61ea
// last-edited: 2026-11-05

// Package archtest holds reusable architecture guards that core and every app
// run in their own tests (03 §3.5). It is a test-support package (03 R4): it
// may import any core package and no core non-test package imports it.
//
// The layering engine moved here from ABO's internal/arch/layering_test.go
// (03 §3.5.1 item 3), so the root module, core and every future app repo run
// one algorithm: Layering(t, LayerSpec{...}).
//
// Standard library only: core's go.mod requires nothing at R1's first core PR.
package archtest

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Edge is an import: [0] imports [1]. In-module ends are module-relative ("."
// is the module-root package); a foreign end is a full import path.
type Edge = [2]string

// LayerSpec describes one module's layering rule. The field set follows 03
// §3.5.1: the module, its declared layers, the shrink-only wrong-way list, the
// mapping of foreign (core) imports, the loader floor, the controls and the
// tag sets.
type LayerSpec struct {
	// ModulePath is the module whose packages are layered.
	ModulePath string
	// Dir is the module root (holding go.mod).
	Dir string

	// LayerOf declares every package's layer, module-relative. A loaded
	// package with no entry fails (it must choose a layer), and the entry must
	// equal the layer the classification rule computes (so a PR cannot clear
	// a violation by raising the importer's layer).
	LayerOf map[string]int
	// DeclaredMayBeAbsent lets LayerOf name packages that do not exist yet
	// (core's R4 table lists deferred packages). When false, a LayerOf key
	// that is not a loaded package fails (a deleted package leaves the map).
	DeclaredMayBeAbsent bool

	// Override, Named and Family are the classification rule's first three
	// sources, in that order. A package none of them names computes from its
	// imports: the maximum layer among its in-module imports and its mapped
	// foreign imports that are >= 0, floored at ImportFloor; 0 when it has no
	// such import (a foreign import mapped to -1 is platform code, legal from
	// every layer, and raises nothing, like the standard library).
	Override    map[string]int
	Named       map[string]int
	Family      func(pkg string) (layer int, ok bool)
	ImportFloor int

	// Allowed is the shrink-only list of known wrong-way edges with a reason.
	// A listed edge must still exist and must still be wrong-way.
	Allowed map[Edge]string

	// ForeignPrefix selects the foreign imports the loader keeps (for the root
	// module, "github.com/falkcorp/mediacore/"). Foreign maps each onto this
	// module's layer scale; an unmapped one fails.
	ForeignPrefix string
	Foreign       func(importPath string) (layer int, ok bool)
	// Restricted maps a foreign import path (and its sub-packages) to the only
	// module packages that may import it in non-test code.
	Restricted map[string][]string

	// TestSupport reports the test-support packages: they may import any
	// layer, need no LayerOf entry, and no other package may import them.
	TestSupport func(pkg string) bool

	// FloorRatio and FloorBase are the loader-health floor: the loader must
	// return at least ceil(FloorRatio * FloorBase) packages. FloorBase 0 means
	// len(LayerOf).
	FloorRatio float64
	FloorBase  int
	// Control is an in-module edge that must exist; the zero Edge means none
	// (core's first PR, whose control is the engine's self-test).
	Control Edge
	// ForeignControl is a (module package, foreign import) edge, test imports
	// included, that must exist and be classified by Foreign.
	ForeignControl Edge

	// TagSets and EmbedOutputs are passed to Load (nil TagSets: the default
	// set only).
	TagSets      []TagSet
	EmbedOutputs []string
}

// Graph is a module's import graph as the engine reads it.
type Graph struct {
	// Imports maps every module package to its sorted in-module non-test
	// imports, module-relative.
	Imports map[string][]string
	// Foreign maps a package to its sorted non-test imports under the spec's
	// ForeignPrefix (full paths).
	Foreign map[string][]string
	// TestForeign is the same for test imports; only ForeignControl reads it.
	TestForeign map[string][]string
}

// Layering loads spec's module and fails t with every layering problem.
func Layering(t testing.TB, spec LayerSpec) {
	t.Helper()
	pkgs, err := Load(LoadOptions{Dir: spec.Dir, TagSets: spec.TagSets, EmbedOutputs: spec.EmbedOutputs})
	if err != nil {
		t.Fatalf("the loader is broken, not the code: %v", err)
	}
	g := BuildGraph(spec.ModulePath, spec.ForeignPrefix, pkgs)
	if problems := CheckGraph(spec, g); len(problems) > 0 {
		t.Errorf("%d layering problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

// BuildGraph converts loaded packages to the module-relative Graph.
func BuildGraph(modulePath, foreignPrefix string, pkgs map[string]*Package) Graph {
	g := Graph{Imports: map[string][]string{}, Foreign: map[string][]string{}, TestForeign: map[string][]string{}}
	for _, p := range pkgs {
		key, ok := Rel(modulePath, p.ImportPath)
		if !ok {
			continue
		}
		deps := []string{}
		for _, imp := range p.Imports {
			if d, ok := Rel(modulePath, imp); ok {
				deps = append(deps, d)
			} else if foreignPrefix != "" && strings.HasPrefix(imp, foreignPrefix) {
				g.Foreign[key] = append(g.Foreign[key], imp)
			}
		}
		sort.Strings(deps)
		g.Imports[key] = deps
		for _, imp := range p.TestImports {
			if foreignPrefix != "" && strings.HasPrefix(imp, foreignPrefix) {
				g.TestForeign[key] = append(g.TestForeign[key], imp)
			}
		}
		sort.Strings(g.Foreign[key])
		sort.Strings(g.TestForeign[key])
	}
	return g
}

// Rel converts an import path to its key relative to modulePath ("." for the
// module root), or reports false when the path is outside the module.
func Rel(modulePath, importPath string) (string, bool) {
	switch {
	case importPath == modulePath:
		return ".", true
	case strings.HasPrefix(importPath, modulePath+"/"):
		return strings.TrimPrefix(importPath, modulePath+"/"), true
	}
	return "", false
}

// CheckGraph applies spec's rule to g and returns every problem, sorted by the
// order the checks run. An empty graph is a problem: it would pass every rule
// vacuously.
func CheckGraph(spec LayerSpec, g Graph) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if len(g.Imports) == 0 {
		return []string{"the loader is broken, not the code: the graph holds no package"}
	}
	base := spec.FloorBase
	if base == 0 {
		base = len(spec.LayerOf)
	}
	if want := int(math.Ceil(spec.FloorRatio * float64(base))); len(g.Imports) < want {
		add("the loader is broken, not the code: loaded %d packages, want at least %d (%.2f x %d)",
			len(g.Imports), want, spec.FloorRatio, base)
	}
	if spec.Control != (Edge{}) && !containsString(g.Imports[spec.Control[0]], spec.Control[1]) {
		add("the loader is broken, not the code: control edge %s -> %s not found; a PR that removes it names a replacement in-module edge",
			spec.Control[0], spec.Control[1])
	}
	if fc := spec.ForeignControl; fc != (Edge{}) {
		if !containsString(g.Foreign[fc[0]], fc[1]) && !containsString(g.TestForeign[fc[0]], fc[1]) {
			add("the classifier is not exercised: control import %s -> %s not found; a PR that removes it names a replacement", fc[0], fc[1])
		} else if spec.Foreign == nil {
			add("the classifier is not exercised: control import %s -> %s exists but the spec has no Foreign mapping", fc[0], fc[1])
		} else if _, ok := spec.Foreign(fc[1]); !ok {
			add("the classifier did not map the control import %s -> %s", fc[0], fc[1])
		}
	}

	isTS := func(pkg string) bool { return spec.TestSupport != nil && spec.TestSupport(pkg) }
	pkgs := sortedKeys(g.Imports)

	for _, pkg := range pkgs {
		if _, ok := spec.LayerOf[pkg]; !ok && !isTS(pkg) {
			add("package %q has no entry in layerOf: choose its layer", pkg)
		}
	}
	if !spec.DeclaredMayBeAbsent {
		for _, pkg := range sortedKeys(spec.LayerOf) {
			if _, ok := g.Imports[pkg]; !ok {
				add("layerOf lists %q, which is not a package in the module: remove the entry", pkg)
			}
		}
	}

	computed := ComputeLayers(spec, g)
	for _, pkg := range pkgs {
		declared, ok := spec.LayerOf[pkg]
		if !ok || isTS(pkg) {
			continue
		}
		if want := computed[pkg]; declared != want {
			add("layerOf[%q] is %d but the classification rule computes %d: set it to %d, or fix the imports that change the computed layer. "+
				"A layer change needs an entry in the override table with go list evidence", pkg, declared, want, want)
		}
	}

	exists := map[Edge]bool{}
	layerOf := func(pkg string) (int, bool) { l, ok := spec.LayerOf[pkg]; return l, ok }
	for _, pkg := range pkgs {
		from, okFrom := layerOf(pkg)
		for _, dep := range g.Imports[pkg] {
			e := Edge{pkg, dep}
			exists[e] = true
			if isTS(pkg) {
				continue
			}
			if isTS(dep) {
				add("%s imports test-support package %s; only test-support packages and test files may", pkg, dep)
				continue
			}
			to, okTo := layerOf(dep)
			if !okFrom || !okTo {
				continue // reported as a missing layerOf entry
			}
			if to > from {
				if _, ok := spec.Allowed[e]; !ok {
					add("layering violation: %s (layer %d) imports %s (layer %d); a package may import only its own layer or lower. "+
						"Fix the import (invert the dependency, pass a function or interface in), or list the edge in allowed with a reason. "+
						"Do not raise the importer's layer: layerOf must match the computed layer", pkg, from, dep, to)
				}
			}
		}
		for _, imp := range g.Foreign[pkg] {
			e := Edge{pkg, imp}
			exists[e] = true
			for restricted, importers := range spec.Restricted {
				if (imp == restricted || strings.HasPrefix(imp, restricted+"/")) && !containsString(importers, pkg) {
					add("%s imports %s; only %s may import it (non-test)", pkg, imp, strings.Join(importers, ", "))
				}
			}
			if spec.Foreign == nil {
				add("%s imports %s, but the spec maps no foreign import", pkg, imp)
				continue
			}
			to, ok := spec.Foreign(imp)
			if !ok {
				add("%s imports %s, which has no mapped layer: add it to the foreign mapping", pkg, imp)
				continue
			}
			if isTS(pkg) || !okFrom {
				continue
			}
			if to > from {
				if _, ok := spec.Allowed[e]; !ok {
					add("layering violation: %s (layer %d) imports %s (mapped layer %d); a core import is checked against the importer's layer like an in-module one",
						pkg, from, imp, to)
				}
			}
		}
	}

	for _, e := range sortedEdges(spec.Allowed) {
		if !exists[e] {
			add("allowed lists %s -> %s, but that import no longer exists: remove this entry, the edge is fixed", e[0], e[1])
			continue
		}
		from, okFrom := layerOf(e[0])
		to, okTo := layerOf(e[1])
		if !okTo && spec.Foreign != nil {
			to, okTo = spec.Foreign(e[1])
		}
		if okFrom && okTo && to <= from {
			add("allowed lists %s -> %s, but that edge is legal (layer %d imports layer %d): remove this entry", e[0], e[1], from, to)
		}
	}
	return problems
}

// ComputeLayers applies spec's classification rule to every package of g:
// Override, then Named, then Family, then the import rule (see LayerSpec).
func ComputeLayers(spec LayerSpec, g Graph) map[string]int {
	out := map[string]int{}
	visiting := map[string]bool{}
	var layer func(pkg string) int
	layer = func(pkg string) int {
		if l, ok := out[pkg]; ok {
			return l
		}
		if visiting[pkg] { // an import cycle cannot compile; guard anyway
			return 0
		}
		visiting[pkg] = true
		defer delete(visiting, pkg)
		var l int
		if o, ok := spec.Override[pkg]; ok {
			l = o
		} else if n, ok := spec.Named[pkg]; ok {
			l = n
		} else if f, ok := familyOf(spec, pkg); ok {
			l = f
		} else {
			counted := false
			for _, d := range g.Imports[pkg] {
				counted = true
				if dl := layer(d); dl > l {
					l = dl
				}
			}
			if spec.Foreign != nil {
				for _, imp := range g.Foreign[pkg] {
					if fl, ok := spec.Foreign(imp); ok && fl >= 0 {
						counted = true
						if fl > l {
							l = fl
						}
					}
				}
			}
			if !counted {
				l = 0
			} else if l < spec.ImportFloor {
				l = spec.ImportFloor
			}
		}
		out[pkg] = l
		return l
	}
	for pkg := range g.Imports {
		layer(pkg)
	}
	return out
}

func familyOf(spec LayerSpec, pkg string) (int, bool) {
	if spec.Family == nil {
		return 0, false
	}
	return spec.Family(pkg)
}

func containsString(s []string, v string) bool {
	return slices.Contains(s, v)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedEdges(m map[Edge]string) []Edge {
	keys := make([]Edge, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	return keys
}
