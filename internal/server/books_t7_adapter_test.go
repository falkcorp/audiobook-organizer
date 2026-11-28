// file: internal/server/books_t7_adapter_test.go
// version: 1.0.0
// guid: 6e0f2d84-3b7a-4c19-a5d8-92c4e1b7f630
// last-edited: 2026-11-28

package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// booksT7TwelveKinds is the twelve scheduled server-t7 kinds, written out from
// the F5-18 brief and notYetBuilt's "server t7" rows in internal/books, not
// derived from booksT7KindList.
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

// booksT7Probes are the methods whose false is their refusal: an availability
// probe has no error to return and must not panic its caller.
var booksT7Probes = map[string]bool{
	"booksT7Store.Ready":                 true,
	"booksT7Store.DedupAvailable":        true,
	"booksT7Store.ScanApplyAvailable":    true,
	"booksT7Store.CanMergeDeleteAuthors": true,
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

func booksT7ErrorType() reflect.Type { return reflect.TypeFor[error]() }

// booksT7RefusesByName asserts c on a zero Server: an error result is exactly
// "books t7: <Name> is not built" with every other result zero; a method with
// no error result panics with that error; a probe returns false.
func booksT7RefusesByName(t *testing.T, c booksT7Callable) {
	t.Helper()
	want := "books t7: " + c.name + " is not built"
	id := c.owner + "." + c.name
	res, panicked := c.call()
	ft := c.fn.Type()
	last := ft.NumOut() - 1

	switch {
	case booksT7Probes[id]:
		require.Nil(t, panicked, "%s: a probe must not panic", id)
		require.Len(t, res, 1, id)
		require.False(t, res[0].Bool(), "%s: a probe refuses with false", id)
	case last >= 0 && ft.Out(last) == booksT7ErrorType():
		require.Nil(t, panicked, "%s must return its refusal, not panic", id)
		err, _ := res[last].Interface().(error)
		require.EqualError(t, err, want, "%s must refuse by name", id)
		for i := range last {
			require.True(t, res[i].IsZero(), "%s: result %d of a refusal must be zero, got %v", id, i, res[i])
		}
	default:
		require.NotNil(t, panicked, "%s has no error result and must panic by name", id)
		err, ok := panicked.(error)
		require.True(t, ok, "%s panicked with %T, want an error", id, panicked)
		require.EqualError(t, err, want, "%s must refuse by name", id)
	}
}

// Cell: every method of the adapter refuses by name on a zero Server.
// Mutants, each killed by the named FAIL:
//   - GetAllImportPaths returns (nil, nil): FAIL
//     TestBooksT7Adapter_StoreRefusesByName/GetAllImportPaths ("An error is
//     expected but got nil", "must refuse by name").
//   - DedupAvailable returns true: FAIL
//     TestBooksT7Adapter_StoreRefusesByName/DedupAvailable ("a probe refuses
//     with false").
//   - a method deleted: the build fails at the interface assertions
//     (*booksT7Store does not implement t7Store, missing method ...).
func TestBooksT7Adapter_StoreRefusesByName(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	for _, c := range booksT7Methods(t, deps.Store, "booksT7Store", reflect.TypeFor[t7Store]()) {
		t.Run(c.name, func(t *testing.T) { booksT7RefusesByName(t, c) })
	}
}

// Cell: the same for the write side, which the kinds get per run.
// Mutants, each killed by the named FAIL: InvalidateDedupCache a silent no-op
// (TestBooksT7Adapter_WriterRefusesByName/InvalidateDedupCache: "has no error
// result and must panic by name"); ClaimBook returns a release func with its
// refusal (.../ClaimBook: "result 0 of a refusal must be zero").
func TestBooksT7Adapter_WriterRefusesByName(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	w := deps.NewWriter(&t7GateWriter{name: "run"})
	require.NotNil(t, w)
	for _, c := range booksT7Methods(t, w, "booksT7Writer", reflect.TypeFor[t7Writer]()) {
		t.Run(c.name, func(t *testing.T) { booksT7RefusesByName(t, c) })
	}
}

// Cell: a refusal names the method it is, not a neighbour.
// Mutant: copy-paste a refusal into the wrong method (PerformOrganize naming
// PerformScan). Killed by TestBooksT7Adapter_WriterRefusesByName/PerformOrganize
// (expected "books t7: PerformOrganize is not built", actual "... PerformScan ...")
// and by this cell, which also fails when a refusal names no method at all.
func TestBooksT7Adapter_RefusalNamesAreDistinct(t *testing.T) {
	deps := (&Server{}).booksT7Deps("books.library.scan")
	seen := map[string]string{}
	all := append(
		booksT7Methods(t, deps.Store, "booksT7Store", reflect.TypeFor[t7Store]()),
		booksT7Methods(t, deps.NewWriter(&t7GateWriter{}), "booksT7Writer", reflect.TypeFor[t7Writer]())...,
	)
	for _, c := range all {
		if booksT7Probes[c.owner+"."+c.name] {
			continue
		}
		res, panicked := c.call()
		var msg string
		if panicked != nil {
			err, ok := panicked.(error)
			require.True(t, ok, "%s.%s panicked with %T, want an error", c.owner, c.name, panicked)
			msg = err.Error()
		} else if len(res) > 0 {
			if err, ok := res[len(res)-1].Interface().(error); ok && err != nil {
				msg = err.Error()
			}
		}
		require.Contains(t, msg, c.name, "%s.%s", c.owner, c.name)
		// Ready is the one name both views have (bool on the store, error on the
		// writer); every other name is on exactly one side.
		if c.name == "Ready" {
			continue
		}
		if prev, dup := seen[c.name]; dup {
			t.Fatalf("method %s is on both %s and %s", c.name, prev, c.owner)
		}
		seen[c.name] = c.owner
	}
}

// Cell: the door builds exactly the twelve kinds, no more and no fewer.
// Mutants, each killed by FAIL TestBooksT7Kinds_BuildsTheTwelve ("the door's
// kinds differ from notYetBuilt's server t7 rows", with the diff): drop
// s.reconcileScanNative from booksT7KindList, or add s.batchApplyNative (the
// door must not build a kind outside the twelve).
func TestBooksT7Kinds_BuildsTheTwelve(t *testing.T) {
	defs := (&Server{}).BooksT7Kinds("books.library.scan", nil)
	var got []tasks.Kind
	for _, d := range defs {
		got = append(got, d.Kind())
	}
	slices.Sort(got)
	want := slices.Clone(booksT7TwelveKinds)
	slices.Sort(want)
	require.Equal(t, want, got, "the door's kinds differ from notYetBuilt's server t7 rows")
	taskstest.Conformance(t, defs...)
}

// Cell: the door hands the scan interlock key through, and the two scan kinds
// share it (never split the scan ConcurrencyKey).
// Mutant: build the deps with StandDownKey "" (killed by FAIL
// TestBooksT7Kinds_StandDownKey, "stand-down key not passed through"). The
// second assertion pins that scan and scan-full share one ConcurrencyKey; it has
// no mutant here because the key comes from HEAD's definition, not from the door
// (the existing TestT7ServerKinds_ScanFullBesideScan holds that half too).
func TestBooksT7Kinds_StandDownKey(t *testing.T) {
	const key = tasks.Key("books.library.scan")
	require.Equal(t, key, (&Server{}).booksT7Deps(key).StandDownKey, "stand-down key not passed through")

	byKind := map[tasks.Kind]tasks.Definition{}
	for _, d := range (&Server{}).BooksT7Kinds(key, nil) {
		byKind[d.Kind()] = d
	}
	scan := tasks.CommonOf(byKind["books.library.scan"])
	full := tasks.CommonOf(byKind["books.library.scan-full"])
	require.Equal(t, key, scan.ConcurrencyKey)
	require.Equal(t, scan.ConcurrencyKey, full.ConcurrencyKey, "the scan kinds must share one ConcurrencyKey")
}

// Cell: the door is dark. Nothing but the adapter's own file calls it until the
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
		if rel == "internal/server/books_t7_adapter.go" {
			return nil
		}
		src, rerr := os.ReadFile(path)
		require.NoError(t, rerr, rel)
		if strings.Contains(string(src), "BooksT7Kinds") || strings.Contains(string(src), "booksT7Deps(") || strings.Contains(string(src), "booksT7KindList(") {
			uses = append(uses, rel)
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "the scan walked the repository, not an empty tree")
	require.Empty(t, uses, "a non-test file reaches the dark t7 door")
}
