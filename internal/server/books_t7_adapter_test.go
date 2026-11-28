// file: internal/server/books_t7_adapter_test.go
// version: 1.2.0
// guid: 6e0f2d84-3b7a-4c19-a5d8-92c4e1b7f630
// last-edited: 2026-11-28

package server

import (
	"errors"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// booksT7TwelveKinds is the twelve scheduled server-t7 kinds, written out from
// notYetBuilt's "server t7" rows in internal/books, not derived from the door.
// The door serves forty-five; these are the floor a refactor of the list must
// not drop.
var booksT7TwelveKinds = []tasks.Kind{
	"books.catalog.harvest-authors",
	"books.collections.refresh-relative-dynamic",
	"books.dedup.author-scan",
	"books.dedup.series-normalize",
	"books.dedup.series-prune",
	"books.identification.advance",
	"books.library.organize",
	"books.library.scan",
	"books.library.scan-full",
	"books.library.size-refresh",
	"books.metadata.candidate-fetch",
	"books.reconcile.scan",
}

// booksT7Callable is one adapter method, called through reflection.
type booksT7Callable struct {
	owner string // booksT7Store or booksT7Writer
	name  string
	fn    reflect.Value
}

// booksT7Methods is every exported method of v, and it fails when v has not
// exactly the method set of iface: a method the interface does not name would
// dodge the loop below, and one it names would not compile.
func booksT7Methods(t *testing.T, v any, owner string, iface reflect.Type) []booksT7Callable {
	t.Helper()
	rv := reflect.ValueOf(v)
	require.Equal(t, iface.NumMethod(), rv.NumMethod(), "%s has a method set of its own besides %s", owner, iface)
	var out []booksT7Callable
	for i := range rv.NumMethod() {
		name := rv.Type().Method(i).Name
		_, ok := iface.MethodByName(name)
		require.True(t, ok, "%s.%s is not a method of %s", owner, name, iface)
		out = append(out, booksT7Callable{owner: owner, name: name, fn: rv.Method(i)})
	}
	return out
}

// call calls c with a zero value for every parameter (a variadic parameter gets
// none) and returns the results and what it panicked with, if it did.
func (c booksT7Callable) call() (res []reflect.Value, panicked any) {
	ft := c.fn.Type()
	n := ft.NumIn()
	if ft.IsVariadic() {
		n--
	}
	args := make([]reflect.Value, n)
	for i := range args {
		args[i] = reflect.Zero(ft.In(i))
	}
	defer func() { panicked = recover() }()
	return c.fn.Call(args), nil
}

// booksT7Refusal asserts what a stub does on a zero Server, and never panics:
// a method with an error result returns an *ErrKindNotBuilt that names the
// method and the kinds that reach it, with every other result zero; a method
// without one records its entry and returns zero values.
func booksT7Refusal(t *testing.T, c booksT7Callable) {
	t.Helper()
	id := c.owner + "." + c.name
	before := booksT7StubEntries.Load()
	res, panicked := c.call()
	require.Nil(t, panicked, "%s must never panic", id)
	ft := c.fn.Type()
	last := ft.NumOut() - 1
	if last >= 0 && ft.Out(last) == reflect.TypeFor[error]() {
		require.Equal(t, before, booksT7StubEntries.Load(), "%s has an error result and refuses with it, not by entering a counted body", id)
		err, _ := res[last].Interface().(error)
		var nb *ErrKindNotBuilt
		require.True(t, errors.As(err, &nb), "%s must refuse with an ErrKindNotBuilt, got %v", id, err)
		require.Equal(t, c.name, nb.Method, "%s must refuse by name", id)
		require.Contains(t, err.Error(), c.name)
		require.Equal(t, booksT7KindsReaching(c.name), nb.Kinds, "%s must name the kinds that reach it", id)
		for i := range last {
			require.True(t, res[i].IsZero(), "%s: result %d of a refusal must be zero, got %v", id, i, res[i])
		}
		return
	}
	require.Equal(t, before+1, booksT7StubEntries.Load(), "%s has no error result and must record its entry", id)
	for i, r := range res {
		require.True(t, r.IsZero(), "%s: result %d must be zero, got %v", id, i, r)
	}
}

// Cell: every method of the adapter's store view refuses by name on a zero
// Server, without panicking.
// Mutants, each killed by the named FAIL:
//   - GetAllImportPaths returns (nil, nil): TestBooksT7Adapter_StoreRefusesByName/GetAllImportPaths
//     ("must refuse with an ErrKindNotBuilt").
//   - DedupAvailable returns true without recording its entry:
//     .../DedupAvailable ("must record its entry" and "must be zero").
//   - a method deleted: the build fails at the interface assertions
//     (*booksT7Store does not implement t7Store, missing method ...).
func TestBooksT7Adapter_StoreRefusesByName(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	for _, c := range booksT7Methods(t, deps.Store, "booksT7Store", reflect.TypeFor[t7Store]()) {
		t.Run(c.name, func(t *testing.T) { booksT7Refusal(t, c) })
	}
}

// Cell: the same for the write side, which the kinds get per run.
// Mutants, each killed by the named FAIL: InvalidateDedupCache a no-op that
// does not record its entry (TestBooksT7Adapter_WriterRefusesByName/InvalidateDedupCache:
// "must record its entry"); InvalidateDedupCache that panics (".../
// InvalidateDedupCache: must never panic"); ClaimBook returns a release func
// with its refusal (.../ClaimBook: "result 0 of a refusal must be zero").
func TestBooksT7Adapter_WriterRefusesByName(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	w := deps.NewWriter(&t7GateWriter{name: "run"})
	require.NotNil(t, w)
	for _, c := range booksT7Methods(t, w, "booksT7Writer", reflect.TypeFor[t7Writer]()) {
		t.Run(c.name, func(t *testing.T) { booksT7Refusal(t, c) })
	}
}

// Cell: a refusal names the method it is, not a neighbour, and no method is
// on both views but Ready.
// Mutant: copy-paste a refusal into the wrong method (PerformOrganize naming
// PerformScan), killed by TestBooksT7Adapter_WriterRefusesByName/PerformOrganize
// ("must refuse by name", expected PerformOrganize, actual PerformScan).
func TestBooksT7Adapter_RefusalNamesAreDistinct(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	seen := map[string]string{}
	all := append(
		booksT7Methods(t, deps.Store, "booksT7Store", reflect.TypeFor[t7Store]()),
		booksT7Methods(t, deps.NewWriter(&t7GateWriter{}), "booksT7Writer", reflect.TypeFor[t7Writer]())...,
	)
	for _, c := range all {
		if c.name == "Ready" { // the one name both views have
			continue
		}
		if prev, dup := seen[c.name]; dup {
			t.Fatalf("method %s is on both %s and %s", c.name, prev, c.owner)
		}
		seen[c.name] = c.owner
	}
}

func booksT7KindsOf(defs []tasks.Definition) []tasks.Kind {
	var out []tasks.Kind
	for _, d := range defs {
		out = append(out, d.Kind())
	}
	slices.Sort(out)
	return out
}

// Cell: the door covers every kind t7Kinds builds, builds none whose methods
// are unbuilt, reports each one it leaves out, and enters no stub body.
// Mutants, each killed by FAIL TestBooksT7Kinds_DoorCoversT7Kinds: drop one
// kind from the door's loop ("kinds of the door differ from t7Kinds"); build a
// kind without checking its row ("builds a kind that reaches an unbuilt
// method"); call a method while deciding ("enters no stub body").
func TestBooksT7Kinds_DoorCoversT7Kinds(t *testing.T) {
	s := &Server{}
	deps := s.booksT7Deps("books.library.scan")
	want := booksT7KindsOf(s.t7Kinds(deps, nil))
	require.GreaterOrEqual(t, len(want), 45)
	for _, k := range booksT7TwelveKinds {
		require.Contains(t, want, k, "t7Kinds lost a scheduled server-t7 kind")
	}

	before := booksT7StubEntries.Load()
	res, err := s.BooksT7Kinds("books.library.scan", nil)
	require.NoError(t, err)
	require.Equal(t, before, booksT7StubEntries.Load(), "the door enters no stub body")

	got := booksT7KindsOf(res.Defs)
	for _, nb := range res.NotBuilt {
		got = append(got, nb.Kind)
		require.NotEmpty(t, nb.Unbuilt, "%s is left out with no unbuilt method named", nb.Kind)
		for _, m := range nb.Unbuilt {
			require.Contains(t, booksT7Reaches[nb.Kind], m)
			require.False(t, booksT7MethodBuilt(m), "%s names %s unbuilt but it is built", nb.Kind, m)
		}
		var target *ErrKindNotBuilt
		require.True(t, errors.As(error(nb), &target))
		require.Contains(t, nb.Error(), string(nb.Kind))
	}
	slices.Sort(got)
	require.Equal(t, want, got, "the kinds of the door differ from t7Kinds")
	for _, d := range res.Defs {
		for _, m := range booksT7Reaches[d.Kind()] {
			require.True(t, booksT7MethodBuilt(m), "the door builds a kind that reaches an unbuilt method: %s reaches %s", d.Kind(), m)
		}
	}
	taskstest.Conformance(t, t7KindList(s, deps, nil)...)
}

// Cell: the door builds a kind once every method of its row is built, and
// refuses one whose backing field is nil, naming the field.
// Mutants, each killed by FAIL TestBooksT7Kinds_BuildsOnBuiltMethodsAndFields:
// skip the presence check ("needs Server.store" not found); ignore the built
// table ("is left out" and the Defs assertion).
func TestBooksT7Kinds_BuildsOnBuiltMethodsAndFields(t *testing.T) {
	const sizeRefresh = tasks.Kind("books.library.size-refresh")
	// every kind that reaches GetAllImportPaths: only size-refresh reaches nothing else.
	require.Equal(t, []string{"GetAllImportPaths"}, booksT7Reaches[sizeRefresh])
	prev, had := booksT7Built["GetAllImportPaths"]
	booksT7Built["GetAllImportPaths"] = true
	t.Cleanup(func() {
		if had {
			booksT7Built["GetAllImportPaths"] = prev
		} else {
			delete(booksT7Built, "GetAllImportPaths")
		}
	})

	_, err := (&Server{}).BooksT7Kinds("books.library.scan", nil)
	require.ErrorContains(t, err, "needs Server.store", "a nil backing is refused by field name")
	require.ErrorContains(t, err, string(sizeRefresh))

	s := &Server{store: struct{ database.Store }{}}
	res, err := s.BooksT7Kinds("books.library.scan", nil)
	require.NoError(t, err)
	require.Equal(t, []tasks.Kind{sizeRefresh}, booksT7KindsOf(res.Defs), "only the kind whose every method is built is built")
	taskstest.Conformance(t, res.Defs...)
}

// Cell: the door hands the scan interlock key through, and the two scan kinds
// share it (never split the scan ConcurrencyKey). It reads t7KindList, not the
// door, which leaves both out while their method is unbuilt.
// Mutant: build the deps with StandDownKey "" (killed by FAIL
// TestBooksT7Kinds_StandDownKey, "stand-down key not passed through"). The
// second assertion pins that scan and scan-full share one ConcurrencyKey; it has
// no mutant here because the key comes from HEAD's definition, not from the door
// (the existing TestT7ServerKinds_ScanFullBesideScan holds that half too).
func TestBooksT7Kinds_StandDownKey(t *testing.T) {
	const key = tasks.Key("books.library.scan")
	s := &Server{}
	deps := s.booksT7Deps(key)
	require.Equal(t, key, deps.StandDownKey, "stand-down key not passed through")

	byKind := map[tasks.Kind]tasks.Definition{}
	for _, d := range t7KindList(s, deps, nil) {
		byKind[d.Kind()] = d
	}
	scan := tasks.CommonOf(byKind["books.library.scan"])
	full := tasks.CommonOf(byKind["books.library.scan-full"])
	require.Equal(t, key, scan.ConcurrencyKey)
	require.Equal(t, scan.ConcurrencyKey, full.ConcurrencyKey, "the scan kinds must share one ConcurrencyKey")
}

// booksT7AdapterSources is the non-test books_t7_*.go files of this package.
func booksT7AdapterSources(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("books_t7_*.go")
	require.NoError(t, err)
	out := map[string]string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		require.NoError(t, err)
		out[p] = string(src)
	}
	require.GreaterOrEqual(t, len(out), 5, "the adapter's files were not found")
	return out
}

