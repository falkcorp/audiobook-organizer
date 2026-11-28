// file: internal/arch/descriptor_callers_test.go
// version: 0.1.0
// guid: 5d1e7a38-92c4-4f06-b8a1-3c70e4d95b62
// last-edited: 2026-11-22

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is audiobook-organizer's twin of core/archtest's
// IntegrationsDescriptorCallers (05-metadata M1.3, I7). Core pins who may call
// integrations.NewDescriptor inside core; this pins who may build or hand out an
// integration Descriptor from the ABO module (the integrations flip, F7-09,
// registers them through app.Options.Adapters, and the kernel's integrations
// registry accepts whatever a Descriptor says about its kind, settings schema,
// egress and limits).
//
// A Descriptor reaches ABO code through four families of function, all guarded
// here:
//
//   - integrations.NewDescriptor, the untyped constructor. Only a kind package
//     calls it in core; ABO packages never call it, they use their kind's typed
//     constructor.
//   - the kind packages' typed constructors (ai, download, metadata and notify
//     each declare Adapter), which stamp the kind and pin the instance type.
//   - the core adapters' Descriptor() (deluge, openai, mail, webhook), which
//     the kernel registers at the flip.
//
// Every reference counts, not only calls: a call, a function value, a generic
// instantiation, through an import alias or a dot import, and a //go:linkname
// naming the symbol. Test files are exempt (they exercise adapters; they do not
// register them in the running app).
//
// The allowed set is a ratchet in both directions, like
// TestFilteredGetterCallers_AreAllReviewed: a package that starts referencing a
// guarded symbol fails until it is listed here on purpose, and a listed package
// that stops referencing it fails too, so the list cannot go stale.

// corePath is the module path of core, which this module reaches through a
// replace directive.
const corePath = "github.com/falkcorp/mediacore"

// descriptorSymbol is one guarded function: an import path and a name.
type descriptorSymbol struct{ pkg, name string }

func (s descriptorSymbol) String() string { return s.pkg + "." + s.name }

// descriptorCallers maps every guarded symbol to the ABO packages (directories
// relative to the module root) allowed to reference it. A symbol with no
// package is one no ABO package references today.
//
// Updating this is the point of the test: the F7-09 flip adds the one package
// that registers the core adapters' Descriptor() functions, and a new ABO
// adapter package adds itself beside the two below.
var descriptorCallers = map[descriptorSymbol][]string{
	{corePath + "/integrations", "NewDescriptor"}: nil,

	{corePath + "/integrations/ai", "Adapter"}:       {"internal/books/adapters/whisper"},
	{corePath + "/integrations/metadata", "Adapter"}: {"internal/books/adapters/metadata"},
	{corePath + "/integrations/download", "Adapter"}: nil,
	{corePath + "/integrations/notify", "Adapter"}:   nil,

	{corePath + "/integrations/ai/openai", "Descriptor"}:       nil,
	{corePath + "/integrations/download/deluge", "Descriptor"}: nil,
	{corePath + "/integrations/notify/mail", "Descriptor"}:     nil,
	{corePath + "/integrations/notify/webhook", "Descriptor"}:  nil,
}

// skipDirs are directories the scan never enters, by name.
var descriptorSkipDirs = map[string]bool{
	".git": true, ".worktrees": true, "node_modules": true, "vendor": true, "web": true,
	".standards": true, "mocks": true, "testdata": true,
}

// descriptorFindings walks the module at root and returns, sorted, one line per
// reference to a symbol of allowed from a package the symbol's list does not
// name, plus one line per listed package that holds no reference. scanned is
// the number of non-test Go files parsed. A nested module (a directory with its
// own go.mod, such as core) is not part of this module and is skipped.
func descriptorFindings(root string, allowed map[descriptorSymbol][]string) (findings []string, scanned int, err error) {
	byPkg := map[string]map[descriptorSymbol]bool{} // directory -> symbols referenced
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if descriptorSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if p != root {
				if _, serr := os.Stat(filepath.Join(p, "go.mod")); serr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		scanned++
		refs, perr := descriptorRefs(p, allowed)
		if perr != nil {
			return perr
		}
		dir := path.Dir(rel)
		for _, r := range refs {
			if byPkg[dir] == nil {
				byPkg[dir] = map[descriptorSymbol]bool{}
			}
			byPkg[dir][r.sym] = true
			if !slices.Contains(allowed[r.sym], dir) {
				findings = append(findings, fmt.Sprintf("%s:%d: %s referenced from %s, which is not in the allowed set", rel, r.line, r.sym, dir))
			}
		}
		return nil
	})
	if err != nil {
		return nil, scanned, err
	}
	for sym, dirs := range allowed {
		for _, dir := range dirs {
			if !byPkg[dir][sym] {
				findings = append(findings, fmt.Sprintf("%s is allowed to reference %s but no longer does; drop it from descriptorCallers", dir, sym))
			}
		}
	}
	sort.Strings(findings)
	return findings, scanned, nil
}

