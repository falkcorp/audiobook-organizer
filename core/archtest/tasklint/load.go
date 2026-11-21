// file: core/archtest/tasklint/load.go
// version: 1.0.0
// guid: 23bb91eb-f2f4-494d-9eeb-c9011647c305
// last-edited: 2026-11-21

package tasklint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"maps"
	"os"
	"runtime"
	"slices"

	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

// loadTyped loads the packages patterns name with syntax, type information and
// sizes, as packages.LoadSyntax does, but type-checks them one at a time on the
// calling goroutine.
//
// packages.Load type-checks independent packages of one call concurrently, and
// the packages it reads from export data share one set of lazily expanded
// *types.Named values. go1.27's (*Checker).isComplete reads such a value's
// fromRHS without the lock (*Named).unpack holds when it writes it, so two
// checkers that meet the same instantiated type (an iter.Seq[T] in a
// dependency's signature, say) report a data race under -race, and in a
// release build read a half-written type. The flake is golang/go#81122,
// seen through x/tools as golang/go#82614; the fix, CL 841585 (golang/go#81871),
// is not in a released Go. Neither the race detector nor the pinned toolchain
// can be changed from here, and there is nothing of ours to lock: the shared
// value lives inside go/types. What removes the sharing is never running two
// checkers at once, so this loader owns the type-check and keeps go/packages
// to what it does without types: the import graph and each package's export
// file.
//
// Parsing stays parallel (parsing shares no go/types state; the FileSet is safe
// for concurrent use). Type-checking is serial and runs in dependency order, so
// every initial package an initial package imports, directly or through a
// package read from export data, is the one *types.Package every importer sees:
// the single type universe packages.Load gives one call, which the whole-program
// unregistered-kind rule compares objects across. Delete this file for a plain
// packages.Load(LoadSyntax) once a released toolchain carries the fix; Analyze
// is its only caller.
func loadTyped(patterns []string) ([]*packages.Package, error) {
	roots, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedFiles |
		packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps |
		packages.NeedExportFile | packages.NeedModule | packages.NeedTypesSizes}, patterns...)
	if err != nil {
		return nil, err
	}

	initial := make(map[*packages.Package]bool, len(roots))
	for _, p := range roots {
		initial[p] = true
	}
	// Dependencies first, and an initial package ahead of every initial package
	// that reaches it through any path: a package read from export data names
	// the initial packages it was built against, and must find them already
	// checked, as the same *types.Package.
	var order []*packages.Package
	seen := map[*packages.Package]bool{}
	var visit func(*packages.Package)
	visit = func(p *packages.Package) {
		if seen[p] {
			return
		}
		seen[p] = true
		for _, path := range slices.Sorted(maps.Keys(p.Imports)) {
			visit(p.Imports[path])
		}
		if initial[p] {
			order = append(order, p)
		}
	}
	for _, p := range roots {
		visit(p)
	}

	fset := token.NewFileSet()
	if err := parseAll(fset, order); err != nil {
		return nil, err
	}

	universe := map[string]*types.Package{}
	for _, p := range order {
		typeCheck(fset, universe, p, initial)
	}
	return roots, nil
}

// parseAll parses every package's compiled Go files into p.Syntax, one worker
// per package: a worker writes only its own package, so the workers share
// nothing but the FileSet.
func parseAll(fset *token.FileSet, pkgs []*packages.Package) error {
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for _, p := range pkgs {
		g.Go(func() error {
			p.Fset = fset
			for _, name := range p.CompiledGoFiles {
				f, err := parser.ParseFile(fset, name, nil, parser.AllErrors|parser.ParseComments)
				if f != nil {
					p.Syntax = append(p.Syntax, f)
				}
				if list, ok := err.(scanner.ErrorList); ok {
					for _, e := range list {
						p.Errors = append(p.Errors, packages.Error{Pos: e.Pos.String(), Msg: e.Msg, Kind: packages.ParseError})
					}
				} else if err != nil {
					p.Errors = append(p.Errors, packages.Error{Pos: name, Msg: err.Error(), Kind: packages.ParseError})
				}
			}
			return nil
		})
	}
	return g.Wait()
}

// typeCheck type-checks p, filling Types, TypesInfo, TypesSizes and IllTyped
// the way packages.Load does, with function bodies checked (p is initial).
// universe is every package type-checked or read from export data so far, by
// path. It must be called on one goroutine, dependencies before dependents.
func typeCheck(fset *token.FileSet, universe map[string]*types.Package, p *packages.Package, initial map[*packages.Package]bool) {
	p.TypesInfo = &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Implicits:    make(map[ast.Node]types.Object),
		Instances:    make(map[*ast.Ident]types.Instance),
		Scopes:       make(map[ast.Node]*types.Scope),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		FileVersions: make(map[*ast.File]string),
	}
	if p.TypesSizes == nil {
		p.TypesSizes = types.SizesFor("gc", runtime.GOARCH)
	}
	p.Types = types.NewPackage(p.PkgPath, p.Name)

	conf := types.Config{
		Sizes: p.TypesSizes,
		Error: func(err error) {
			e := packages.Error{Msg: err.Error(), Kind: packages.TypeError}
			if te, ok := err.(types.Error); ok {
				e.Pos, e.Msg = fset.Position(te.Pos).String(), te.Msg
			}
			p.Errors = append(p.Errors, e)
		},
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if path == "unsafe" {
				return types.Unsafe, nil
			}
			// The imports map is keyed by the path as the source spells it.
			ip := p.Imports[path]
			if ip == nil {
				return nil, fmt.Errorf("no metadata for %s", path)
			}
			if initial[ip] {
				if ip.Types == nil {
					return nil, fmt.Errorf("%s is imported before it was type-checked", ip.PkgPath)
				}
				return ip.Types, nil
			}
			return readExportData(fset, universe, ip)
		}),
	}
	if p.Module != nil && p.Module.GoVersion != "" {
		conf.GoVersion = "go" + p.Module.GoVersion
	}
	if err := types.NewChecker(&conf, fset, p.Types, p.TypesInfo).Files(p.Syntax); err != nil && len(p.Errors) == 0 {
		p.Errors = append(p.Errors, packages.Error{Msg: err.Error(), Kind: packages.UnknownError})
	}
	universe[p.PkgPath] = p.Types

	p.IllTyped = len(p.Errors) > 0
	for _, ip := range p.Imports {
		p.IllTyped = p.IllTyped || ip.IllTyped
	}
}

// readExportData returns the package ip as its export file describes it,
// resolving every package the file names against universe.
func readExportData(fset *token.FileSet, universe map[string]*types.Package, ip *packages.Package) (*types.Package, error) {
	if t := universe[ip.PkgPath]; t != nil && t.Complete() {
		return t, nil
	}
	if ip.ExportFile == "" {
		return nil, fmt.Errorf("no export data for %s", ip.PkgPath)
	}
	f, err := os.Open(ip.ExportFile)
	if err != nil {
		return nil, fmt.Errorf("export data for %s: %w", ip.PkgPath, err)
	}
	defer f.Close()
	r, err := gcexportdata.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("export data for %s: %w", ip.PkgPath, err)
	}
	t, err := gcexportdata.Read(r, fset, universe, ip.PkgPath)
	if err != nil {
		return nil, fmt.Errorf("export data for %s: %w", ip.PkgPath, err)
	}
	return t, nil
}

// importerFunc adapts a function to types.Importer.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
