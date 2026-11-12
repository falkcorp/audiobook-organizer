// file: core/internal/bleveindex/backoff_test.go
// version: 1.0.1
// guid: 48f5c24b-72f0-47f6-83e3-3203447f8515
// last-edited: 2026-11-12

package bleveindex

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"

	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/index/indextest"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// poisonIDs is n ids that sort before every healthy test id.
func poisonIDs(n int) []string {
	ids := make([]string, n)
	for k := range ids {
		ids[k] = fmt.Sprintf("a%06d", k)
	}
	return ids
}

// failPoison makes every document whose id starts with "a" fail to load and
// counts the attempts.
func failPoison(t *testing.T, i *idx) *atomic.Int64 {
	t.Helper()
	inner := i.spec.Document
	calls := new(atomic.Int64)
	i.spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		if strings.HasPrefix(id, "a") {
			calls.Add(1)
			return nil, false, errors.New("the Arkham records are sealed")
		}
		return inner(ctx, id)
	}
	return calls
}

func (i *idx) backoffEntries() int {
	i.failMu.Lock()
	defer i.failMu.Unlock()
	return len(i.fails)
}

// TestBackoffTableIsBoundedAndKeepsItsEntries: a full table does not wipe
// itself. With a cap of 50 and 120 poison marks, the 50 that were recorded keep
// backing off on the next pass and only the 70 that did not fit are retried; a
// later pass, once the windows are over, sweeps the expired entries and records
// 50 again.
func TestBackoffTableIsBoundedAndKeepsItsEntries(t *testing.T) {
	const limit, poison = 50, 120
	r := seeded(t, func(o *Options) { o.MaxBackoffEntries = limit })
	clk := r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	calls := failPoison(t, i)
	r.markMany(poisonIDs(poison))

	r.reconcile()
	if got := calls.Load(); got != poison {
		t.Fatalf("first pass: %d attempts, want %d", got, poison)
	}
	if got := i.backoffEntries(); got != limit {
		t.Fatalf("first pass: %d entries, want the cap %d", got, limit)
	}
	r.reconcile() // same instant: nothing expired, nothing swept
	if got := calls.Load(); got != poison+(poison-limit) {
		t.Fatalf("second pass: %d attempts in all, want %d: only the %d marks past the cap are retried, the %d recorded ones keep backing off",
			got, poison+(poison-limit), poison-limit, limit)
	}
	if got := i.backoffEntries(); got != limit {
		t.Fatalf("second pass: %d entries, want %d", got, limit)
	}
	clk.Advance(failBackoffBase + time.Second) // every window is over
	before := calls.Load()
	r.reconcile()
	if got := calls.Load() - before; got != poison {
		t.Fatalf("third pass: %d attempts, want all %d (every window over)", got, poison)
	}
	if got := i.backoffEntries(); got != limit {
		t.Fatalf("third pass: %d entries, want %d (expired swept, %d recorded again)", got, limit, limit)
	}
}

// TestBackoffAbovePackageDefaultCap: more poison marks than the default table
// holds. The recorded ones keep backing off on every later pass; only the
// overflow is retried.
func TestBackoffAbovePackageDefaultCap(t *testing.T) {
	if testing.Short() {
		t.Skip("writes and fails 66000 marks")
	}
	const poison = DefaultMaxBackoffEntries + 464
	r := seeded(t)
	r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	calls := failPoison(t, i)
	r.markMany(poisonIDs(poison))
	r.reconcile()
	if got := calls.Load(); got != poison {
		t.Fatalf("first pass: %d attempts, want %d", got, poison)
	}
	if got := i.backoffEntries(); got != DefaultMaxBackoffEntries {
		t.Fatalf("first pass: %d entries, want %d", got, DefaultMaxBackoffEntries)
	}
	for pass := 2; pass <= 3; pass++ {
		before := calls.Load()
		r.reconcile()
		if got := calls.Load() - before; got != poison-DefaultMaxBackoffEntries {
			t.Fatalf("pass %d: %d attempts, want only the %d marks past the table (was every one of the %d retried?)",
				pass, got, poison-DefaultMaxBackoffEntries, poison)
		}
	}
}

