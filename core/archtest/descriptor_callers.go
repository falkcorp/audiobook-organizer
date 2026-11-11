// file: core/archtest/descriptor_callers.go
// version: 1.0.0
// guid: 3c5d8d9b-6d91-48f8-88f9-2a0b4130d95f
// last-edited: 2026-11-11

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// integrationsPath is the package that declares NewDescriptor.
const integrationsPath = mediacorePath + "/integrations"

// newDescriptor is the guarded function's name.
const newDescriptor = "NewDescriptor"

// descriptorKinds are the packages allowed to call integrations.NewDescriptor:
// the kind packages, directly under core/integrations/, each declaring its
// kind's typed constructor (download.Adapter, ai.Adapter, ...; 05-metadata
// M1.3 Descriptor). The kinds are integrations' closed set.
func descriptorKinds() map[string]bool {
	out := map[string]bool{}
	for _, k := range []string{"ai", "download", "indexer", "metadata", "notify"} {
		out[integrationsPath+"/"+k] = true
	}
	return out
}

// descriptorExempt reports the packages whose non-test files may reference
// NewDescriptor besides the kind packages: integrations itself, and the
// test-support packages of 03 R4 (coretest, archtest and every
// <pkg>/<pkg>test, such as integrations/integrationstest's fake adapter).
func descriptorExempt(importPath string) bool {
	rel, ok := Rel(mediacorePath, importPath)
	if !ok {
		return false
	}
	if rel == "integrations" {
		return true
	}
	for _, root := range []string{"coretest", "archtest"} {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	if i := strings.LastIndex(rel, "/"); i > 0 {
		parent, last := rel[:i], rel[i+1:]
		return last == parent[strings.LastIndex(parent, "/")+1:]+"test"
	}
	return false
}

// IntegrationsDescriptorCallers fails t when a non-test file outside the
// kind packages references integrations.NewDescriptor (05-metadata M1.3,
// I7): an adapter or module package that builds a Descriptor itself could
// register a kind with an instance type the kind package does not accept.
// Every reference counts, not only calls: a call, a function value, a
// generic instantiation, through any import alias or a dot import, and a
// //go:linkname naming it. Test files and test-support packages are exempt.
//
// It loads core, the module above the working directory, over the whole
// tag-set union.
func IntegrationsDescriptorCallers(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("IntegrationsDescriptorCallers: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("IntegrationsDescriptorCallers: %v", err)
	}
	findings, err := IntegrationsDescriptorCallerFindings(root)
	if err != nil {
		t.Fatalf("IntegrationsDescriptorCallers: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d reference(s) to %s.%s outside the kind packages (05-metadata M1.3: only a kind's typed constructor builds a Descriptor):\n  %s",
			len(findings), integrationsPath, newDescriptor, strings.Join(findings, "\n  "))
	}
}

// IntegrationsDescriptorCallerFindings loads the core module at dir over
// TagSets() and returns one line per reference IntegrationsDescriptorCallers
// reports, sorted. It returns an error when the load fails, when
// integrations is not among the loaded packages, or when no non-test file
// was scanned: each would make the guard pass without looking.
func IntegrationsDescriptorCallerFindings(dir string) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	if _, ok := pkgs[integrationsPath]; !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", integrationsPath, len(pkgs))
	}
	kinds := descriptorKinds()
	var out []string
	scanned := 0
	for _, p := range pkgs {
		if kinds[p.ImportPath] || descriptorExempt(p.ImportPath) {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := descriptorRefs(f)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file outside the kind packages under %s; the guard would be vacuous", dir)
	}
	sort.Strings(out)
	return uniq(out), nil
}

// descriptorRefs parses one file and reports every reference to
// integrations.NewDescriptor: a selector through the import's name (its
// alias or "integrations"), any identifier of that name in a file that
// dot-imports integrations, and a //go:linkname naming it.
func descriptorRefs(file string) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	names := map[string]bool{}
	dot := false
	for _, imp := range af.Imports {
		// The parser only accepts a well-formed quoted import path.
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != integrationsPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["integrations"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	var out []string
	report := func(pos token.Pos) {
		out = append(out, fmt.Sprintf("%s: %s.%s outside a kind package", fset.Position(pos), integrationsPath, newDescriptor))
	}
	if len(names) > 0 || dot {
		sels := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			sels[sel.Sel] = true
			if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] && sel.Sel.Name == newDescriptor {
				report(sel.Pos())
			}
			return true
		})
		if dot {
			ast.Inspect(af, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == newDescriptor && !sels[id] {
					report(id.Pos())
				}
				return true
			})
		}
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if !ok {
				continue
			}
			for field := range strings.FieldsSeq(rest) {
				if field == integrationsPath+"."+newDescriptor {
					report(c.Pos())
					break
				}
			}
		}
	}
	return out, nil
}
