// file: core/internal/bleveindex/review_test.go
// version: 1.0.0
// guid: 5a21dcba-39eb-4d8d-ab72-f47d2f10ccd2
// last-edited: 2026-11-11

package bleveindex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"
)

// artifactDocCount opens a copied artifact and counts its documents.
func artifactDocCount(t *testing.T, dir string) uint64 {
	t.Helper()
	bi, err := bleve.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer bi.Close()
	n, err := bi.DocCount()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *rig) current() storage.Generation {
	r.t.Helper()
	g, err := r.f.Derived(indextest.Signal).Current(r.t.Context())
	if err != nil {
		r.t.Fatal(err)
	}
	return g
}

// ---- CopyTo: the artifact's stamp must never outlive its truth ----

// TestCopyToRefusesWhileMarksArePending: the generation does not move when the
// reconciler indexes a mark, so an artifact copied with a mark pending would be
// stamped with the very generation the store has after the drain, and accepted
// with a document missing. It is refused until the marks are drained.
func TestCopyToRefusesWhileMarksArePending(t *testing.T) {
	r := seeded(t)
	r.save("harley", "Harley Quinn", "clown", 1992) // committed, not drained
	dir := filepath.Join(t.TempDir(), "copy")
	err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir)
	if !errors.Is(err, ErrNotQuiescent) {
		t.Fatalf("CopyTo with a pending mark: %v, want ErrNotQuiescent", err)
	}
	if _, serr := os.Stat(dir); serr == nil {
		t.Fatal("a refused copy left an artifact directory behind")
	}
	r.reconcile()
	if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir); err != nil {
		t.Fatalf("CopyTo after the drain: %v", err)
	}
	if ok, err := storage.ArtifactAccepted(dir, r.current()); err != nil || !ok {
		t.Fatalf("the artifact of a drained index: accepted=%v err=%v", ok, err)
	}
	if n := artifactDocCount(t, dir); n != 6 {
		t.Fatalf("the accepted artifact holds %d documents, the source has 6", n)
	}
}

// TestCopyToSeesAMarkCommittedAfterTheGenerationRead: the generation is read
// first and the dirty set second. A mark committed between the two is in the
// generation (its batch touched it) and not yet in the index, so only the
// pending check, run after the read, keeps it from being stamped as complete.
func TestCopyToSeesAMarkCommittedAfterTheGenerationRead(t *testing.T) {
	r := seeded(t)
	r.e.mustIdx(t, indextest.Signal).afterGenerationRead = func() {
		r.save("harley", "Harley Quinn", "clown", 1992)
	}
	dir := filepath.Join(t.TempDir(), "copy")
	if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir); !errors.Is(err, ErrNotQuiescent) {
		t.Fatalf("CopyTo with a mark committed after the generation read: %v, want ErrNotQuiescent", err)
	}
}

// TestCopyToRefusesAnIndexThatIsNotReady: a Rebuilding index does not cover its
// source, and the .rebuilding marker that says so is a sibling the copy does
// not carry, so the artifact would later load as a complete index.
func TestCopyToRefusesAnIndexThatIsNotReady(t *testing.T) {
	t.Run("rebuilding", func(t *testing.T) {
		r := newRig(t)
		r.save("joker", "The Joker", "clown", 1940)
		r.reconcile() // a fresh index drains but stays Rebuilding until a rebuild ran
		if r.ix.State() != index.Rebuilding {
			t.Fatalf("setup: state %v", r.ix.State())
		}
		dir := filepath.Join(t.TempDir(), "copy")
		if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir); !errors.Is(err, ErrNotReady) {
			t.Fatalf("CopyTo of a Rebuilding index: %v, want ErrNotReady", err)
		}
		if _, serr := os.Stat(dir); serr == nil {
			t.Fatal("a refused copy left an artifact directory behind")
		}
	})
	t.Run("degraded", func(t *testing.T) {
		f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := rigOn(t, f, filepath.Join(blocker, "index"), "1")
		if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, filepath.Join(t.TempDir(), "copy")); !errors.Is(err, ErrNotReady) {
			t.Fatalf("CopyTo of a Degraded index: %v, want ErrNotReady", err)
		}
	})
}

