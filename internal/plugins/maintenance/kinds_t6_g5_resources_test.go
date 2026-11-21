// file: internal/plugins/maintenance/kinds_t6_g5_resources_test.go
// version: 1.2.1
// guid: 5c9b940e-34ff-4da6-88ba-5dbfff0b5b2a
// last-edited: 2026-11-15

package maintenance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests tie the three places a resource name lives: the method table
// (t6g5WriterResources, the one F5-18's writer reads), each kind's declared
// Effects, and the pkg/ops vocabulary. They compare VALUES and walk the parsed
// source, so a spelling trick (a split line, a raw string, an alias constant, a
// helper) cannot get past them the way it got past a line regex.

// opsResourceNames parses pkg/ops/effects.go and returns every constant of type
// Resource, as name -> value. It is read from source, not hand-typed, so a
// constant added to pkg/ops is known here the day it lands. The path is found
// from the working directory (CI builds with -trimpath, which breaks
// runtime.Caller).
func opsResourceNames(t *testing.T) map[string]string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	src := filepath.Join(wd, "..", "..", "..", "pkg", "ops", "effects.go")
	f, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
	require.NoError(t, err)
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Resource" {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				require.True(t, ok, "%s is a string literal constant", n.Name)
				val, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				out[n.Name] = val
			}
		}
	}
	require.NotEmpty(t, out, "pkg/ops/effects.go declares Resource constants")
	require.Equal(t, "books", out["ResBooks"], "the parse reads the real vocabulary")
	return out
}

func opsResourceValues(t *testing.T) map[string]bool {
	t.Helper()
	vals := map[string]bool{}
	for _, v := range opsResourceNames(t) {
		vals[v] = true
	}
	return vals
}

// t6g5KindWriterIfaces names, for each converted kind, the writer interfaces its
// core is handed, so "the methods this kind can call" is read from the compiled
// interface and not copied by hand. A kind that gains a method in one of these
// interfaces gains its resources here, and its Effects must follow.
func t6g5KindWriterIfaces() map[string][]reflect.Type {
	return map[string][]reflect.Type{
		"itunes.clone-into-library":          {reflect.TypeFor[icStoreWriter]()},
		"itunes.playlist-import":             {reflect.TypeFor[playlistImportWriter]()},
		"itunes.regroup":                     {reflect.TypeFor[itunesRegroupWriter]()},
		"maintenance.repair-library-state":   {reflect.TypeFor[libraryStateRepairModifier]()},
		"maintenance.mark-missing-files":     {reflect.TypeFor[bookFileBatchWriter]()},
		"maintenance.merge-same-path-dupes":  {reflect.TypeFor[mergeSamePathMerger]()},
		"maintenance.metadata-cache-reap":    {reflect.TypeFor[reapCacheDeleter]()},
		"maintenance.missing-file-repoint":   {reflect.TypeFor[bookFileBatchWriter]()},
		"maintenance.move-book-file-rows":    {reflect.TypeFor[mbfWriter]()},
		"maintenance.purge-empty-narrators":  {reflect.TypeFor[narratorPurger]()},
		"maintenance.split-joined-narrators": {reflect.TypeFor[narratorSplitWriter]()},
	}
}