// descriptorRef is one reference to a guarded symbol.
type descriptorRef struct {
	sym  descriptorSymbol
	line int
}

// descriptorRefs parses one file and reports every reference to a guarded
// symbol: a selector through the import's local name (its alias, or the last
// element of its path), any identifier of the symbol's name in a file that
// dot-imports the symbol's package, and a //go:linkname naming it.
func descriptorRefs(file string, guarded map[descriptorSymbol][]string) ([]descriptorRef, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	line := func(pos token.Pos) int { return fset.Position(pos).Line }

	// local name -> import path, for the guarded packages this file imports.
	names := map[string]string{}
	dots := map[string]bool{} // import paths dot-imported
	for _, imp := range af.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		known := false
		for sym := range guarded {
			if sym.pkg == p {
				known = true
				break
			}
		}
		if !known {
			continue
		}
		switch {
		case imp.Name == nil:
			names[path.Base(p)] = p
		case imp.Name.Name == ".":
			dots[p] = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = p
		}
	}

	var out []descriptorRef
	if len(names) > 0 || len(dots) > 0 {
		sels := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			sels[sel.Sel] = true
			if x, ok := sel.X.(*ast.Ident); ok {
				if pkg, ok := names[x.Name]; ok {
					if sym := (descriptorSymbol{pkg, sel.Sel.Name}); hasSymbol(guarded, sym) {
						out = append(out, descriptorRef{sym, line(sel.Pos())})
					}
				}
			}
			return true
		})
		if len(dots) > 0 {
			ast.Inspect(af, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || sels[id] {
					return true
				}
				for pkg := range dots {
					if sym := (descriptorSymbol{pkg, id.Name}); hasSymbol(guarded, sym) {
						out = append(out, descriptorRef{sym, line(id.Pos())})
					}
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
				if pkg, name, ok := splitQualified(field); ok {
					if sym := (descriptorSymbol{pkg, name}); hasSymbol(guarded, sym) {
						out = append(out, descriptorRef{sym, line(c.Pos())})
					}
				}
			}
		}
	}
	return out, nil
}

func hasSymbol(guarded map[descriptorSymbol][]string, s descriptorSymbol) bool {
	_, ok := guarded[s]
	return ok
}

// splitQualified splits "a/b/c.Name" at the last dot after the last slash.
func splitQualified(s string) (pkg, name string, ok bool) {
	slash := strings.LastIndex(s, "/")
	dot := strings.LastIndex(s, ".")
	if dot <= slash || dot == len(s)-1 {
		return "", "", false
	}
	return s[:dot], s[dot+1:], true
}

// aboRoot is the module root, from the package directory (go test runs in it).
func aboRoot(t testing.TB) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(wd, "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found from %s: %v", wd, err)
	}
	return root
}