// Cell: no adapter file panics. A refusal is a returned error or a recorded
// entry, never a panic, dark or not.
// Mutant: put a panic call back into any stub (killed by FAIL
// TestBooksT7Adapter_NeverPanics, "books_t7_<file>.go calls panic").
func TestBooksT7Adapter_NeverPanics(t *testing.T) {
	for name, src := range booksT7AdapterSources(t) {
		require.False(t, strings.Contains(src, "panic"+"("), "%s calls panic", name)
	}
}

// Cell: the door is dark. Nothing but the adapter's own files calls it until the
// registration commit wires it; that commit changes this cell with it.
// Mutant: call s.BooksT7Kinds from any other non-test file (killed by FAIL
// TestBooksT7Kinds_IsDark, "a non-test file reaches the dark t7 door").
func TestBooksT7Kinds_IsDark(t *testing.T) {
	root, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		require.NotEqual(t, root, parent, "no module root above the test's directory")
		root = parent
	}

	var uses []string
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", ".claude", "node_modules", "web", "testdata", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/server/books_t7_adapter.go" || rel == "internal/server/books_t7_door.go" {
			return nil
		}
		src, rerr := os.ReadFile(path)
		require.NoError(t, rerr, rel)
		if strings.Contains(string(src), "BooksT7Kinds") || strings.Contains(string(src), "booksT7Deps(") {
			uses = append(uses, rel)
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "the scan walked the repository, not an empty tree")
	require.Empty(t, uses, "a non-test file reaches the dark t7 door")
}