// brokenX makes the document x fail while broken is set and counts the attempts.
func brokenX(i *idx, broken *atomic.Bool) *atomic.Int64 {
	inner := i.spec.Document
	calls := new(atomic.Int64)
	i.spec.Document = func(ctx context.Context, id string) (index.Doc, bool, error) {
		if id == "x" && broken.Load() {
			calls.Add(1)
			return nil, false, errors.New("bad data")
		}
		return inner(ctx, id)
	}
	return calls
}

// climb fails x until its backoff window is the maximum.
func climb(r *rig, clk *fakeClock) {
	for range 9 {
		r.reconcile()
		clk.Advance(failBackoffMax + time.Second)
	}
	r.reconcile() // one more failure: the next window is failBackoffMax
}

// TestRewrittenMarkIsNotHeldByItsOldBackoff: an id that has backed off to the
// longest window is fixed by its owner, which writes the entity again and so
// marks it with a new token. The fresh write is indexed on the next pass, not
// after the old window.
func TestRewrittenMarkIsNotHeldByItsOldBackoff(t *testing.T) {
	r := seeded(t)
	clk := r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	var broken atomic.Bool
	broken.Store(true)
	calls := brokenX(i, &broken)
	r.save("x", "Xavier", "xx", 2000)
	climb(r, clk)
	held := calls.Load()
	r.reconcile() // the same mark, inside its window
	if got := calls.Load(); got != held {
		t.Fatalf("the unchanged mark was retried inside its 5 minute window (%d attempts, %d before)", got, held)
	}
	broken.Store(false)
	r.save("x", "Xavier Fixed", "xx", 2001) // a new write: a new token
	r.reconcile()
	if got := r.ids(index.Match("name", "fixed")); !slices.Equal(got, []string{"x"}) || r.dirtyCount() != 0 {
		t.Fatalf("the fixing write was not indexed on the next pass: %v, %d pending", got, r.dirtyCount())
	}
}

// TestRewrittenMarkStartsItsBackoffAgain: a rewrite that is still broken is new
// data too, so its first failure waits failBackoffBase, not the window the old
// mark had climbed to.
func TestRewrittenMarkStartsItsBackoffAgain(t *testing.T) {
	r := seeded(t)
	clk := r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	var broken atomic.Bool
	broken.Store(true)
	calls := brokenX(i, &broken)
	r.save("x", "Xavier", "xx", 2000)
	climb(r, clk)
	r.save("x", "Xavier Again", "xx", 2001) // new token, still broken
	before := calls.Load()
	r.reconcile()
	if got := calls.Load() - before; got != 1 {
		t.Fatalf("the rewritten mark was attempted %d times on the next pass, want 1", got)
	}
	clk.Advance(failBackoffBase + time.Second)
	r.reconcile()
	if got := calls.Load() - before; got != 2 {
		t.Fatalf("%d attempts %s after a rewrite, want 2: the count must restart at the first window, not at 5 minutes",
			got, failBackoffBase+time.Second)
	}
}

// TestRebuildReleasesTheWriterLockWhenItPanics: the rebuild holds the index's
// writer lock; a panic in it must not leave the lock held, or no reconcile pass,
// copy or rebuild of that index could ever run again.
func TestRebuildReleasesTheWriterLockWhenItPanics(t *testing.T) {
	r := seeded(t)
	i := r.e.mustIdx(t, indextest.Signal)
	i.duringRebuild = func() { panic("the Batcomputer overheated") }
	func() {
		defer func() { _ = recover() }()
		_, _ = r.e.runRebuild(taskstest.NewFakeRun(t.Context(), RebuildKind).RC(),
			RebuildParams{Module: string(indextest.Module), Index: indextest.Signal})
	}()
	if !i.op.TryLock() {
		t.Fatal("a panic in the rebuild left the writer lock held")
	}
	i.op.Unlock()
}