// TestDescriptorCallers_ABOPackagesAreAllowlisted fails when an ABO package
// outside descriptorCallers references an integration Descriptor constructor,
// or a listed package stopped doing so.
func TestDescriptorCallers_ABOPackagesAreAllowlisted(t *testing.T) {
	findings, scanned, err := descriptorFindings(aboRoot(t), descriptorCallers)
	if err != nil {
		t.Fatal(err)
	}
	// A scan that read nothing, or that did not run over the packages this
	// module is known to build descriptors in, would pass without looking.
	if scanned < 100 {
		t.Fatalf("scanned only %d non-test Go files; the guard would be vacuous", scanned)
	}
	if len(findings) > 0 {
		t.Errorf("%d descriptor-caller finding(s) (05-metadata M1.3: only the packages in descriptorCallers build or register an integration Descriptor):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// TestDescriptorCallers_GuardedSymbolsExist fails when a guarded name no longer
// exists in core, so a rename there cannot turn the guard into one that matches
// nothing. It also pins that each guarded package's name is the last element of
// its path, which the unaliased-import detection assumes.
func TestDescriptorCallers_GuardedSymbolsExist(t *testing.T) {
	coreDir := filepath.Join(aboRoot(t), "core")
	for sym := range descriptorCallers {
		rel, ok := strings.CutPrefix(sym.pkg, corePath+"/")
		if !ok {
			t.Errorf("%s is not under %s", sym, corePath)
			continue
		}
		dir := filepath.Join(coreDir, filepath.FromSlash(rel))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("%s: %v", sym, err)
			continue
		}
		found := false
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			af, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Errorf("parse %s: %v", e.Name(), err)
				continue
			}
			if af.Name.Name != path.Base(sym.pkg) {
				t.Errorf("%s: package name %q is not the last path element %q", sym, af.Name.Name, path.Base(sym.pkg))
			}
			for _, decl := range af.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == sym.name {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no function %s in %s", sym, sym.name, dir)
		}
	}
}

// writeTree writes files (path -> content, slash-separated) under a new temp
// directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestDescriptorCallers_DetectsEveryShape drives the scanner over a synthetic
// module: each reference shape in an unlisted package is reported once, the
// listed package is not, a blank import and a test file report nothing, a
// nested module is not scanned, and a listed package that holds no reference is
// reported as stale.
func TestDescriptorCallers_DetectsEveryShape(t *testing.T) {
	const integ = corePath + "/integrations"
	const aiPkg = corePath + "/integrations/ai"
	allowed := map[descriptorSymbol][]string{
		{integ, "NewDescriptor"}: nil,
		{aiPkg, "Adapter"}:       {"internal/batcave"},
	}
	files := map[string]string{
		"go.mod": "module example.test/abo\n\ngo 1.27.0\n",
		// the listed package: an allowed reference
		"internal/batcave/batcave.go": "package batcave\n\nimport \"" + aiPkg + "\"\n\nvar A = ai.Adapter\n",
		// a plain call, an alias, a function value, a generic instantiation
		"internal/gordon/plain.go": "package gordon\n\nimport \"" + integ + "\"\n\nvar D = integrations.NewDescriptor[int]\n",
		"internal/gordon/alias.go": "package gordon\n\nimport sig \"" + integ + "\"\n\nfunc F() { _ = sig.NewDescriptor[int](nil, nil) }\n",
		// a dot import, an identifier that is not a selector
		"internal/robin/dot.go": "package robin\n\nimport . \"" + integ + "\"\n\nvar D = NewDescriptor[int]\n",
		// the listed symbol from another package
		"internal/oracle/ai.go": "package oracle\n\nimport \"" + aiPkg + "\"\n\nvar A = ai.Adapter\n",
		// a linkname naming the symbol
		"internal/joker/link.go": "package joker\n\nimport _ \"unsafe\"\n\n//go:linkname newDescriptor " + integ + ".NewDescriptor\nfunc newDescriptor()\n",
		// shapes that must report nothing
		"internal/penguin/blank.go":     "package penguin\n\nimport _ \"" + integ + "\"\n",
		"internal/penguin/other.go":     "package penguin\n\nimport \"" + integ + "\"\n\nvar K = integrations.KindMetadata\n",
		"internal/penguin/local.go":     "package penguin\n\ntype integrations struct{}\n\nfunc (integrations) NewDescriptor() {}\n\nvar _ = integrations{}.NewDescriptor\n",
		"internal/gordon/plain_test.go": "package gordon\n\nimport \"" + integ + "\"\n\nvar _ = integrations.NewDescriptor[int]\n",
		// a nested module is not this module
		"core/go.mod":         "module " + corePath + "\n\ngo 1.27.0\n",
		"core/inner/x.go":     "package inner\n\nimport \"" + integ + "\"\n\nvar _ = integrations.NewDescriptor[int]\n",
		"node_modules/y/y.go": "package y\n\nimport \"" + integ + "\"\n\nvar _ = integrations.NewDescriptor[int]\n",
	}
	root := writeTree(t, files)
	got, scanned, err := descriptorFindings(root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 9 {
		t.Errorf("scanned %d files, want 9 (the nested module, node_modules and the test file are not scanned)", scanned)
	}
	wantDirs := []string{"internal/gordon", "internal/gordon", "internal/robin", "internal/oracle", "internal/joker"}
	var gotDirs []string
	for _, f := range got {
		if strings.Contains(f, "no longer does") {
			t.Errorf("unexpected stale report: %s", f)
			continue
		}
		file, _, _ := strings.Cut(f, ":")
		gotDirs = append(gotDirs, path.Dir(file))
	}
	sort.Strings(gotDirs)
	sort.Strings(wantDirs)
	if !slices.Equal(gotDirs, wantDirs) {
		t.Errorf("reported directories = %v, want %v\nfindings:\n  %s", gotDirs, wantDirs, strings.Join(got, "\n  "))
	}

	// A listed package with no reference is stale.
	stale := writeTree(t, map[string]string{
		"go.mod":                      "module example.test/abo\n\ngo 1.27.0\n",
		"internal/batcave/batcave.go": "package batcave\n\nvar A = 1\n",
	})
	got, _, err = descriptorFindings(stale, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "internal/batcave is allowed to reference "+aiPkg+".Adapter but no longer does") {
		t.Errorf("stale listing: %v", got)
	}
}
