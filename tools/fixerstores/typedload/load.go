// file: tools/fixerstores/typedload/load.go
// version: 1.0.0
// guid: 8c2e47d1-5b36-4a90-b1f8-0d7a3e9c6f52
// last-edited: 2026-11-21

// Package typedload is a copy of core/archtest/typedload, which is where its
// doc comment lives, for this module: tools/fixerstores is a module of its own
// (x/tools stays out of the root go.mod), so it cannot import core's. From the
// package clause on it is byte for byte the original, and
// internal/arch.TestTypedloadCopyMatches keeps it so. Delete it with the
// original when a released toolchain carries the go/types fix (golang/go#81122).
package typedload

import (
	"errors"
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

// Load loads the packages patterns name as packages.Load(cfg, patterns...)
// does for a cfg whose Mode includes NeedTypes, and returns the same result:
// the matched packages, with Syntax, Types, TypesInfo, TypesSizes, Fset,
// Errors and IllTyped filled as Mode asks, and the same one type universe, so
// two packages of the load that import a third see one *types.Package.
//
// The packages type-checked from source are the matched ones, or with NeedDeps
// every package of the import graph, standard library included; the rest are
// read from export data. A cfg with Tests set needs NeedDeps (a test variant
// and the package it extends share an import path, which the export-data
// universe is keyed by).
//
// Loading and export data come from go/packages; the files are parsed in
// parallel (a worker per package, sharing only the concurrency-safe FileSet),
// with cfg.Overlay and cfg.ParseFile honoured; then the type-check runs on the
// calling goroutine, dependencies first.
func Load(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
	if cfg == nil {
		return nil, errors.New("typedload: nil Config")
	}
	if cfg.Mode&packages.NeedTypes == 0 {
		return nil, errors.New("typedload: Mode lacks NeedTypes; use packages.Load, which type-checks nothing then")
	}
	deps := cfg.Mode&packages.NeedDeps != 0
	if cfg.Tests && !deps {
		return nil, errors.New("typedload: Tests needs NeedDeps")
	}

	meta := *cfg
	meta.Mode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
		packages.NeedDeps | packages.NeedModule | packages.NeedTypesSizes
	if !deps {
		meta.Mode |= packages.NeedExportFile
	}
	roots, err := packages.Load(&meta, patterns...)
	if err != nil {
		return nil, err
	}

	// Dependencies first, and a source package ahead of every source package
	// that reaches it through any path: a package read from export data names
	// the source packages it was built against, and must find them already
	// checked, as the same *types.Package.
	isRoot := make(map[*packages.Package]bool, len(roots))
	for _, p := range roots {
		isRoot[p] = true
	}
	source := func(p *packages.Package) bool { return deps || isRoot[p] }
	var order, all []*packages.Package
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
		all = append(all, p)
		if source(p) {
			order = append(order, p)
		}
	}
	for _, p := range roots {
		visit(p)
	}

	fset := cfg.Fset
	if fset == nil {
		fset = token.NewFileSet()
	}
	for _, p := range all {
		p.Fset = fset
	}
	if err := parseAll(cfg, fset, order); err != nil {
		return nil, err
	}

	c := &checker{fset: fset, universe: map[string]*types.Package{}, source: source,
		wantInfo: cfg.Mode&packages.NeedTypesInfo != 0}
	for _, p := range order {
		c.check(p)
	}
	if cfg.Mode&packages.NeedSyntax == 0 {
		for _, p := range order {
			p.Syntax = nil
		}
	}
	return roots, nil
}

// parseAll parses every package's compiled Go files into p.Syntax, one worker
// per package: a worker writes only its own package, so the workers share
// nothing but the FileSet and the read-only Config.
func parseAll(cfg *packages.Config, fset *token.FileSet, pkgs []*packages.Package) error {
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for _, p := range pkgs {
		g.Go(func() error {
			for _, name := range p.CompiledGoFiles {
				src, ok := cfg.Overlay[name]
				if !ok {
					var err error
					if src, err = os.ReadFile(name); err != nil {
						p.Errors = append(p.Errors, packages.Error{Pos: name, Msg: err.Error(), Kind: packages.ParseError})
						continue
					}
				}
				var f *ast.File
				var err error
				if cfg.ParseFile != nil {
					f, err = cfg.ParseFile(fset, name, src)
				} else {
					f, err = parser.ParseFile(fset, name, src, parser.AllErrors|parser.ParseComments)
				}
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

// checker type-checks packages one at a time, on one goroutine, dependencies
// before dependents.
type checker struct {
	fset     *token.FileSet
	universe map[string]*types.Package // every package checked or read from export data so far, by path
	source   func(*packages.Package) bool
	wantInfo bool
}

// check type-checks p, filling Types, TypesInfo, TypesSizes and IllTyped the
// way packages.Load does, with function bodies checked.
func (c *checker) check(p *packages.Package) {
	if p.PkgPath == "unsafe" {
		p.Types = types.Unsafe
		return
	}
	if c.wantInfo {
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
				e.Pos, e.Msg = c.fset.Position(te.Pos).String(), te.Msg
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
			if c.source(ip) {
				if ip.Types == nil {
					return nil, fmt.Errorf("%s is imported before it was type-checked", ip.PkgPath)
				}
				return ip.Types, nil
			}
			t, err := c.readExportData(ip)
			if err != nil {
				return nil, err
			}
			ip.Types = t // as packages.Load leaves a direct dependency's Types
			return t, nil
		}),
	}
	if p.Module != nil && p.Module.GoVersion != "" {
		conf.GoVersion = "go" + p.Module.GoVersion
	}
	if err := types.NewChecker(&conf, c.fset, p.Types, p.TypesInfo).Files(p.Syntax); err != nil && len(p.Errors) == 0 {
		p.Errors = append(p.Errors, packages.Error{Msg: err.Error(), Kind: packages.UnknownError})
	}
	c.universe[p.PkgPath] = p.Types

	p.IllTyped = len(p.Errors) > 0
	for _, ip := range p.Imports {
		p.IllTyped = p.IllTyped || ip.IllTyped
	}
}

// readExportData returns the package ip as its export file describes it,
// resolving every package the file names against the universe.
func (c *checker) readExportData(ip *packages.Package) (*types.Package, error) {
	if t := c.universe[ip.PkgPath]; t != nil && t.Complete() {
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
	t, err := gcexportdata.Read(r, c.fset, c.universe, ip.PkgPath)
	if err != nil {
		return nil, fmt.Errorf("export data for %s: %w", ip.PkgPath, err)
	}
	return t, nil
}

// importerFunc adapts a function to types.Importer.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