// booksT7AllMethods is the method names of both interfaces.
func booksT7AllMethods() map[string]bool {
	out := map[string]bool{}
	for _, it := range []reflect.Type{reflect.TypeFor[t7Store](), reflect.TypeFor[t7Writer]()} {
		for i := range it.NumMethod() {
			out[it.Method(i).Name] = true
		}
	}
	return out
}

// Cell: the call map's tables name real things: every method in a row, in the
// built table or in the needs table is a method of t7Store or t7Writer, every
// needed field has a presence check, and every kind of t7KindList has a row
// (and no row names a kind outside it).
// Mutants, each killed by FAIL TestBooksT7Tables_NameRealThings: misspell a
// method in a row ("names a method neither interface has"); drop a kind's row
// ("has no row"); add a needs entry with an unknown field ("has no presence check").
func TestBooksT7Tables_NameRealThings(t *testing.T) {
	methods := booksT7AllMethods()
	listed := map[tasks.Kind]bool{}
	for _, d := range t7KindList(&Server{}, (&Server{}).booksT7Deps(""), nil) {
		listed[d.Kind()] = true
		_, ok := booksT7Reaches[d.Kind()]
		require.True(t, ok, "%s has no row", d.Kind())
	}
	for kind, row := range booksT7Reaches {
		require.True(t, listed[kind], "row for %s, which t7KindList does not build", kind)
		for _, m := range row {
			require.True(t, methods[m], "%s: %s names a method neither interface has", kind, m)
		}
	}
	for m, fields := range booksT7Needs {
		require.True(t, methods[m], "needs row %s names a method neither interface has", m)
		for _, f := range fields {
			require.NotNil(t, booksT7Present[f], "%s needs %s, which has no presence check", m, f)
		}
	}
	for m := range booksT7Built {
		require.True(t, methods[strings.TrimPrefix(strings.TrimPrefix(m, "Store."), "Writer.")], "built table names %s, which is no method", m)
	}
}

