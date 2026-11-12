// file: core/internal/bleveindex/reconcile_test.go
// version: 1.0.2
// guid: b19af4f8-02b3-4c02-b3db-ef84ad3df329
// last-edited: 2026-11-11

package bleveindex

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/index/scorch"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// TestIndexReconcilerDrainsAfterCrash is G4: a mark committed with its entity
// write survives a crash that loses the index's own in-memory progress, and the
// reconciler of the next boot indexes the entity.
func TestIndexReconcilerDrainsAfterCrash(t *testing.T) {
	mem := vfs.NewCrashableMem()
	f := indextest.Open(t, storage.FSOf(mem), dbPath)
	dir := t.TempDir()
	r := rigOn(t, f, dir, "1")
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	r.save("joker", "The Joker", "clown", 1940)
	r.reconcile()
	if got := r.ids(index.Match("name", "joker")); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("setup: %v", got)
	}

	// The next write commits its entity and its mark, and the process dies
	// before the reconciler sees it; a batch that never committed leaves nothing.
	r.save("bane", "Bane", "luchador", 1993)
	lost := r.f.Module.NewBatch()
	if err := lost.Set([]byte("villain/scarecrow"), villainRow("Scarecrow", "fear", 1941, false)); err != nil {
		t.Fatal(err)
	}
	if err := r.ix.MarkDirty(lost, "scarecrow"); err != nil {
		t.Fatal(err)
	}
	clone := mem.CrashClone(vfs.CrashCloneCfg{})
	_ = lost.Close()
	if got := r.ids(index.Match("name", "bane")); len(got) != 0 {
		t.Fatalf("bane was indexed before the crash: %v", got)
	}
	if err := r.ix.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Close(); err != nil {
		t.Fatal(err)
	}

	// The next boot: the store as the crash left it, the same index directory.
	f2 := indextest.Open(t, storage.FSOf(clone), dbPath)
	r2 := rigOn(t, f2, dir, "1")
	if r2.ix.State() != index.Ready {
		t.Fatalf("the index state after the crash: %v", r2.ix.State())
	}
	if n := r2.dirtyCount(); n != 1 {
		t.Fatalf("the committed mark did not survive the crash: %d pending", n)
	}
	if got := r2.ids(index.Match("name", "bane")); len(got) != 0 {
		t.Fatalf("bane is in the index without a reconcile: %v", got)
	}
	r2.reconcile()
	if got := r2.ids(index.Match("name", "bane")); !slices.Equal(got, []string{"bane"}) {
		t.Fatalf("the reconciler did not index the entity whose mark survived: %v", got)
	}
	if got := r2.ids(index.Match("name", "scarecrow")); len(got) != 0 {
		t.Fatalf("an uncommitted write reached the index: %v", got)
	}
	if n := r2.dirtyCount(); n != 0 {
		t.Fatalf("pending after the drain: %d", n)
	}
}

// TestReconcileAppliesAddsUpdatesAndDeletes: a mark of a changed entity
// re-indexes it, a mark of a removed entity deletes the document.
func TestReconcileAppliesAddsUpdatesAndDeletes(t *testing.T) {
	r := seeded(t)
	r.save("joker", "The Joker Reborn", "jester", 1940)
	r.remove("penguin")
	r.reconcile()
	if got := r.ids(index.Term("alias", "clown")); len(got) != 0 {
		t.Fatalf("the old alias survived an update: %v", got)
	}
	if got := r.ids(index.Match("name", "reborn")); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("update: %v", got)
	}
	if got := r.ids(index.Match("name", "penguin")); len(got) != 0 {
		t.Fatalf("delete: %v", got)
	}
	if n := r.dirtyCount(); n != 0 {
		t.Fatalf("pending: %d", n)
	}
	// Removing an id that was never indexed is a no-op, not a failure.
	r.remove("ghost")
	r.reconcile()
}