// TestCopyToHoldsTheWriterLockForTheWholeCopy: a reconcile pass that ran during
// the copy would change the content the artifact is stamped for.
func TestCopyToHoldsTheWriterLockForTheWholeCopy(t *testing.T) {
	r := seeded(t)
	var pendingDuringCopy int
	r.e.mustIdx(t, indextest.Signal).afterQuiescenceCheck = func() {
		r.save("harley", "Harley Quinn", "clown", 1992) // a write during the copy
		r.reconcile()                                   // a pass during the copy must skip the index
		pendingDuringCopy = r.dirtyCount()
	}
	dir := filepath.Join(t.TempDir(), "copy")
	if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir); err != nil {
		t.Fatal(err)
	}
	if pendingDuringCopy != 1 {
		t.Fatalf("a reconcile pass applied marks during the copy (pending %d, want 1)", pendingDuringCopy)
	}
	if ok, _ := storage.ArtifactAccepted(dir, r.current()); ok {
		t.Fatal("an artifact that missed a write committed during the copy was accepted")
	}
}

// ---- open vs the reconciler ----

// TestOpenWhileTheReconcilerRuns: an index is published to the reconciler only
// after its open finished. Run under -race, the old order (reserve, then open
// in place) is a data race on the handle and the state.
func TestOpenWhileTheReconcilerRuns(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	e := engineFor(t, f, t.TempDir(), func(o *Options) { o.Interval = func() time.Duration { return time.Millisecond } })
	ds := f.DirtySet(indextest.Signal)
	for _, id := range []string{"a", "b", "c"} {
		b := f.Module.NewBatch()
		if err := b.Set([]byte("villain/"+id), villainRow("x", "y", 1, false)); err != nil {
			t.Fatal(err)
		}
		if err := ds.Mark(t.Context(), b, id); err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(t.Context(), storage.Sync); err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	for _, name := range []string{indextest.Signal, indextest.Cave} {
		s := newSpec(f, "1", nil)
		s.Name = name
		if _, err := e.Opener(string(indextest.Module)).Open(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if err := e.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestAnIndexIsNotVisibleWhileItOpens is the deterministic form of the race
// above: while idx.open runs, the reconciler's view (snapshot), lookups and
// Stats do not include the index, and a second Open is refused.
func TestAnIndexIsNotVisibleWhileItOpens(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	e := engineFor(t, f, t.TempDir())
	var seen bool
	e.duringOpen = func(*idx) {
		seen = true
		if _, err := e.lookup(string(indextest.Module), indextest.Signal); !errors.Is(err, ErrNotOpen) {
			t.Errorf("lookup while opening: %v, want ErrNotOpen", err)
		}
		if n := len(e.snapshot()); n != 0 {
			t.Errorf("the reconciler's snapshot holds %d indexes while one is opening", n)
		}
		if _, err := e.Opener(string(indextest.Module)).Open(t.Context(), newSpec(f, "1", nil)); !errors.Is(err, ErrAlreadyOpen) {
			t.Errorf("second open while opening: %v, want ErrAlreadyOpen", err)
		}
	}
	if _, err := e.Opener(string(indextest.Module)).Open(t.Context(), newSpec(f, "1", nil)); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("the open seam never ran")
	}
	if _, err := e.lookup(string(indextest.Module), indextest.Signal); err != nil {
		t.Fatalf("the opened index is not published: %v", err)
	}
}

// TestSecondOpenOfAnIndexBeingOpenedIsRefused: the duplicate guard covers the
// window in which the index is opening and not yet published.
func TestSecondOpenOfAnIndexBeingOpenedIsRefused(t *testing.T) {
	r := seeded(t)
	if _, err := r.e.Opener(string(indextest.Module)).Open(t.Context(), r.spec); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second open: %v", err)
	}
	r.e.mu.Lock()
	r.e.opening["gotham/cave"] = struct{}{}
	r.e.mu.Unlock()
	s := newSpec(r.f, "1", nil)
	s.Name = indextest.Cave
	if _, err := r.e.Opener(string(indextest.Module)).Open(t.Context(), s); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("open of an index being opened: %v", err)
	}
}

// ---- Degraded is repairable ----

// TestInterruptedCreateIsRecreatedNotDegraded: a crash inside the create (or
// inside a rebuild's recreate) leaves the rebuilding marker and a directory
// that does not open. The marker says nothing in it is worth keeping, so the
// next open recreates it instead of degrading for good.
func TestInterruptedCreateIsRecreatedNotDegraded(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	dir := t.TempDir()
	path := indexDir(dir)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mappingMarkerPath(path), []byte(mappingStamp(newSpec(f, "1", nil))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rebuildingMarkerPath(path), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := rigOn(t, f, dir, "1")
	if got := r.ix.State(); got != index.Rebuilding {
		t.Fatalf("state after an interrupted create = %v, want Rebuilding", got)
	}
	r.save("joker", "The Joker", "clown", 1940)
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	if r.ix.State() != index.Ready || !slices.Equal(r.ids(index.Match("name", "joker")), []string{"joker"}) {
		t.Fatalf("after the rebuild: state %v", r.ix.State())
	}
}

// TestDegradedIndexIsRepairedByTheRebuildTask: a complete index that will not
// open is left alone at boot (it may be repairable by hand), but it is listed
// for a rebuild and the rebuild task recreates it.
func TestDegradedIndexIsRepairedByTheRebuildTask(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	dir := t.TempDir()
	path := indexDir(dir)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "index_meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mappingMarkerPath(path), []byte(mappingStamp(newSpec(f, "1", nil))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := rigOn(t, f, dir, "1")
	if r.ix.State() != index.Degraded {
		t.Fatalf("setup: state %v", r.ix.State())
	}
	if got := r.e.PendingRebuilds(); len(got) != 1 || got[0] != (Ref{string(indextest.Module), indextest.Signal}) {
		t.Fatalf("PendingRebuilds does not list the Degraded index: %v", got)
	}
	r.save("joker", "The Joker", "clown", 1940)
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatalf("rebuild of a Degraded index: %v", err)
	}
	if r.ix.State() != index.Ready {
		t.Fatalf("state after the repair = %v, want Ready", r.ix.State())
	}
	if got := r.ids(index.Match("name", "joker")); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("the repaired index: %v", got)
	}
	if got := r.e.PendingRebuilds(); len(got) != 0 {
		t.Fatalf("PendingRebuilds after the repair: %v", got)
	}
}