// Cell: the built table and the methods' behaviour agree. A method is a stub
// exactly when it is not built (Ready needs both views built). A group that
// builds a method and forgets the table, or sets the table first, fails here.
// Mutants, each killed by FAIL TestBooksT7Stubs_AgreeWithBuilt: set
// booksT7Built["GetAllImportPaths"] while it is still a stub ("is built but
// still refuses"); give a method a body without setting the table ("is not
// built but no longer refuses").
func TestBooksT7Stubs_AgreeWithBuilt(t *testing.T) {
	stubs := BooksT7Stubs()
	isStub := func(side, name string) bool { return slices.Contains(stubs, side+"."+name) }
	storeT, writerT := reflect.TypeFor[t7Store](), reflect.TypeFor[t7Writer]()
	for _, v := range []struct {
		side string
		it   reflect.Type
	}{{"Store", storeT}, {"Writer", writerT}} {
		for i := range v.it.NumMethod() {
			name := v.it.Method(i).Name
			built := booksT7Built[name] || booksT7Built[v.side+"."+name]
			if built {
				require.False(t, isStub(v.side, name), "%s.%s is built but still refuses", v.side, name)
			} else {
				require.True(t, isStub(v.side, name), "%s.%s is not built but no longer refuses", v.side, name)
			}
		}
	}
	if len(booksT7Built) == 0 {
		require.Len(t, stubs, storeT.NumMethod()+writerT.NumMethod(), "every method is a stub while nothing is built")
	}
}