// TestReconcileKeepsAMarkWrittenAfterItWasRead: the lost-update guard. The
// entity changes (and re-marks) while the reconciler is between loading its
// document and clearing its mark; the mark must survive and the next pass must
// index the newer state.
func TestReconcileKeepsAMarkWrittenAfterItWasRead(t *testing.T) {
	r := seeded(t)
	var once sync.Once
	inner := r.spec.Document
	r.e.mustIdx(t, indextest.Signal).spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		doc, found, err := inner(ctx, id)
		if id == "joker" {
			once.Do(func() { r.saveRow("joker", villainRow("The Joker Returns", "clown", 1940, false)) })
		}
		return doc, found, err
	}
	r.save("joker", "The Joker Reborn", "clown", 1940)
	r.reconcile()
	if n := r.dirtyCount(); n != 1 {
		t.Fatalf("the re-mark was cleared along with the stale one: %d pending, want 1", n)
	}
	if got := r.ids(index.Match("name", "returns")); len(got) != 0 {
		t.Fatalf("the first pass cannot have seen the newer write: %v", got)
	}
	r.reconcile()
	if got := r.ids(index.Match("name", "returns")); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("the newer state never reached the index: %v", got)
	}
	if n := r.dirtyCount(); n != 0 {
		t.Fatalf("pending: %d", n)
	}
}

// TestReconcileKeepsMarksWhenTheIndexWriteFails: the marks are cleared only
// after the batch was applied. A write that fails must leave every mark
// pending, or the documents would never be indexed.
func TestReconcileKeepsMarksWhenTheIndexWriteFails(t *testing.T) {
	r := seeded(t)
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.save("harley", "Harley Quinn", "clown", 1992)
	i := r.e.mustIdx(t, indextest.Signal)
	i.mu.Lock()
	broken := i.bi
	i.bi = failingIndex{bleveIndex: broken}
	i.mu.Unlock()
	t.Cleanup(func() {
		i.mu.Lock()
		i.bi = broken
		i.mu.Unlock()
	})
	if err := r.e.Reconcile(t.Context()); err == nil {
		t.Fatal("a failed index write was not reported")
	}
	if n := r.dirtyCount(); n != 2 {
		t.Fatalf("pending = %d after a failed write, want both marks kept", n)
	}
}

// bleveIndex names bleve.Index so a struct can embed it: the embedded field
// would otherwise be called Index and collide with the interface's Index method.
type bleveIndex = bleve.Index

// failingIndex is an index whose batches fail.
type failingIndex struct{ bleveIndex }

func (failingIndex) Batch(*bleve.Batch) error { return errors.New("the Batcave is flooded") }

// TestReconcileLeavesAFailedDocumentPending: a document that cannot load keeps
// its mark, the rest of the page is applied, and a later pass retries it.
func TestReconcileLeavesAFailedDocumentPending(t *testing.T) {
	r := seeded(t)
	clk := r.useClock()
	broken := true
	var mu sync.Mutex
	i := r.e.mustIdx(t, indextest.Signal)
	inner := i.spec.Document
	i.spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if id == "scarecrow" && broken {
			return nil, false, errors.New("the Arkham records are sealed")
		}
		return inner(ctx, id)
	}
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.save("harley", "Harley Quinn", "clown", 1992)
	r.reconcile()
	if got := r.ids(index.Match("name", "harley")); !slices.Equal(got, []string{"harley"}) {
		t.Fatalf("a failing neighbour starved the page: %v", got)
	}
	if n := r.dirtyCount(); n != 1 {
		t.Fatalf("pending = %d, want the one failed mark", n)
	}
	if i.failures.Load() != 1 {
		t.Fatalf("failures = %d", i.failures.Load())
	}
	mu.Lock()
	broken = false
	mu.Unlock()
	clk.Advance(failBackoffBase + time.Second) // the failed mark is retried once its backoff is over
	r.reconcile()
	if got := r.ids(index.Match("name", "scarecrow")); !slices.Equal(got, []string{"scarecrow"}) {
		t.Fatalf("the retry: %v", got)
	}
}

// TestReconcileWalksPastFailingMarks: more failing marks than one page do not
// stop the pass reaching the marks behind them.
func TestReconcileWalksPastFailingMarks(t *testing.T) {
	r := seeded(t)
	i := r.e.mustIdx(t, indextest.Signal)
	inner := i.spec.Document
	i.spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		if strings.HasPrefix(id, "a-") {
			return nil, false, errors.New("sealed")
		}
		return inner(ctx, id)
	}
	for n := range pageSize + 5 {
		r.saveRow(fmt.Sprintf("a-%05d", n), villainRow("Sealed", "x", 1940, false))
	}
	r.save("zatanna", "Zatanna", "magic", 1964)
	r.reconcile()
	if got := r.ids(index.Match("name", "zatanna")); !slices.Equal(got, []string{"zatanna"}) {
		t.Fatalf("a page of failing marks hid the one behind it: %v", got)
	}
}