func ifaceMethods(types []reflect.Type) []string {
	seen := map[string]bool{}
	for _, ty := range types {
		for method := range ty.Methods() {
			seen[method.Name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// TestT6G5Resources_TableCoversEveryWriterMethod: the production table names
// exactly the methods of t6g5Writer, by reflection. A method added to the
// interface needs a row before any native test (or F5-18's writer) can use it.
func TestT6G5Resources_TableCoversEveryWriterMethod(t *testing.T) {
	typ := reflect.TypeFor[t6g5Writer]()
	var methods, rows []string
	for method := range typ.Methods() {
		methods = append(methods, method.Name)
	}
	for name := range t6g5WriterResources {
		rows = append(rows, name)
	}
	sort.Strings(methods)
	sort.Strings(rows)
	assert.Equal(t, methods, rows, "t6g5WriterResources names exactly the methods of t6g5Writer")
	for name, rs := range t6g5WriterResources {
		assert.NotNil(t, rs, "%s: an empty row is written as an explicit empty list, never left nil", name)
	}
}

// TestT6G5Resources_EffectsAreTheUnionOfTheirMethods: each writing kind's
// declared write set equals the union of the resources of every method it can
// call, plus the writes its core makes outside the writer (t6g5OutsideWriterWrites)
// and the capabilities it keeps only for HEAD parity (t6g5HeadParityDeclared),
// each with a stated reason. Equality, not a subset: an Effects entry nothing
// writes, and a method resource the kind does not declare, both fail.
func TestT6G5Resources_EffectsAreTheUnionOfTheirMethods(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g5Kinds(fakeT6G5Deps(t), nil)
	ifaces := t6g5KindWriterIfaces()
	wants := t6g5Wants()
	require.Len(t, defs, len(wants))
	for i, w := range wants {
		if w.readOnly {
			assert.NotContains(t, ifaces, w.headID, "a read-only kind has no writer interface")
			assert.NotContains(t, t6g5OutsideWriterWrites, w.headID)
			assert.NotContains(t, t6g5HeadParityDeclared, w.headID)
			continue
		}
		t.Run(w.headID, func(t *testing.T) {
			types, ok := ifaces[w.headID]
			require.True(t, ok, "%s is mapped to the writer interfaces its core takes", w.headID)
			union := map[string]bool{}
			for _, m := range ifaceMethods(types) {
				rs, ok := t6g5WriterResources[m]
				require.True(t, ok, "method %s has a row in t6g5WriterResources", m)
				for _, r := range rs {
					union[string(r)] = true
				}
			}
			for _, o := range t6g5OutsideWriterWrites[w.headID] {
				assert.NotEmpty(t, strings.TrimSpace(o.Why), "an outside write states why the writer does not carry it")
				union[string(o.Resource)] = true
			}
			for _, o := range t6g5HeadParityDeclared[w.headID] {
				assert.NotEmpty(t, strings.TrimSpace(o.Why), "a head-parity declaration states why nothing on the path writes it")
				union[string(o.Resource)] = true
			}
			want := make([]string, 0, len(union))
			for r := range union {
				want = append(want, r)
			}
			sort.Strings(want)
			assert.Equal(t, want, t6g5WriteKinds(t, tasks.CommonOf(defs[i]).Effects),
				"declared Effects equal the union of the methods this kind can call plus its reasoned outside writes")
		})
	}
	for id := range t6g5OutsideWriterWrites {
		_, ok := ifaces[id]
		assert.True(t, ok, "t6g5OutsideWriterWrites names a real writing kind (%s)", id)
	}
	for id, parity := range t6g5HeadParityDeclared {
		_, ok := ifaces[id]
		assert.True(t, ok, "t6g5HeadParityDeclared names a real writing kind (%s)", id)
		for _, o := range parity {
			for _, m := range ifaceMethods(ifaces[id]) {
				assert.NotContains(t, t6g5WriterResources[m], o.Resource,
					"%s: a head-parity resource is one NO method carries; %s does, so it is a traced write and belongs to the table", id, m)
			}
		}
	}
}

// t6g5SourceFiles are the files that build this row's kinds (each declares one
// kind's Effects with tasks.Res) and the group's shared kinds_t6_g5*.go files.
// The syntax-tree check below is scoped to them on purpose: sibling groups
// (F5-07, F5-08, F5-09) build their Effects from typed ops.Resource helpers
// (tasks.Res("books", string(r))), which is correct for them and which a whole
// package walk would fail the day they stack with this row. The value checks
// still cover every declared name wherever it was written.
var t6g5SourceFiles = []string{
	"itunes_clone_into_library.go",
	"itunes_playlist_import.go",
	"itunes_regroup.go",
	"library_state_repair.go",
	"mark_missing_files.go",
	"merge_same_path_dupes.go",
	"metadata_cache_reap.go",
	"missing_file_repoint.go",
	"move_book_file_rows.go",
	"narrator_purge_empty.go",
	"narrator_split_joined.go",
}

// TestT6G5Resources_SourceFilesListIsComplete: the hand list above is checked
// against the files themselves. A file of this package that is not a test, is
// not one of the group's own kinds_t6_g5*.go files, declares an Effects with
// tasks.Res and names this row's wiring (t6g5...) is one of this row's kind
// files. A kind file added without joining t6g5SourceFiles would escape the
// syntax-tree check below, and a listed file that stopped declaring Effects
// would keep a stale entry; this test fails on either. A sibling group's file
// (typed helpers, its own wiring names) matches neither condition.
func TestT6G5Resources_SourceFilesListIsComplete(t *testing.T) {
	all, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var derived []string
	for _, name := range all {
		if strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "kinds_t6_g5") {
			continue
		}
		b, err := os.ReadFile(name)
		require.NoError(t, err)
		if strings.Contains(string(b), "tasks.Res(") && strings.Contains(string(b), "t6g5") {
			derived = append(derived, name)
		}
	}
	assert.ElementsMatch(t, t6g5SourceFiles, derived,
		"t6g5SourceFiles must list exactly the non-test files that declare Effects with tasks.Res and name t6g5 wiring")
}

// TestT6G5Resources_EveryNameIsAnOpsConstant: every resource name this group
// declares, wherever it is written down, is the VALUE of a pkg/ops Resource
// constant. It walks the live data (the method table, the outside writes, the
// head-parity list, every kind's write set, and the expected write sets in the
// test tables) and the parsed source of this row's files (t6g5SourceFiles and
// kinds_t6_g5*.go, tests included). A coined name compiles (an untyped string
// constant converts to ops.Resource), so this test is the only guard against one.
func TestT6G5Resources_EveryNameIsAnOpsConstant(t *testing.T) {
	names := opsResourceNames(t)
	values := opsResourceValues(t)

	for method, rs := range t6g5WriterResources {
		for _, r := range rs {
			assert.True(t, values[string(r)], "t6g5WriterResources[%s] names %q, which is no pkg/ops constant", method, r)
		}
	}
	for id, outs := range t6g5OutsideWriterWrites {
		for _, o := range outs {
			assert.True(t, values[string(o.Resource)], "t6g5OutsideWriterWrites[%s] names %q, which is no pkg/ops constant", id, o.Resource)
		}
	}
	defs := (&Plugin{}).t6g5Kinds(fakeT6G5Deps(t), nil)
	for _, d := range defs {
		for _, r := range tasks.CommonOf(d).Effects.WriteSet() {
			assert.True(t, values[r.Kind], "%s declares %q, which is no pkg/ops constant", d.Kind(), r.Kind)
		}
	}
	for id, outs := range t6g5HeadParityDeclared {
		for _, o := range outs {
			assert.True(t, values[string(o.Resource)], "t6g5HeadParityDeclared[%s] names %q, which is no pkg/ops constant", id, o.Resource)
		}
	}

	// Source walk: the second argument of every tasks.Res call in this row's
	// files must be string(ops.<one of the Resource constants>).
	files := slices.Clone(t6g5SourceFiles)
	group, err := filepath.Glob("kinds_t6_g5*.go")
	require.NoError(t, err)
	files = append(files, group...)
	fset := token.NewFileSet()
	calls := 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSelector(call.Fun, "tasks", "Res") {
				return true
			}
			calls++
			pos := fset.Position(call.Pos())
			if !assert.Len(t, call.Args, 2, "%s: tasks.Res takes a module and a kind", pos) {
				return true
			}
			conv, ok := call.Args[1].(*ast.CallExpr)
			if !assert.True(t, ok && isIdent(conv.Fun, "string") && len(conv.Args) == 1,
				"%s: a resource kind is written string(ops.ResX), not a literal, alias or helper result", pos) {
				return true
			}
			sel, ok := conv.Args[0].(*ast.SelectorExpr)
			if !assert.True(t, ok && isIdent(sel.X, "ops"), "%s: the converted value is an ops.<constant>", pos) {
				return true
			}
			_, known := names[sel.Sel.Name]
			assert.True(t, known, "%s: ops.%s is not a Resource constant in pkg/ops/effects.go", pos, sel.Sel.Name)
			return true
		})
	}
	require.Positive(t, calls, "the walk found this row's tasks.Res calls")
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name && isIdent(sel.X, pkg)
}