// Cell (slow, skipped under -short): the call map is complete. For each kind
// of t7KindList it walks the kind's constructor in the SSA of this package the
// way the map was derived (static callees, closures and method values in this
// package; a call leaving the package reaches every t7 method of an
// interface-typed parameter) and fails when the walk reaches a method the
// kind's row does not list. A row with an extra method is conservative and is
// not a failure.
// Mutant: delete a method from a row (killed by FAIL
// TestBooksT7Reaches_MatchTheCores, "reaches <method>, which its row omits").
func TestBooksT7Reaches_MatchTheCores(t *testing.T) {
	if testing.Short() {
		t.Skip("loads and builds the SSA of the server package and its dependencies")
	}
	pkgs, err := packages.Load(&packages.Config{Mode: packages.LoadAllSyntax, Dir: "."}, ".")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	require.Empty(t, pkgs[0].Errors)
	prog, ssapkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	sp := ssapkgs[0]
	srvPath := pkgs[0].PkgPath
	names := booksT7AllMethods()

	// t7KindList's constructors, in the order it lists them.
	list := sp.Func("t7KindList")
	require.NotNil(t, list)
	var ctors []*ssa.Function
	for _, b := range list.Blocks {
		for _, ins := range b.Instrs {
			if c, ok := ins.(ssa.CallInstruction); ok {
				if fn := c.Common().StaticCallee(); fn != nil && strings.HasSuffix(fn.Name(), "Native") {
					ctors = append(ctors, fn)
				}
			}
		}
	}
	defs := t7KindList(&Server{}, (&Server{}).booksT7Deps(""), nil)
	require.Len(t, ctors, len(defs), "t7KindList's constructors and its definitions differ")

	for i, ctor := range ctors {
		reached := map[string]bool{}
		seen := map[*ssa.Function]bool{}
		var walk func(f *ssa.Function)
		walk = func(f *ssa.Function) {
			if f == nil || seen[f] || f.Pkg == nil || f.Pkg.Pkg.Path() != srvPath {
				return
			}
			seen[f] = true
			for _, af := range f.AnonFuncs {
				walk(af)
			}
			for _, b := range f.Blocks {
				for _, ins := range b.Instrs {
					if c, ok := ins.(ssa.CallInstruction); ok {
						cc := c.Common()
						if cc.IsInvoke() {
							if m := cc.Method; m.Pkg() != nil && m.Pkg().Path() == srvPath && names[m.Name()] {
								reached[m.Name()] = true
							}
						} else if fn := cc.StaticCallee(); fn != nil {
							if fn.Pkg != nil && fn.Pkg.Pkg.Path() != srvPath {
								params := fn.Signature.Params()
								for p := range params.Len() {
									if it, ok := params.At(p).Type().Underlying().(*types.Interface); ok {
										for j := range it.NumMethods() {
											if names[it.Method(j).Name()] {
												reached[it.Method(j).Name()] = true
											}
										}
									}
								}
							}
							walk(fn)
						}
					}
					if mc, ok := ins.(*ssa.MakeClosure); ok {
						fn := mc.Fn.(*ssa.Function)
						if strings.HasPrefix(fn.Synthetic, "bound method wrapper") && fn.Object() != nil && names[fn.Object().Name()] {
							reached[fn.Object().Name()] = true
						}
						walk(fn)
					}
				}
			}
		}
		walk(ctor)
		kind := defs[i].Kind()
		for m := range reached {
			require.Contains(t, booksT7Reaches[kind], m, "%s (%s) reaches %s, which its row omits", kind, ctor.Name(), m)
		}
	}
}