// TestReconcileUsesABoundedPoolAndNeverDoublesAnID: a large dirty set drains
// under several workers with every document indexed exactly as its entity says,
// while writers keep marking.
func TestReconcileUsesABoundedPoolAndNeverDoublesAnID(t *testing.T) {
	r := seeded(t)
	var mu sync.Mutex
	inflight, peak, perID := 0, 0, map[string]int{}
	i := r.e.mustIdx(t, indextest.Signal)
	inner := i.spec.Document
	i.spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		perID[id]++
		mu.Unlock()
		time.Sleep(200 * time.Microsecond)
		defer func() { mu.Lock(); inflight--; mu.Unlock() }()
		return inner(ctx, id)
	}
	const n = 600
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for k := w; k < n; k += 4 {
				r.saveRow(fmt.Sprintf("goon-%04d", k), villainRow(fmt.Sprintf("Goon %04d", k), "henchman", 1950+k%40, false))
			}
		})
	}
	wg.Wait()
	r.reconcile()
	if got := r.count(index.Term("alias", "henchman")); got != n {
		t.Fatalf("indexed %d of %d", got, n)
	}
	if peak < 2 || peak > 4 {
		t.Fatalf("peak concurrency %d: want a pool of 2..4 (core.index.reconcile.workers = 4)", peak)
	}
	for id, c := range perID {
		if strings.HasPrefix(id, "goon-") && c != 1 {
			t.Fatalf("%s was loaded %d times in one pass", id, c)
		}
	}
	if r.dirtyCount() != 0 {
		t.Fatalf("pending: %d", r.dirtyCount())
	}
}

// TestReconcilerComponentWakesOnNudgeAndInterval: Run drains without being
// called, on a MarkDirty nudge and, when the nudge is missed, on the interval.
func TestReconcilerComponentWakesOnNudgeAndInterval(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	e := engineFor(t, f, t.TempDir(), func(o *Options) { o.Interval = func() time.Duration { return 50 * time.Millisecond } })
	r := &rig{t: t, f: f, e: e, dir: e.o.Dir}
	r.spec = newSpec(f, "1", nil)
	r.open()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := e.Stop(context.Background()); err != nil {
			t.Error(err)
		}
		<-done
	})

	r.save("joker", "The Joker", "clown", 1940)
	waitFor(t, "the nudge drain", func() bool { return len(r.ids(index.Match("name", "joker"))) == 1 })

	// A mark committed with no MarkDirty nudge (written through the dirty set
	// directly) is found by the interval.
	b := f.Module.NewBatch()
	if err := b.Set([]byte("villain/bane"), villainRow("Bane", "luchador", 1993, false)); err != nil {
		t.Fatal(err)
	}
	if err := e.mustIdx(t, indextest.Signal).dirty.Mark(t.Context(), b, "bane"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the interval drain", func() bool { return len(r.ids(index.Match("name", "bane"))) == 1 })

	if err := e.Run(t.Context()); err == nil {
		t.Fatal("a second Run")
	}
}

