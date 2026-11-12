// file: core/internal/bleveindex/index_test.go
// version: 1.0.1
// guid: a08fe3e7-f1a2-4bf1-a2ca-de739c2ce218
// last-edited: 2026-11-11

package bleveindex

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"
)

// seeded is a rig whose index holds five villains and has left Rebuilding.
//
//go:fix inline
func seeded(t *testing.T, opts ...func(*Options)) *rig {
	t.Helper()
	r := newRig(t, opts...)
	r.save("joker", "The Joker", "clown", 1940)
	r.save("penguin", "The Penguin", "bird", 1941)
	r.save("riddler", "The Riddler", "puzzle", 1948)
	r.save("bane", "Bane", "luchador", 1993)
	r.save("twoface", "Two Face", "coin", 1942)
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSearchQueriesAcrossFieldKinds(t *testing.T) {
	r := seeded(t)
	for name, tc := range map[string]struct {
		q    index.Query
		want []string
	}{
		"match text":           {index.Match("name", "penguin"), []string{"penguin"}},
		"match is case folded": {index.Match("name", "PENGUIN"), []string{"penguin"}},
		"term keyword":         {index.Term("alias", "clown"), []string{"joker"}},
		"term is exact":        {index.Term("alias", "clo"), nil},
		"prefix":               {index.Prefix("alias", "c"), []string{"joker", "twoface"}},
		"number range":         {index.Range("year", new(int64(1941)), new(int64(1945))), []string{"penguin", "twoface"}},
		"open number end":      {index.Range[int64]("year", new(int64(1990)), nil), []string{"bane"}},
		"float range":          {index.Range("year", new(1947.5), new(1948.5)), []string{"riddler"}},
		"date range":           {index.Range("debut", new(time.Date(1941, 1, 1, 0, 0, 0, 0, time.UTC)), new(time.Date(1941, 12, 31, 0, 0, 0, 0, time.UTC))), []string{"penguin"}},
		"bool term":            {index.Term("free", "false"), []string{"bane", "joker", "penguin", "riddler", "twoface"}},
		"bool true":            {index.Term("free", "true"), nil},
		"number term":          {index.Term("year", "1993"), []string{"bane"}},
		"and":                  {index.And(index.Prefix("alias", "c"), index.Range("year", new(int64(1941)), nil)), []string{"twoface"}},
		"or":                   {index.Or(index.Term("alias", "bird"), index.Term("alias", "puzzle")), []string{"penguin", "riddler"}},
		"not":                  {index.Not(index.Term("alias", "clown")), []string{"bane", "penguin", "riddler", "twoface"}},
		"query string":         {index.QueryString("name:riddler"), []string{"riddler"}},
	} {
		if got := r.ids(tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
		if n := r.count(tc.q); n != uint64(len(tc.want)) {
			t.Errorf("%s: count %d, want %d", name, n, len(tc.want))
		}
	}
}

func TestSearchRefusesWhatItCannotAnswer(t *testing.T) {
	r := seeded(t)
	for name, q := range map[string]index.Query{
		"unknown field":      index.Match("nope", "x"),
		"match on a number":  index.Match("year", "1940"),
		"prefix on a date":   index.Prefix("debut", "19"),
		"term on a date":     index.Term("debut", "1940"),
		"bad bool":           index.Term("free", "maybe"),
		"range on text":      index.Range("name", new(int64(1)), nil),
		"time range, number": index.Range("year", new(time.Now()), nil),
		"int range, date":    index.Range("debut", new(int64(1)), nil),
		"nil":                nil,
	} {
		if _, err := r.ix.Search(t.Context(), q, index.Page{}); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
	if _, err := r.ix.Search(t.Context(), index.Match("name", "joker"), index.Page{From: -1}); !errors.Is(err, index.ErrInvalidQuery) {
		t.Errorf("negative From: %v", err)
	}
	if _, err := r.ix.Search(t.Context(), index.Match("name", "joker"), index.Page{From: 9990, Size: 100}); !errors.Is(err, index.ErrInvalidQuery) {
		t.Errorf("a window past the cap: %v", err)
	}
}

// TestPageWindowRefusesOverflowingBounds pins the cap against integer
// overflow: From+Size wraps for a From near MaxInt, and a wrapped (negative)
// sum must not pass as "inside the window", or bleve allocates that window.
func TestPageWindowRefusesOverflowingBounds(t *testing.T) {
	for name, p := range map[string]index.Page{
		"from MaxInt-1, default size": {From: math.MaxInt - 1},
		"from MaxInt-1, size 10":      {From: math.MaxInt - 1, Size: 10},
		"from MaxInt":                 {From: math.MaxInt, Size: 1},
		"size MaxInt":                 {Size: math.MaxInt},
		"from 1, size MaxInt":         {From: 1, Size: math.MaxInt},
		"both huge":                   {From: math.MaxInt, Size: math.MaxInt},
		"from past cap":               {From: maxResultWindow + 1, Size: 1},
		"size past cap":               {Size: maxResultWindow + 1},
		"negative size":               {Size: -1},
		"negative from":               {From: -1},
		"sum one past cap":            {From: maxResultWindow, Size: 1},
	} {
		if _, err := pageWindow(p); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
	for name, p := range map[string]index.Page{
		"zero page":             {},
		"last full page":        {From: maxResultWindow - 10, Size: 10},
		"whole window":          {Size: maxResultWindow},
		"explicit default size": {Size: defaultPageSize},
	} {
		if _, err := pageWindow(p); err != nil {
			t.Errorf("%s: %v, want accepted", name, err)
		}
	}
	r := seeded(t)
	q := index.Match("name", "joker")
	if _, err := r.ix.Search(t.Context(), q, index.Page{From: math.MaxInt - 1}); !errors.Is(err, index.ErrInvalidQuery) {
		t.Errorf("Search with From = MaxInt-1: %v, want ErrInvalidQuery", err)
	}
	if _, err := r.ix.Facets(t.Context(), q, []string{"alias"}, math.MaxInt); !errors.Is(err, index.ErrInvalidQuery) {
		t.Errorf("Facets with size MaxInt: %v, want ErrInvalidQuery", err)
	}
}

func TestSearchPagingSortAndStoredFields(t *testing.T) {
	r := seeded(t)
	all := index.Prefix("alias", "")
	res, err := r.ix.Search(t.Context(), all, index.Page{Size: 2, SortBy: []string{"-year"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Hits) != 2 || res.Hits[0].ID != "bane" || res.Hits[1].ID != "riddler" {
		t.Fatalf("page 1 by -year: %+v", res)
	}
	if res.Hits[0].Fields["name"] != "Bane" || res.Hits[0].Fields["alias"] != "luchador" {
		t.Fatalf("stored fields: %v", res.Hits[0].Fields)
	}
	next, err := r.ix.Search(t.Context(), all, index.Page{From: 2, Size: 2, SortBy: []string{"-year"}})
	if err != nil || len(next.Hits) != 2 || next.Hits[0].ID != "twoface" {
		t.Fatalf("page 2: %+v %v", next, err)
	}
	def, err := r.ix.Search(t.Context(), index.Match("name", "joker"), index.Page{})
	if err != nil || len(def.Hits) != 1 || def.Hits[0].Score <= 0 {
		t.Fatalf("default page: %+v %v", def, err)
	}
}

func TestFacets(t *testing.T) {
	r := seeded(t)
	r.save("harley", "Harley Quinn", "clown", 1992)
	r.reconcile()
	fs, err := r.ix.Facets(t.Context(), index.Prefix("alias", ""), []string{"alias", "free"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs["alias"]) != 3 || fs["alias"][0] != (index.FacetValue{Term: "clown", Count: 2}) {
		t.Fatalf("alias facet: %v", fs["alias"])
	}
	if len(fs["free"]) != 1 || fs["free"][0].Count != 6 {
		t.Fatalf("free facet: %v", fs["free"])
	}
	if _, err := r.ix.Facets(t.Context(), index.Prefix("alias", ""), []string{"name"}, 3); !errors.Is(err, index.ErrInvalidQuery) {
		t.Fatalf("a non-facet field: %v", err)
	}
	only, err := r.ix.Facets(t.Context(), index.Term("alias", "clown"), []string{"alias"}, 5)
	if err != nil || len(only["alias"]) != 1 || only["alias"][0].Count != 2 {
		t.Fatalf("facet over a query: %v %v", only, err)
	}
}

// TestIndexOpenFailureDegrades is I4: an index that cannot open costs search,
// never boot, and the dirty marks written meanwhile are kept.
func TestIndexOpenFailureDegrades(t *testing.T) {
	t.Run("a corrupt index directory", func(t *testing.T) {
		f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
		dir := t.TempDir()
		path := indexDir(dir)
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "index_meta.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A complete index that will not open: the mapping marker matches and there
		// is no rebuilding marker, so this is damage, not an interrupted create.
		if err := os.WriteFile(mappingMarkerPath(path), []byte(mappingStamp(newSpec(f, "1", nil))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := rigOn(t, f, dir, "1") // Open must not fail
		if got := r.ix.State(); got != index.Degraded {
			t.Fatalf("state = %v, want Degraded", got)
		}
		if _, err := r.ix.Search(t.Context(), index.Match("name", "joker"), index.Page{}); !errors.Is(err, index.ErrDegraded) {
			t.Fatalf("Search on a degraded index: %v", err)
		}
		if _, err := r.ix.Count(t.Context(), index.Match("name", "joker")); !errors.Is(err, index.ErrDegraded) {
			t.Fatalf("Count: %v", err)
		}
		if _, err := r.ix.Facets(t.Context(), index.Match("name", "joker"), []string{"alias"}, 1); !errors.Is(err, index.ErrDegraded) {
			t.Fatalf("Facets: %v", err)
		}
		// A mark is durable even though nothing can index it, and a pass skips
		// the index without an error.
		r.save("joker", "The Joker", "clown", 1940)
		if n := r.dirtyCount(); n != 1 {
			t.Fatalf("dirty marks = %d, want 1", n)
		}
		r.reconcile()
		if n := r.dirtyCount(); n != 1 {
			t.Fatalf("a degraded index drained its marks: %d", n)
		}
		if err := r.ix.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an unwritable index root", func(t *testing.T) {
		f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := rigOn(t, f, filepath.Join(blocker, "index"), "1")
		if got := r.ix.State(); got != index.Degraded {
			t.Fatalf("state = %v, want Degraded", got)
		}
	})
	t.Run("a rebuild that cannot create the index reports it Degraded", func(t *testing.T) {
		f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := rigOn(t, f, filepath.Join(blocker, "index"), "1")
		if _, err := runRebuild(t, r.e); !errors.Is(err, index.ErrDegraded) {
			t.Fatalf("rebuild: %v", err)
		}
	})
}

// TestIndexMappingChangeRebuilds is I4: a changed mapping version drops the
// index and rebuilds it; an unchanged one keeps it.
func TestIndexMappingChangeRebuilds(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	dir := t.TempDir()
	r := rigOn(t, f, dir, "1")
	r.save("joker", "The Joker", "clown", 1940)
	r.save("bane", "Bane", "luchador", 1993)
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	if r.ix.State() != index.Ready || r.count(index.Prefix("alias", "")) != 2 {
		t.Fatalf("setup: state %v, count %d", r.ix.State(), r.count(index.Prefix("alias", "")))
	}
	if err := r.ix.Close(); err != nil {
		t.Fatal(err)
	}

	// The same version: the index is kept, Ready, with its documents.
	same := rigOn(t, f, dir, "1")
	if same.ix.State() != index.Ready || same.count(index.Prefix("alias", "")) != 2 {
		t.Fatalf("same version: state %v, count %d", same.ix.State(), same.count(index.Prefix("alias", "")))
	}
	if got := same.e.PendingRebuilds(); len(got) != 0 {
		t.Fatalf("a kept index asks for a rebuild: %v", got)
	}
	if err := same.ix.Close(); err != nil {
		t.Fatal(err)
	}

	// A new mapping version: dropped, Rebuilding, empty, and it asks for a rebuild.
	next := rigOn(t, f, dir, "2")
	if next.ix.State() != index.Rebuilding {
		t.Fatalf("a changed mapping version: state %v, want Rebuilding", next.ix.State())
	}
	if n := next.count(index.Prefix("alias", "")); n != 0 {
		t.Fatalf("the stale index survived with %d documents", n)
	}
	if got := next.e.PendingRebuilds(); len(got) != 1 || got[0] != (Ref{string(indextest.Module), indextest.Signal}) {
		t.Fatalf("PendingRebuilds = %v", got)
	}
	if !next.e.mustIdx(t, indextest.Signal).recreatedForMapping {
		t.Fatal("the recreation was not recorded")
	}
	if _, err := runRebuild(t, next.e); err != nil {
		t.Fatal(err)
	}
	if next.ix.State() != index.Ready || next.count(index.Prefix("alias", "")) != 2 {
		t.Fatalf("after the rebuild: state %v, count %d", next.ix.State(), next.count(index.Prefix("alias", "")))
	}
	if got := next.e.PendingRebuilds(); len(got) != 0 {
		t.Fatalf("PendingRebuilds after the rebuild: %v", got)
	}
	if err := next.ix.Close(); err != nil {
		t.Fatal(err)
	}

	// An index with no mapping marker is the pre-marker mapping: dropped.
	if err := os.Remove(mappingMarkerPath(indexDir(dir))); err != nil {
		t.Fatal(err)
	}
	unmarked := rigOn(t, f, dir, "2")
	if unmarked.ix.State() != index.Rebuilding || unmarked.count(index.Prefix("alias", "")) != 0 {
		t.Fatalf("no marker: state %v", unmarked.ix.State())
	}
}

// TestFreshIndexIsRebuildingUntilRebuilt: the first boot finds no state, so the
// index is Rebuilding, and it leaves that state only after a rebuild AND an
// empty dirty set (HEAD's rule).
func TestFreshIndexIsRebuildingUntilRebuilt(t *testing.T) {
	r := newRig(t)
	if r.ix.State() != index.Rebuilding {
		t.Fatalf("fresh index: %v", r.ix.State())
	}
	if _, err := os.Stat(rebuildingMarkerPath(indexDir(r.dir))); err != nil {
		t.Fatalf("the rebuilding marker must exist from creation: %v", err)
	}
	r.save("joker", "The Joker", "clown", 1940)
	r.reconcile() // a drain alone does not end Rebuilding: nothing seeded the library
	if r.ix.State() != index.Rebuilding {
		t.Fatalf("a drain without a rebuild ended Rebuilding")
	}
	if got := r.ids(index.Match("name", "joker")); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("a Rebuilding index answers from what it holds: %v", got)
	}
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	if r.ix.State() != index.Ready {
		t.Fatalf("after rebuild: %v", r.ix.State())
	}
	if _, err := os.Stat(rebuildingMarkerPath(indexDir(r.dir))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the rebuilding marker outlived the rebuild: %v", err)
	}
}

func TestOpenRefusals(t *testing.T) {
	r := newRig(t)
	if _, err := r.e.Opener(string(indextest.Module)).Open(t.Context(), r.spec); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("a second Open: %v", err)
	}
	bad := r.spec
	bad.Fields = nil
	if _, err := r.e.Opener(string(indextest.Module)).Open(t.Context(), bad); !errors.Is(err, index.ErrInvalidSpec) {
		t.Fatalf("invalid spec: %v", err)
	}
	odd := r.spec
	odd.Name = indextest.Cave
	odd.Fields = []index.Field{{Name: "x", Kind: index.Text, Analyzer: "nightwing"}}
	if _, err := r.e.Opener(string(indextest.Module)).Open(t.Context(), odd); !errors.Is(err, index.ErrInvalidSpec) {
		t.Fatalf("an unregistered analyzer: %v", err)
	}
	if _, err := r.e.Opener("Bad Module").Open(t.Context(), r.spec); err == nil {
		t.Fatal("a bad module name opened")
	}
	undeclared := r.spec
	undeclared.Name = "joker"
	if _, err := r.e.Opener(string(indextest.Module)).Open(t.Context(), undeclared); !errors.Is(err, storage.ErrDerivedUndeclared) {
		t.Fatalf("an index its module did not declare as derived: %v", err)
	}
	if err := r.ix.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.ix.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
	if _, err := r.ix.Count(t.Context(), index.Match("name", "x")); !errors.Is(err, index.ErrClosed) {
		t.Fatalf("Count after Close: %v", err)
	}
	b := r.f.Module.NewBatch()
	defer b.Close()
	if err := r.ix.MarkDirty(b, "x"); !errors.Is(err, index.ErrClosed) {
		t.Fatalf("MarkDirty after Close: %v", err)
	}
	// Closed indexes leave the engine: the name is free again.
	r.open()
	if r.ix.State() == index.StateInvalid {
		t.Fatal("reopen")
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New with no options")
	}
	if _, err := New(Options{Dir: "relative", Root: r.f.Root, Derived: func(string, string) (storage.Derived, error) { return storage.Derived{}, nil }}); err == nil {
		t.Fatal("New with a relative Dir")
	}
}