// TestBackoffEvictionRules: a full table drops only entries whose window is
// over, never a live one, and records nothing new while every entry is live.
func TestBackoffEvictionRules(t *testing.T) {
	const limit = 20
	r := seeded(t, func(o *Options) { o.MaxBackoffEntries = limit })
	clk := r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	mark := func(id string) index.Mark { return index.Mark{ID: id, Token: []byte("t-" + id)} }
	for k := range limit {
		i.recordFailure(mark(fmt.Sprintf("s%02d", k)))
	}
	if got := i.backoffEntries(); got != limit {
		t.Fatalf("%d entries, want %d", got, limit)
	}
	// Every entry is live: a new failing mark is not recorded, and none is evicted.
	i.recordFailure(mark("new1"))
	if got := i.backoffEntries(); got != limit {
		t.Fatalf("a full table of live entries grew or shrank to %d", got)
	}
	if _, ok := i.fails["new1"]; ok {
		t.Fatal("a new mark was recorded in a table full of live entries")
	}
	if due := i.due([]index.Mark{mark("s00"), mark("new1")}, clk.Now()); len(due) != 1 || due[0].ID != "new1" {
		t.Fatalf("due = %v: the recorded entry must keep backing off and the unrecorded mark be retried", due)
	}
	// A known id fails again while the table is full: its entry is updated.
	i.recordFailure(mark("s00"))
	if f := i.fails["s00"]; f.n != 2 {
		t.Fatalf("a recorded mark's second failure: n = %d, want 2", f.n)
	}
	// The first windows end (2s); s00's second one (4s) has not: the next new
	// failure sweeps the expired entries, keeps the live one and is recorded.
	clk.Advance(failBackoffBase + time.Second)
	i.recordFailure(mark("new2"))
	if _, ok := i.fails["new2"]; !ok {
		t.Fatal("an expired entry was not reclaimed for a new failing mark")
	}
	if _, ok := i.fails["s00"]; !ok {
		t.Fatal("the sweep dropped s00, whose 4s window is not over")
	}
	if got := i.backoffEntries(); got != 2 {
		t.Fatalf("%d entries after the sweep, want 2 (s00 and new2)", got)
	}
}

// refusingIndex refuses any batch that carries one of ids (a document the index
// itself will not take) and applies every other batch, a probe write included.
type refusingIndex struct {
	bleveIndex
	ids []string
}

func (p refusingIndex) Batch(b *bleve.Batch) error {
	for _, id := range p.ids {
		if strings.Contains(b.String(), "'"+id+"'") { // Batch.String lists each id as 'id'
			return errors.New("the index refuses this document")
		}
	}
	return p.bleveIndex.Batch(b)
}

// downIndex is an index that takes no write at all, and counts the attempts.
type downIndex struct {
	bleveIndex
	calls *atomic.Int64
}

func (d downIndex) Batch(*bleve.Batch) error {
	d.calls.Add(1)
	return errors.New("the Batcave is flooded")
}

// wrapIndex swaps the index's backend for the test and restores it after.
func wrapIndex(t *testing.T, i *idx, wrap func(bleveIndex) bleveIndex) {
	t.Helper()
	i.mu.Lock()
	real := i.bi
	i.bi = wrap(real)
	i.mu.Unlock()
	t.Cleanup(func() {
		i.mu.Lock()
		i.bi = real
		i.mu.Unlock()
	})
}