func TestEngineStopBeforeRunAndRunAfterStop(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	e := engineFor(t, f, t.TempDir())
	if err := e.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if err := e.Run(t.Context()); err == nil {
		t.Fatal("Run after Stop started")
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- the rebuild task ----

func TestRebuildTaskConformsAndDeclaresItself(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	e := engineFor(t, f, t.TempDir())
	taskstest.Conformance(t, e.RebuildTask())
}

// TestRebuildFillsTheIndexFromParallelPuts: a rebuild whose callback scans in
// parallel fills the index, leaves Rebuilding and reports its count.
func TestRebuildFillsTheIndexFromParallelPuts(t *testing.T) {
	r := newRig(t)
	const n = 2000
	for k := range n {
		if err := r.f.Module.Set(t.Context(), fmt.Appendf(nil, "villain/rogue-%05d", k), villainRow(fmt.Sprintf("Rogue %d", k), "rogue", 1940+k%60, false)); err != nil {
			t.Fatal(err)
		}
	}
	r.e.mustIdx(t, indextest.Signal).spec.Rebuild = func(ctx context.Context, w index.RebuildWriter) error {
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for s := range 4 {
			wg.Go(func() {
				for k := s; k < n; k += 4 {
					id := fmt.Sprintf("rogue-%05d", k)
					if err := w.Put(id, index.Doc{"name": fmt.Sprintf("Rogue %d", k), "alias": "rogue", "year": float64(1940 + k%60)}); err != nil {
						errs <- err
						return
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		return <-errs
	}
	res, err := runRebuild(t, r.e)
	if err != nil {
		t.Fatal(err)
	}
	if res.Documents != n {
		t.Fatalf("reported %d documents, want %d", res.Documents, n)
	}
	if got := r.count(index.Term("alias", "rogue")); got != n {
		t.Fatalf("indexed %d of %d", got, n)
	}
	if r.ix.State() != index.Ready {
		t.Fatalf("state after rebuild: %v", r.ix.State())
	}
}

// TestRebuildDropsDocumentsTheSourceNoLongerHas and the marks written during a
// rebuild are applied after it.
func TestRebuildDropsGhostsAndAppliesMarksWrittenDuringIt(t *testing.T) {
	r := seeded(t)
	// A ghost: in the index, gone from the source, with no mark to say so.
	if err := r.f.Module.Delete(t.Context(), []byte("villain/penguin")); err != nil {
		t.Fatal(err)
	}
	if got := r.ids(index.Match("name", "penguin")); len(got) != 1 {
		t.Fatalf("setup: %v", got)
	}
	i := r.e.mustIdx(t, indextest.Signal)
	inner := i.spec.Rebuild
	i.spec.Rebuild = func(ctx context.Context, w index.RebuildWriter) error {
		if err := inner(ctx, w); err != nil {
			return err
		}
		// Written after the scan read it, marked in the same batch.
		r.saveRow("joker", villainRow("The Joker Wild", "clown", 1940, false))
		return nil
	}
	if _, err := runRebuild(t, r.e); err != nil {
		t.Fatal(err)
	}
	if got := r.ids(index.Match("name", "penguin")); len(got) != 0 {
		t.Fatalf("the rebuild kept a document the source no longer has: %v", got)
	}
	if got := r.ids(index.Match("name", "wild")); !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("a mark written during the rebuild was lost: %v", got)
	}
	if r.ix.State() != index.Ready || r.dirtyCount() != 0 {
		t.Fatalf("state %v, pending %d", r.ix.State(), r.dirtyCount())
	}
}

// TestRebuildFailureLeavesTheIndexRebuilding: a failed callback fails the run,
// keeps the marker so a restart still knows, and the index asks again.
func TestRebuildFailureLeavesTheIndexRebuilding(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	dir := t.TempDir()
	r := rigOn(t, f, dir, "1")
	r.save("joker", "The Joker", "clown", 1940)
	i := r.e.mustIdx(t, indextest.Signal)
	i.spec.Rebuild = func(ctx context.Context, w index.RebuildWriter) error {
		_ = w.Put("joker", index.Doc{"name": "The Joker"})
		return errors.New("the Batcomputer is offline")
	}
	if _, err := runRebuild(t, r.e); err == nil || !strings.Contains(err.Error(), "Batcomputer") {
		t.Fatalf("rebuild: %v", err)
	}
	if r.ix.State() != index.Rebuilding || len(r.e.PendingRebuilds()) != 1 {
		t.Fatalf("state %v, pending rebuilds %v", r.ix.State(), r.e.PendingRebuilds())
	}
	// A reconcile pass still works on a Rebuilding index whose rebuild failed.
	r.reconcile()
	if r.ix.State() != index.Rebuilding {
		t.Fatalf("a failed rebuild left Rebuilding: %v", r.ix.State())
	}
	if err := r.ix.Close(); err != nil {
		t.Fatal(err)
	}
	again := rigOn(t, f, dir, "1")
	if again.ix.State() != index.Rebuilding {
		t.Fatalf("the restart forgot the unfinished rebuild: %v", again.ix.State())
	}
}

// TestRebuildStopsWhenCancelled: a cancelled run returns instead of hanging on
// a producer that blocks, and the index stays Rebuilding.
func TestRebuildStopsWhenCancelled(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(t.Context())
	i := r.e.mustIdx(t, indextest.Signal)
	started := make(chan struct{})
	i.spec.Rebuild = func(ctx context.Context, w index.RebuildWriter) error {
		close(started)
		for k := 0; ; k++ {
			if err := w.Put(fmt.Sprintf("endless-%d", k), index.Doc{"name": "Endless"}); err != nil {
				return err
			}
		}
	}
	go func() { <-started; time.Sleep(50 * time.Millisecond); cancel() }()
	done := make(chan error, 1)
	go func() {
		_, err := r.e.runRebuild(taskstest.NewFakeRun(ctx, RebuildKind).RC(), RebuildParams{Module: string(indextest.Module), Index: indextest.Signal})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled rebuild reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the rebuild did not stop")
	}
	if r.ix.State() != index.Rebuilding || r.e.mustIdx(t, indextest.Signal).rebuilt.Load() {
		t.Fatalf("a cancelled rebuild marked the index complete")
	}
}

func TestRebuildRefusals(t *testing.T) {
	r := newRig(t)
	for name, p := range map[string]RebuildParams{
		"no module":    {Index: "signal"},
		"no index":     {Module: "gotham"},
		"not open":     {Module: "gotham", Index: "cave"},
		"wrong module": {Module: "metropolis", Index: "signal"},
	} {
		if _, err := r.e.runRebuild(taskstest.NewFakeRun(t.Context(), RebuildKind).RC(), p); err == nil {
			t.Errorf("%s: rebuilt", name)
		}
	}
	w := &rebuildWriter{ctx: t.Context(), ch: make(chan []entry, 1)}
	if err := w.Put("", index.Doc{}); err == nil {
		t.Error("Put of an empty id")
	}
	if err := w.Put("x", nil); err == nil {
		t.Error("Put of a nil document")
	}
}

// TestReconcileSkipsAnIndexWhileItIsRebuilding: a pass that arrives mid-rebuild
// applies nothing, because it would race the drop and refill (the writer lock).
func TestReconcileSkipsAnIndexWhileItIsRebuilding(t *testing.T) {
	r := seeded(t)
	i := r.e.mustIdx(t, indextest.Signal)
	inRebuild, release := make(chan struct{}), make(chan struct{})
	inner := i.spec.Rebuild
	i.spec.Rebuild = func(ctx context.Context, w index.RebuildWriter) error {
		close(inRebuild)
		<-release
		return inner(ctx, w)
	}
	done := make(chan error, 1)
	go func() { _, err := runRebuild(t, r.e); done <- err }()
	<-inRebuild
	r.saveRow("bane", villainRow("Bane", "luchador", 1993, false))
	before := r.loads.Load()
	r.reconcile()
	if got := r.loads.Load(); got != before {
		t.Fatalf("a reconcile pass loaded %d documents while the index was being rebuilt", got-before)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.ids(index.Match("name", "bane")); !slices.Equal(got, []string{"bane"}) {
		t.Fatalf("the mark written during the rebuild: %v", got)
	}
}

// ---- backups ----

// TestCopyToStampsTheGenerationReadBeforeTheCopy: the copy is a working index,
// stamped with the generation of before the copy, so a touch afterwards makes
// the artifact stale.
func TestCopyToStampsTheGenerationReadBeforeTheCopy(t *testing.T) {
	r := seeded(t)
	dir := t.TempDir() + "/copy"
	if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir); err != nil {
		t.Fatal(err)
	}
	der, err := r.f.DB.Derived(indextest.Module, indextest.Signal)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := der.Current(t.Context())
	if ok, err := storage.ArtifactAccepted(dir, cur); err != nil || !ok {
		t.Fatalf("the fresh copy: %v %v", ok, err)
	}
	r.save("joker", "The Joker Reborn", "jester", 1940) // MarkDirty touches the generation
	cur, _ = der.Current(t.Context())
	if ok, _ := storage.ArtifactAccepted(dir, cur); ok {
		t.Fatal("a copy taken before a write was accepted after it")
	}
	if err := r.e.CopyTo(t.Context(), "gotham", "nope", t.TempDir()); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("copy of an unopened index: %v", err)
	}
}

// TestCopyToRejectsAnArtifactWhenAWriteCommitsDuringTheCopy: the generation is
// read before the copy, so a write that commits after that read (here, at the
// seam right behind it) leaves the artifact stale. Reading it after the copy
// would stamp the newer generation on a copy that lacks the write.
func TestCopyToRejectsAnArtifactWhenAWriteCommitsDuringTheCopy(t *testing.T) {
	r := seeded(t)
	r.e.mustIdx(t, indextest.Signal).afterQuiescenceCheck = func() {
		r.save("riddler", "The Riddler Unmasked", "puzzle", 1948)
	}
	dir := t.TempDir() + "/copy"
	if err := r.e.CopyTo(t.Context(), string(indextest.Module), indextest.Signal, dir); err != nil {
		t.Fatal(err)
	}
	der, err := r.f.DB.Derived(indextest.Module, indextest.Signal)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := der.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := storage.ArtifactAccepted(dir, cur); err != nil || ok {
		t.Fatalf("an artifact that missed a committed write was accepted: ok=%v err=%v", ok, err)
	}
}

// ---- the scorch callback ----

func TestScorchCallbackIsRegisteredOncePerIndexUnderAUniqueName(t *testing.T) {
	f := indextest.Open(t, storage.FSOf(vfs.NewMem()), dbPath)
	a := rigOn(t, f, t.TempDir(), "1")
	b := rigOn(t, f, t.TempDir(), "1")
	ia, ib := a.e.mustIdx(t, indextest.Signal), b.e.mustIdx(t, indextest.Signal)
	if ia.cb.name == "" || ia.cb.name == ib.cb.name {
		t.Fatalf("names %q and %q must be set and distinct", ia.cb.name, ib.cb.name)
	}
	registered := func(name string) bool {
		scorchRegistryMu.Lock()
		defer scorchRegistryMu.Unlock()
		_, ok := scorch.RegistryAsyncErrorCallbacks[name]
		return ok
	}
	if !registered(ia.cb.name) || !registered(ib.cb.name) {
		t.Fatal("an open index has no callback in scorch's registry")
	}
	name := ia.cb.name
	if cfg := ia.cb.register("gotham", "signal"); cfg["asyncErrorCallbackName"] != name {
		t.Fatalf("a second register changed the name: %v", cfg)
	}
	// The callback counts and logs a failure.
	var logged int
	ia.cb.log = func(error, string, int64) { logged++ }
	scorchRegistryMu.Lock()
	cb := scorch.RegistryAsyncErrorCallbacks[name]
	scorchRegistryMu.Unlock()
	cb(errors.New("merge plan failed"), "/batcave")
	cb(errors.New("merge plan failed"), "/batcave")
	if got := ia.cb.errors.Load(); got != 2 || logged != 1 {
		t.Fatalf("errors %d, logged %d: want 2 counted and the second rate-limited", got, logged)
	}
	if err := a.ix.Close(); err != nil {
		t.Fatal(err)
	}
	if registered(name) {
		t.Fatal("Close left the callback in scorch's registry")
	}
	if !registered(ib.cb.name) {
		t.Fatal("closing one index removed another's callback")
	}
	if got := a.e.Stats(t.Context()); len(got) != 0 {
		t.Fatalf("Stats of an engine with no open index: %v", got)
	}
	st := b.e.Stats(t.Context())
	if len(st) != 1 || st[0].Module != "gotham" || st[0].Index != "signal" || st[0].State != index.Rebuilding {
		t.Fatalf("Stats: %+v", st)
	}
}

func TestStatsReportDocsDirtyAndLag(t *testing.T) {
	r := seeded(t)
	st := r.e.Stats(t.Context())[0]
	if st.Docs != 5 || st.Dirty != 0 || st.ReconcileLag != 0 || st.State != index.Ready {
		t.Fatalf("settled: %+v", st)
	}
	r.save("harley", "Harley Quinn", "clown", 1992)
	st = r.e.Stats(t.Context())[0]
	if st.Dirty != 1 || st.Docs != 5 {
		t.Fatalf("one pending: %+v", st)
	}
	i := r.e.mustIdx(t, indextest.Signal)
	i.dirtySince.Store(time.Now().Add(-time.Minute).UnixNano())
	if lag := r.e.Stats(t.Context())[0].ReconcileLag; lag < time.Minute {
		t.Fatalf("lag = %v", lag)
	}
}