// ---- markers ----

// TestMarkersAreWrittenWholeAndLeaveNoTempFiles: the markers go through
// storage.WriteFileDurable (whose crash behaviour storage tests), and the mapping
// marker carries the version and the field fingerprint.
func TestMarkersAreWrittenWholeAndLeaveNoTempFiles(t *testing.T) {
	r := newRig(t)
	path := indexDir(r.dir)
	if got, want := readMarker(mappingMarkerPath(path)), mappingStamp(r.spec); got != want {
		t.Fatalf("mapping marker = %q, want %q", got, want)
	}
	if got := readMarker(rebuildingMarkerPath(path)); got != "1" {
		t.Fatalf("rebuilding marker = %q", got)
	}
	left, err := filepath.Glob(filepath.Join(r.dir, string(indextest.Module), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

// TestFieldChangeWithoutAVersionBumpRecreatesTheIndex: the marker holds a
// fingerprint of the field declarations as well as the version.
func TestFieldChangeWithoutAVersionBumpRecreatesTheIndex(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	dir := t.TempDir()
	r := rigOn(t, f, dir, "1")
	r.save("joker", "The Joker", "clown", 1940)
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	if err := r.ix.Close(); err != nil {
		t.Fatal(err)
	}
	e2 := engineFor(t, f, dir)
	spec := newSpec(f, "1", nil) // the same MappingVersion
	spec.Fields = append(spec.Fields, index.Field{Name: "extra", Kind: index.Keyword})
	ix, err := e2.Opener(string(indextest.Module)).Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if ix.State() != index.Rebuilding || !e2.mustIdx(t, indextest.Signal).recreatedForMapping {
		t.Fatalf("a Fields change under an unchanged version kept the stale index: state %v", ix.State())
	}
}

// ---- query cost ----

// TestSearchBoundsTermExpansion: a prefix (or wildcard, or fuzzy term) expands to
// every matching term; bleve's term-searcher cap is set on each call, so an
// expansion past it fails fast on Search, Count and Facets alike.
func TestSearchBoundsTermExpansion(t *testing.T) {
	r := seeded(t, func(o *Options) { o.MaxTermSearchers = 2 })
	wide := index.Prefix("name", "")
	_, err := r.ix.Search(t.Context(), wide, index.Page{})
	if err == nil || !strings.Contains(err.Error(), "TooManyTermSearchers") {
		t.Fatalf("Search: %v, want a TooManyTermSearchers error", err)
	}
	if _, err := r.ix.Count(t.Context(), wide); err == nil || !strings.Contains(err.Error(), "TooManyTermSearchers") {
		t.Fatalf("Count: %v", err)
	}
	if _, err := r.ix.Facets(t.Context(), wide, []string{"alias"}, 5); err == nil || !strings.Contains(err.Error(), "TooManyTermSearchers") {
		t.Fatalf("Facets: %v", err)
	}
	if n := r.count(index.Term("alias", "clown")); n != 1 {
		t.Fatalf("a query inside the cap: %d hits, want 1", n)
	}
}

// TestSearchHasADefaultDeadline: a caller with no deadline gets the engine's.
func TestSearchHasADefaultDeadline(t *testing.T) {
	r := seeded(t, func(o *Options) { o.SearchTimeout = time.Nanosecond })
	if _, err := r.ix.Search(context.Background(), index.Match("name", "joker"), index.Page{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Search without a deadline: %v, want the default deadline", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := r.ix.Search(ctx, index.Match("name", "joker"), index.Page{}); err != nil {
		t.Fatalf("a caller's own deadline is kept: %v", err)
	}
}

func TestQueryStringIsBounded(t *testing.T) {
	r := seeded(t)
	for name, qs := range map[string]string{
		"regular expression": `name:/jo.*/`,
		"leading star":       `*oker`,
		"leading question":   `?oker`,
		"field leading star": `name:*oker`,
		"inside a group":     `name:joker AND (alias:clown OR name:*ane)`,
		"over 4 KB":          strings.Repeat("joker ", 800),
	} {
		if _, err := r.ix.Search(t.Context(), index.QueryString(qs), index.Page{}); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
	for name, qs := range map[string]string{
		"trailing wildcard": `name:jok*`,
		"boolean":           `name:joker AND alias:clown`,
		"negation":          `name:joker -alias:bird`,
	} {
		if _, err := r.ix.Search(t.Context(), index.QueryString(qs), index.Page{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestQueryShapeIsBounded(t *testing.T) {
	r := seeded(t)
	many := make([]index.Query, maxQueryNodes+1)
	for i := range many {
		many[i] = index.Term("alias", "clown")
	}
	deep := index.Query(index.Term("alias", "clown"))
	for range maxQueryDepth + 8 {
		deep = index.Not(deep)
	}
	ok := index.Query(index.Term("alias", "clown"))
	for range maxQueryDepth - 4 {
		ok = index.Not(ok)
	}
	for name, q := range map[string]index.Query{
		"too many nodes": index.Or(many...),
		"too deep":       deep,
		"long text":      index.Match("name", strings.Repeat("x", maxQueryStringBytes+1)),
	} {
		if _, err := r.ix.Search(t.Context(), q, index.Page{}); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
	if _, err := r.ix.Search(t.Context(), ok, index.Page{}); err != nil {
		t.Errorf("a query inside the limits: %v", err)
	}
}

func TestSortKeysMustNameFields(t *testing.T) {
	r := seeded(t)
	for _, bad := range []string{"nonexistent", "-nonexistent"} {
		if _, err := r.ix.Search(t.Context(), index.Prefix("alias", ""), index.Page{SortBy: []string{bad}}); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("sort %q: %v, want ErrInvalidQuery", bad, err)
		}
	}
	for _, good := range []string{"year", "-year", "_score", "-_score", "_id", "-_id"} {
		if _, err := r.ix.Search(t.Context(), index.Prefix("alias", ""), index.Page{SortBy: []string{good}}); err != nil {
			t.Errorf("sort %q: %v", good, err)
		}
	}
}

// ---- reconciler resilience ----

// poisonIndex refuses any batch that carries the id "scarecrow".
type poisonIndex struct{ bleveIndex }

func (p poisonIndex) Batch(b *bleve.Batch) error {
	if strings.Contains(b.String(), "'scarecrow'") { // Batch.String lists each id as 'id'
		return errors.New("the index refuses this document")
	}
	return p.bleveIndex.Batch(b)
}

// TestReconcileIsolatesAPoisonMark: a document the index refuses stays pending
// and counted; the rest of its chunk is applied and cleared, and the pass
// reports no error.
func TestReconcileIsolatesAPoisonMark(t *testing.T) {
	r := seeded(t)
	i := r.e.mustIdx(t, indextest.Signal)
	i.mu.Lock()
	real := i.bi
	i.bi = poisonIndex{real}
	i.mu.Unlock()
	t.Cleanup(func() {
		i.mu.Lock()
		i.bi = real
		i.mu.Unlock()
	})
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.save("harley", "Harley Quinn", "clown", 1992)
	r.save("ivy", "Poison Ivy", "plant", 1966)
	r.reconcile() // fails the test on any error
	if got := r.ids(index.Match("name", "harley")); !slices.Equal(got, []string{"harley"}) {
		t.Fatalf("a poison neighbour kept a good document out: %v", got)
	}
	if got := r.ids(index.Match("name", "ivy")); !slices.Equal(got, []string{"ivy"}) {
		t.Fatalf("ivy: %v", got)
	}
	if n := r.dirtyCount(); n != 1 {
		t.Fatalf("pending = %d, want only the poison mark", n)
	}
	if i.failures.Load() != 1 {
		t.Fatalf("failures = %d, want 1", i.failures.Load())
	}
}

// TestReconcileYieldsAfterItsBudget: an index with a large backlog stops after
// the pass budget (having applied at least one page) so the next index is
// served, and the loop takes another pass at once.
func TestReconcileYieldsAfterItsBudget(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	e := engineFor(t, f, t.TempDir(), func(o *Options) { o.PassBudget = time.Nanosecond })
	signal := newSpec(f, "1", nil)
	cave := newSpec(f, "1", nil)
	cave.Name = indextest.Cave
	for _, s := range []index.Spec{signal, cave} {
		if _, err := e.Opener(string(indextest.Module)).Open(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	mark := func(name string, n int) {
		ds := f.DirtySet(name)
		for k := range n {
			id := name + "-" + string(rune('a'+k/26)) + string(rune('a'+k%26))
			b := f.Module.NewBatch()
			if err := ds.Mark(t.Context(), b, id); err != nil {
				t.Fatal(err)
			}
			if err := b.Commit(t.Context(), storage.NoSync); err != nil {
				t.Fatal(err)
			}
			_ = b.Close()
		}
	}
	mark(indextest.Signal, pageSize+10) // more than one page: the budget applies
	mark(indextest.Cave, 3)
	ctx, cancel := context.WithTimeout(t.Context(), testWatchdog)
	defer cancel()
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.yielded.Load() {
		t.Fatal("a pass that left a backlog behind did not record that it yielded")
	}
	if n, _ := f.DirtySet(indextest.Cave).Count(ctx); n != 0 {
		t.Fatalf("the second index starved behind the first one's backlog: %d marks pending", n)
	}
	if n, _ := f.DirtySet(indextest.Signal).Count(ctx); n == 0 || n >= pageSize+10 {
		t.Fatalf("the first index should have applied one page and stopped: %d of %d pending", n, pageSize+10)
	}
	for range 3 { // later passes finish the backlog
		if err := e.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := f.DirtySet(indextest.Signal).Count(ctx); n != 0 {
		t.Fatalf("the backlog never drained: %d", n)
	}
}

// ---- panicking module callbacks ----

func TestPanickingDocumentCallbackIsAFailedLoad(t *testing.T) {
	r := seeded(t)
	i := r.e.mustIdx(t, indextest.Signal)
	inner := i.spec.Document
	var mu sync.Mutex
	boom := true
	i.spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if id == "scarecrow" && boom {
			panic("the fear toxin leaked")
		}
		return inner(ctx, id)
	}
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.save("harley", "Harley Quinn", "clown", 1992)
	r.reconcile() // must not panic
	if got := r.ids(index.Match("name", "harley")); !slices.Equal(got, []string{"harley"}) {
		t.Fatalf("a panicking neighbour starved the page: %v", got)
	}
	if n := r.dirtyCount(); n != 1 || i.failures.Load() != 1 {
		t.Fatalf("pending %d failures %d, want the one failed mark", n, i.failures.Load())
	}
	mu.Lock()
	boom = false
	mu.Unlock()
	r.reconcile()
	if got := r.ids(index.Match("name", "scarecrow")); !slices.Equal(got, []string{"scarecrow"}) {
		t.Fatalf("the retry: %v", got)
	}
}

func TestPanickingRebuildCallbackFailsTheRebuild(t *testing.T) {
	r := seeded(t)
	r.e.mustIdx(t, indextest.Signal).spec.Rebuild = func(context.Context, index.RebuildWriter) error {
		panic("the Batcomputer is down")
	}
	_, err := runRebuild(t, r.e)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("rebuild with a panicking callback: %v", err)
	}
}