// TestLoneRefusedDocumentBacksOffByItsToken: a document the index refuses, alone
// in its chunk (no healthy neighbour to prove the index works), backs off like
// one that cannot load, keyed on its mark's token. It does not fail the pass
// (r.reconcile fails the test on an error), it is not retried inside its window,
// it is retried when the window is over, and a new write retries it at once and
// starts its backoff again at the first window.
func TestLoneRefusedDocumentBacksOffByItsToken(t *testing.T) {
	r := seeded(t)
	clk := r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	wrapIndex(t, i, func(real bleveIndex) bleveIndex { return refusingIndex{real, []string{"scarecrow"}} })
	failures := func(want int64, when string) {
		t.Helper()
		if got := i.failures.Load(); got != want {
			t.Fatalf("%s: %d refusals, want %d", when, got, want)
		}
	}
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.reconcile()
	failures(1, "first pass")
	if n := r.dirtyCount(); n != 1 {
		t.Fatalf("pending = %d, want the refused mark kept", n)
	}
	r.reconcile()
	failures(1, "second pass inside the window: the lone refused mark must be backing off")
	clk.Advance(failBackoffBase + time.Second)
	r.reconcile()
	failures(2, "after the window")
	clk.Advance(time.Second) // 1s into the second (4s) window
	r.reconcile()
	failures(2, "inside the second window")
	r.save("scarecrow", "Scarecrow Again", "fear", 1942) // a new write: a new token
	r.reconcile()
	failures(3, "right after a rewrite, inside the old window")
	clk.Advance(failBackoffBase + 100*time.Millisecond) // past a restarted first window, not past the old second one
	r.reconcile()
	failures(4, "the rewrite's own backoff restarted at the first window")
}

// TestAllRefusedChunkBacksOffEveryMark: a chunk in which the index refuses every
// document is the documents' fault, not the index's: the pass does not fail, every
// mark stays pending with an entry of its own, and none is retried inside its
// window.
func TestAllRefusedChunkBacksOffEveryMark(t *testing.T) {
	r := seeded(t)
	clk := r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	wrapIndex(t, i, func(real bleveIndex) bleveIndex {
		return refusingIndex{real, []string{"scarecrow", "riddler", "penguin"}}
	})
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.save("riddler", "Riddler", "riddle", 1948)
	r.save("penguin", "Penguin", "bird", 1941)
	r.reconcile() // fails the test on any error
	if got := i.failures.Load(); got != 3 {
		t.Fatalf("%d refusals, want 3", got)
	}
	if n := r.dirtyCount(); n != 3 {
		t.Fatalf("pending = %d, want all three marks kept", n)
	}
	if got := i.backoffEntries(); got != 3 {
		t.Fatalf("%d backoff entries, want one per refused mark", got)
	}
	r.reconcile()
	if got := i.failures.Load(); got != 3 {
		t.Fatalf("%d refusals after a second pass: the marks must be backing off", got)
	}
	clk.Advance(failBackoffBase + time.Second)
	r.reconcile()
	if got := i.failures.Load(); got != 6 {
		t.Fatalf("%d refusals once the windows are over, want 6", got)
	}
}

// TestIndexThatTakesNoWriteIsNotARefusal: when the index cannot take any write,
// the pass fails, no mark is blamed (no backoff, no refusal counted), and the
// documents are not retried one by one against a dead index. Once the index
// works again the marks drain at once, with no window to wait out.
func TestIndexThatTakesNoWriteIsNotARefusal(t *testing.T) {
	r := seeded(t)
	r.useClock()
	i := r.e.mustIdx(t, indextest.Signal)
	r.save("scarecrow", "Scarecrow", "fear", 1941)
	r.save("riddler", "Riddler", "riddle", 1948)
	r.save("penguin", "Penguin", "bird", 1941)
	calls := new(atomic.Int64)
	i.mu.Lock()
	real := i.bi
	i.bi = downIndex{real, calls}
	i.mu.Unlock()
	if err := r.e.Reconcile(t.Context()); err == nil {
		t.Fatal("a pass over an index that takes no write did not fail")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d writes against a dead index, want 2 (the chunk and the probe): the documents must not be retried one by one", got)
	}
	if got := i.failures.Load(); got != 0 {
		t.Fatalf("%d documents counted as refused by an index that is down", got)
	}
	if got := i.backoffEntries(); got != 0 {
		t.Fatalf("%d backoff entries for an index that is down: no mark is to blame", got)
	}
	if n := r.dirtyCount(); n != 3 {
		t.Fatalf("pending = %d, want all three marks kept", n)
	}
	i.mu.Lock()
	i.bi = real
	i.mu.Unlock()
	r.reconcile()
	if n := r.dirtyCount(); n != 0 {
		t.Fatalf("%d marks left after the index came back: nothing may hold them back", n)
	}
}
