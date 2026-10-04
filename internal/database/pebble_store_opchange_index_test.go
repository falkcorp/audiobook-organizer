// file: internal/database/pebble_store_opchange_index_test.go
// version: 1.3.0
// guid: 9e202853-2ab3-4f8f-b567-6c435e5bebb3
// last-edited: 2026-10-03

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func newOpChangeTestStore(t testing.TB) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// opChangeEntries lists the book ids that have an index entry for the row at
// (opID, changeID).
func opChangeEntries(t *testing.T, p *PebbleStore, opID, changeID string) []string {
	t.Helper()
	primary := opChangeKey(opID, changeID)
	suffix := string(primary[len(opChangeKeyPrefix):])
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(opChangeByBookPrefix),
		UpperBound: prefixEnd([]byte(opChangeByBookPrefix)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	var books []string
	for iter.First(); iter.Valid(); iter.Next() {
		rest := string(iter.Key()[len(opChangeByBookPrefix):])
		if len(rest) > len(suffix) && rest[len(rest)-len(suffix):] == suffix && rest[len(rest)-len(suffix)-1] == ':' {
			books = append(books, rest[:len(rest)-len(suffix)-1])
		}
	}
	return books
}

func countOpChangeIndexKeys(t testing.TB, p *PebbleStore) int {
	t.Helper()
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(opChangeByBookPrefix),
		UpperBound: prefixEnd([]byte(opChangeByBookPrefix)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

// rawPutOpChange writes a journal row the way a pre-index binary did: the row
// only, no index entry.
func rawPutOpChange(t testing.TB, p *PebbleStore, c *OperationChange) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opChangeKey(c.OperationID, c.ID), data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
}

func mustBackfillOpChange(t testing.TB, p *PebbleStore) OpChangeByBookBackfillResult {
	t.Helper()
	res, err := p.BackfillOpChangeByBookIndex(context.Background())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	return res
}

// mustTrustOpChange runs the startup path (backfill, verify, trust) and fails
// unless the index ends up trusted, so the test's GetBookChanges calls take
// the indexed path rather than the scan.
func mustTrustOpChange(t testing.TB, p *PebbleStore) OpChangeByBookEnsureResult {
	t.Helper()
	res, err := p.EnsureOpChangeByBookIndex(context.Background())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !res.Trusted || !p.opChangeByBookIndexTrusted() {
		t.Fatalf("ensure = %+v; index not trusted", res)
	}
	return res
}

func changeIDs(cs []*OperationChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.OperationID + "/" + c.ID
	}
	return out
}

func TestOpchangeIndex_CreateWritesEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	c := &OperationChange{OperationID: "op1", BookID: "b1", ChangeType: "metadata_update"}
	if err := p.CreateOperationChange(c); err != nil {
		t.Fatal(err)
	}
	if got := opChangeEntries(t, p, "op1", c.ID); !reflect.DeepEqual(got, []string{"b1"}) {
		t.Fatalf("entries = %v, want [b1]", got)
	}
	// A row with no book is not indexed.
	nob := &OperationChange{OperationID: "op1", ChangeType: "op_level"}
	if err := p.CreateOperationChange(nob); err != nil {
		t.Fatal(err)
	}
	if got := opChangeEntries(t, p, "op1", nob.ID); len(got) != 0 {
		t.Fatalf("empty-book row has entries %v", got)
	}
	if n := countOpChangeIndexKeys(t, p); n != 1 {
		t.Fatalf("index keys = %d, want 1", n)
	}
}

func TestOpchangeIndex_RewriteSameBookKeepsOneEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	c := &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1", NewValue: "v1"}
	if err := p.CreateOperationChange(c); err != nil {
		t.Fatal(err)
	}
	c2 := &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1", NewValue: "v2"}
	if err := p.CreateOperationChange(c2); err != nil {
		t.Fatal(err)
	}
	if got := opChangeEntries(t, p, "op1", "c1"); !reflect.DeepEqual(got, []string{"b1"}) {
		t.Fatalf("entries = %v, want [b1]", got)
	}
	if n := countOpChangeIndexKeys(t, p); n != 1 {
		t.Fatalf("index keys = %d, want 1", n)
	}
}

func TestOpchangeIndex_RewriteNewBookMovesEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	if err := p.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := p.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op1", BookID: "b2"}); err != nil {
		t.Fatal(err)
	}
	if got := opChangeEntries(t, p, "op1", "c1"); !reflect.DeepEqual(got, []string{"b2"}) {
		t.Fatalf("entries = %v, want [b2]", got)
	}
	if got, _ := p.GetBookChanges("b1"); len(got) != 0 {
		t.Fatalf("b1 still has %v", changeIDs(got))
	}
	if got, _ := p.GetBookChanges("b2"); len(got) != 1 {
		t.Fatalf("b2 = %v, want the moved row", changeIDs(got))
	}
	// Moving to no book drops the entry entirely.
	if err := p.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op1"}); err != nil {
		t.Fatal(err)
	}
	if n := countOpChangeIndexKeys(t, p); n != 0 {
		t.Fatalf("index keys = %d after moving to no book, want 0", n)
	}
}

func TestOpchangeIndex_MarkRevertedKeepsEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	a := &OperationChange{OperationID: "op1", BookID: "b1"}
	b := &OperationChange{OperationID: "op1", BookID: "b2"}
	for _, c := range []*OperationChange{a, b} {
		if err := p.CreateOperationChange(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.MarkOperationChangesReverted("op1", []string{a.ID, b.ID, "not-there"}); err != nil {
		t.Fatal(err)
	}
	if n := countOpChangeIndexKeys(t, p); n != 2 {
		t.Fatalf("index keys = %d, want 2", n)
	}
	for _, book := range []string{"b1", "b2"} {
		got, err := p.GetBookChanges(book)
		if err != nil || len(got) != 1 || got[0].RevertedAt == nil {
			t.Fatalf("GetBookChanges(%s) = %v, %v; want one reverted row", book, got, err)
		}
	}
}

func TestOpchangeIndex_PruneDeletesEntry(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	old := &OperationChange{OperationID: "op1", BookID: "b1"}
	if err := p.CreateOperationChange(old); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	cut := time.Now()
	time.Sleep(2 * time.Millisecond)
	keep := &OperationChange{OperationID: "op2", BookID: "b1"}
	if err := p.CreateOperationChange(keep); err != nil {
		t.Fatal(err)
	}
	n, err := p.PruneOperationChanges(cut)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	if got := opChangeEntries(t, p, "op1", old.ID); len(got) != 0 {
		t.Fatalf("pruned row still has entries %v", got)
	}
	if got := opChangeEntries(t, p, "op2", keep.ID); !reflect.DeepEqual(got, []string{"b1"}) {
		t.Fatalf("kept row entries = %v", got)
	}
	got, _ := p.GetBookChanges("b1")
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("GetBookChanges(b1) = %v", changeIDs(got))
	}
}

// TestOpChangeIndex_FallbackBeforeSentinel: until the index is trusted,
// GetBookChanges must scan, so rows a pre-index binary wrote (no entry) are
// still returned, and a backfill alone (sentinel set) does not change that;
// once the startup ensure trusts it, it reads the index.
func TestOpchangeIndex_FallbackBeforeSentinel(t *testing.T) {
	p := newOpChangeTestStore(t)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1"})
	if err := p.CreateOperationChange(&OperationChange{OperationID: "op2", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetBookChanges("b1")
	if err != nil || len(got) != 2 {
		t.Fatalf("pre-sentinel GetBookChanges = %v, %v; want both rows (full scan)", changeIDs(got), err)
	}
	if built, _ := p.opChangeByBookIndexBuilt(); built {
		t.Fatal("sentinel set before any backfill")
	}

	mustBackfillOpChange(t, p)
	if p.opChangeByBookIndexTrusted() {
		t.Fatal("a backfill alone trusted the index")
	}
	// Sentinel set, not trusted: a row written without an entry is still
	// returned, because readers are on the scan.
	rawPutOpChange(t, p, &OperationChange{ID: "c3", OperationID: "op3", BookID: "b1"})
	got, err = p.GetBookChanges("b1")
	if err != nil || len(got) != 3 {
		t.Fatalf("post-backfill GetBookChanges = %v, %v; want all 3 rows (full scan)", changeIDs(got), err)
	}
	// The startup ensure finds the entry-less row and rebuilds before trusting.
	ens := mustTrustOpChange(t, p)
	if ens.Verify.MissingEntries != 1 || !ens.Rebuilt || ens.Rebuild.Indexed != 3 {
		t.Fatalf("ensure = %+v; want 1 missing entry found and a rebuild indexing 3", ens)
	}
	got, err = getBookChangesVia(t, p, "b1")
	if err != nil || len(got) != 3 {
		t.Fatalf("indexed GetBookChanges after ensure = %v, %v; want 3 rows", changeIDs(got), err)
	}
	if again := mustBackfillOpChange(t, p); !again.Skipped {
		t.Fatalf("second backfill = %+v; want skipped", again)
	}
}

// TestOpChangeIndex_BackfillResumesAfterCut cuts the backfill after its second
// chunk commit, checks that readers stay on the full scan, then reruns it and
// checks that it resumes from the cursor rather than restarting.
func TestOpchangeIndex_BackfillResumesAfterCut(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldChunk := opChangeByBookBackfillChunk
	opChangeByBookBackfillChunk = 3
	t.Cleanup(func() { opChangeByBookBackfillChunk = oldChunk; opChangeByBookBackfillAfterChunk = nil })

	const rows = 10
	for i := 0; i < rows; i++ {
		rawPutOpChange(t, p, &OperationChange{
			ID: fmt.Sprintf("c%02d", i), OperationID: "op1", BookID: fmt.Sprintf("b%d", i%3),
		})
	}
	errCut := errors.New("cut")
	opChangeByBookBackfillAfterChunk = func(commits int) error {
		if commits == 2 {
			return errCut
		}
		return nil
	}
	res, err := p.BackfillOpChangeByBookIndex(context.Background())
	if !errors.Is(err, errCut) {
		t.Fatalf("cut backfill err = %v, want the cut", err)
	}
	if res.Scanned != 6 {
		t.Fatalf("cut backfill scanned %d, want 6", res.Scanned)
	}
	if built, _ := p.opChangeByBookIndexBuilt(); built {
		t.Fatal("sentinel set by a cut run")
	}
	// Partial index, but readers must not see it.
	if got, _ := p.GetBookChanges("b0"); len(got) != 4 {
		t.Fatalf("GetBookChanges(b0) mid-backfill = %v, want all 4 (full scan)", changeIDs(got))
	}
	// A live write during the gap is indexed by the writer itself.
	if err := p.CreateOperationChange(&OperationChange{ID: "c00x", OperationID: "op0", BookID: "b0"}); err != nil {
		t.Fatal(err)
	}
	// A pre-index binary (rollback during the gap) writes a row that sorts
	// BEFORE the cursor: the resumed run never visits it.
	rawPutOpChange(t, p, &OperationChange{ID: "c00y", OperationID: "op0", BookID: "b0"})

	opChangeByBookBackfillAfterChunk = nil
	res, err = p.BackfillOpChangeByBookIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ResumedAfter != "opchange:op1:c05" || res.Scanned != 4 {
		t.Fatalf("resumed run = %+v; want resumed after opchange:op1:c05 with 4 rows", res)
	}
	if _, closer, err := p.db.Get([]byte(opChangeByBookCursorKey)); err == nil {
		closer.Close()
		t.Fatal("cursor survived a completed backfill")
	}
	// Sentinel set, but the skipped row has no entry: readers must still be
	// on the scan, and the startup ensure must find it and rebuild.
	if got, _ := p.GetBookChanges("b0"); len(got) != 6 {
		t.Fatalf("GetBookChanges(b0) before ensure = %v, want 6 (full scan)", changeIDs(got))
	}
	ens := mustTrustOpChange(t, p)
	if ens.Verify.MissingEntries != 1 || !ens.Rebuilt {
		t.Fatalf("ensure = %+v; want the skipped row reported missing and a rebuild", ens)
	}
	assertIndexedMatchesScan(t, p, []string{"b0", "b1", "b2"})
	if got, _ := getBookChangesVia(t, p, "b0"); len(got) != 6 {
		t.Fatalf("indexed GetBookChanges(b0) = %v, want 6", changeIDs(got))
	}
}

func TestOpchangeIndex_BackfillCanceled(t *testing.T) {
	p := newOpChangeTestStore(t)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.BackfillOpChangeByBookIndex(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if built, _ := p.opChangeByBookIndexBuilt(); built {
		t.Fatal("sentinel set by a canceled run")
	}
}

// TestOpChangeIndex_UndecodableFailsClosed: one undecodable row anywhere
// fails every GetBookChanges on the full scan; the indexed path must too,
// until that row is rewritten.
func TestOpchangeIndex_UndecodableFailsClosed(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.CreateOperationChange(&OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opChangeKey("op9", "bad"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := p.getBookChangesScan("b1"); err == nil {
		t.Fatal("scan path accepted an undecodable row; the test premise is wrong")
	}
	res := mustBackfillOpChange(t, p)
	if res.Undecodable != 1 {
		t.Fatalf("backfill = %+v, want 1 undecodable", res)
	}
	ens := mustTrustOpChange(t, p)
	if ens.Rebuilt || ens.Verify.UnmarkedUndecodable != 0 || ens.Verify.Undecodable != 1 {
		t.Fatalf("ensure = %+v; want the backfill's marker to satisfy the verify", ens)
	}
	if _, err := getBookChangesVia(t, p, "b1"); err == nil {
		t.Fatal("indexed GetBookChanges ignored an undecodable row; the scan fails on it")
	}
	// Rewriting the row repairs it: the marker goes stale and both paths agree.
	if err := p.CreateOperationChange(&OperationChange{ID: "bad", OperationID: "op9", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	assertIndexedMatchesScan(t, p, []string{"b1"})
	if got, _ := p.GetBookChanges("b1"); len(got) != 2 {
		t.Fatalf("GetBookChanges(b1) = %v, want 2", changeIDs(got))
	}
}

// getBookChangesVia calls GetBookChanges after asserting that it will take
// the indexed path (trusted, sentinel set, indexable id).
func getBookChangesVia(t *testing.T, p *PebbleStore, bookID string) ([]*OperationChange, error) {
	t.Helper()
	usable, err := p.opChangeByBookIndexUsable()
	if err != nil || !usable || !opChangeIndexable(bookID) {
		t.Fatalf("GetBookChanges(%q) would not use the index (usable=%t, err=%v)", bookID, usable, err)
	}
	return p.GetBookChanges(bookID)
}

// assertIndexedMatchesScan compares both read paths for each book. A book id
// that is not indexable (empty, or containing ':') has no entries by design,
// so for it the check is that GetBookChanges itself returns the scan's rows.
func assertIndexedMatchesScan(t *testing.T, p *PebbleStore, books []string) {
	t.Helper()
	for _, book := range books {
		want, werr := p.getBookChangesScan(book)
		if !opChangeIndexable(book) {
			got, gerr := p.GetBookChanges(book)
			if (werr == nil) != (gerr == nil) || !reflect.DeepEqual(changeIDs(want), changeIDs(got)) {
				t.Fatalf("non-indexable book %q: GetBookChanges = %v, %v; scan = %v, %v",
					book, changeIDs(got), gerr, changeIDs(want), werr)
			}
			continue
		}
		got, gerr := p.getBookChangesIndexed(book)
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("book %q: scan err %v, indexed err %v", book, werr, gerr)
		}
		if !reflect.DeepEqual(changeIDs(want), changeIDs(got)) {
			t.Fatalf("book %q order/set differs:\n scan    %v\n indexed %v", book, changeIDs(want), changeIDs(got))
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("book %q rows differ in content", book)
		}
	}
}

// TestOpChangeIndex_PropertyMatchesScan drives random journals (pre-index raw
// rows, creates, rewrites that move books, reverts, prunes, empty book ids and
// book ids that prefix one another) and checks that the indexed read returns
// exactly what the full scan returns, row for row and in order.
func TestOpchangeIndex_PropertyMatchesScan(t *testing.T) {
	books := []string{"b1", "b1:x", "b10", "b2", "B1", ""}
	for seed := int64(1); seed <= 25; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			p := newOpChangeTestStore(t)
			type ref struct{ op, id string }
			var all []ref
			pick := func() string { return books[rng.Intn(len(books))] }
			op := func() string { return fmt.Sprintf("op%d", rng.Intn(6)) }

			// Pre-index history.
			for i := 0; i < 40; i++ {
				r := ref{op(), fmt.Sprintf("raw%03d", i)}
				rawPutOpChange(t, p, &OperationChange{ID: r.id, OperationID: r.op, BookID: pick(), NewValue: "raw"})
				all = append(all, r)
			}
			mustTrustOpChange(t, p)

			var cut time.Time
			for step := 0; step < 200; step++ {
				switch k := rng.Intn(10); {
				case k < 5 || len(all) == 0: // create
					c := &OperationChange{OperationID: op(), BookID: pick(), NewValue: fmt.Sprint(step)}
					if err := p.CreateOperationChange(c); err != nil {
						t.Fatal(err)
					}
					all = append(all, ref{c.OperationID, c.ID})
				case k < 8: // rewrite by id, possibly to another book
					r := all[rng.Intn(len(all))]
					if err := p.CreateOperationChange(&OperationChange{ID: r.id, OperationID: r.op, BookID: pick(), NewValue: "rw"}); err != nil {
						t.Fatal(err)
					}
				default: // revert-mark a few rows of one op
					r := all[rng.Intn(len(all))]
					if err := p.MarkOperationChangesReverted(r.op, []string{r.id, all[rng.Intn(len(all))].id}); err != nil {
						t.Fatal(err)
					}
				}
				if step == 100 {
					time.Sleep(time.Millisecond)
					cut = time.Now()
					time.Sleep(time.Millisecond)
				}
			}
			assertIndexedMatchesScan(t, p, books)
			if _, err := p.PruneOperationChanges(cut); err != nil {
				t.Fatal(err)
			}
			assertIndexedMatchesScan(t, p, books)
			// And the public entry point returns the scan's rows for every
			// book: through the index for indexable ids (trusted above),
			// through the scan for "" and "b1:x".
			for _, b := range books {
				got, err := p.GetBookChanges(b)
				want, _ := p.getBookChangesScan(b)
				if err != nil || !reflect.DeepEqual(changeIDs(got), changeIDs(want)) {
					t.Fatalf("GetBookChanges(%q) = %v, %v; want %v", b, changeIDs(got), err, changeIDs(want))
				}
			}
		})
	}
}

// TestOpChangeIndex_BackfillRacesLiveWrites runs the backfill (small chunks)
// while writers create rows, move rows between books and prune, then checks
// that the finished index serves exactly what the scan does.
func TestOpchangeIndex_BackfillRacesLiveWrites(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldChunk := opChangeByBookBackfillChunk
	opChangeByBookBackfillChunk = 7
	t.Cleanup(func() { opChangeByBookBackfillChunk = oldChunk })
	books := []string{"b1", "b2", "b3", "b4"}
	for i := 0; i < 600; i++ {
		rawPutOpChange(t, p, &OperationChange{
			ID: fmt.Sprintf("r%04d", i), OperationID: fmt.Sprintf("op%d", i%9), BookID: books[i%4],
			CreatedAt: time.Now().Add(-time.Hour),
		})
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				r := rng.Intn(600)
				// Move an existing (possibly pre-index) row to another book.
				if err := p.CreateOperationChange(&OperationChange{
					ID: fmt.Sprintf("r%04d", r), OperationID: fmt.Sprintf("op%d", r%9), BookID: books[rng.Intn(4)],
				}); err != nil {
					t.Error(err)
					return
				}
				if err := p.CreateOperationChange(&OperationChange{OperationID: "opw", BookID: books[rng.Intn(4)]}); err != nil {
					t.Error(err)
					return
				}
				if i%50 == 0 {
					if _, err := p.PruneOperationChanges(time.Now().Add(-30 * time.Minute)); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}(w)
	}
	res, err := p.BackfillOpChangeByBookIndex(context.Background())
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits < 2 {
		t.Fatalf("backfill = %+v; want a multi-chunk run", res)
	}
	assertIndexedMatchesScan(t, p, books)
	// Completeness under the race is what lets the startup ensure trust the
	// index without a rebuild.
	if ens := mustTrustOpChange(t, p); ens.Rebuilt || ens.Verify.MissingEntries != 0 {
		t.Fatalf("ensure after a raced backfill = %+v; want a clean verify", ens)
	}
}

// seedOpChangeJournal writes rows journal rows spread over books books, the
// way prod's journal looks (a few rows per book per op), straight into Pebble.
func seedOpChangeJournal(b testing.TB, p *PebbleStore, rows, books int) {
	b.Helper()
	batch := p.db.NewBatch()
	now := time.Now()
	for i := 0; i < rows; i++ {
		c := &OperationChange{
			ID:          fmt.Sprintf("%026d", i),
			OperationID: fmt.Sprintf("op%05d", i/50),
			BookID:      fmt.Sprintf("book%06d", (i*7919)%books),
			ChangeType:  "metadata_update",
			FieldName:   "title",
			OldValue:    "an old value of typical length for a title field",
			NewValue:    "a new value of typical length for a title field",
			CreatedAt:   now,
		}
		data, err := json.Marshal(c)
		if err != nil {
			b.Fatal(err)
		}
		if err := batch.Set(opChangeKey(c.OperationID, c.ID), data, nil); err != nil {
			b.Fatal(err)
		}
		if batch.Len() > 4<<20 {
			if err := batch.Commit(pebble.NoSync); err != nil {
				b.Fatal(err)
			}
			batch = p.db.NewBatch()
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkGetBookChanges compares the full-scan path with the index at a
// 300k-row journal on a real on-disk store.
//
//	go test -run '^$' -bench BenchmarkGetBookChanges -benchtime 20x ./internal/database/
func BenchmarkGetBookChanges(b *testing.B) {
	const rows, books = 300_000, 30_000
	p, err := NewPebbleStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = p.Close() })
	seedOpChangeJournal(b, p, rows, books)
	if _, err := p.BackfillOpChangeByBookIndex(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Run("scan", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			got, err := p.getBookChangesScan(fmt.Sprintf("book%06d", i%books))
			if err != nil || len(got) == 0 {
				b.Fatalf("scan: %d rows, %v", len(got), err)
			}
		}
	})
	b.Run("indexed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			got, err := p.GetBookChanges(fmt.Sprintf("book%06d", i%books))
			if err != nil || len(got) == 0 {
				b.Fatalf("indexed: %d rows, %v", len(got), err)
			}
		}
	})
}

// BenchmarkOpChangeBackfill times the one-time backfill over a 300k-row
// on-disk journal; the deploy note's 1M-row estimate scales from it.
//
//	go test -run '^$' -bench BenchmarkOpChangeBackfill -benchtime 1x ./internal/database/
func BenchmarkOpChangeBackfill(b *testing.B) {
	const rows, books = 300_000, 30_000
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		p, err := NewPebbleStore(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		seedOpChangeJournal(b, p, rows, books)
		b.StartTimer()
		res, err := p.BackfillOpChangeByBookIndex(context.Background())
		b.StopTimer()
		if err != nil || res.Scanned != rows {
			b.Fatalf("backfill = %+v, %v", res, err)
		}
		_ = p.Close()
	}
}

// TestOpchangeIndex_RebuildInvalidatesCachedSentinel: while a rebuild runs,
// readers must be on the full scan even though they cached a positive
// sentinel read before it started, including one cached from a Get that
// raced the sentinel delete (simulated by storing the pre-rebuild generation).
func TestOpchangeIndex_RebuildInvalidatesCachedSentinel(t *testing.T) {
	p := newOpChangeTestStore(t)
	t.Cleanup(func() { opChangeByBookBackfillAfterChunk = nil })
	if err := p.CreateOperationChange(&OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	mustTrustOpChange(t, p)
	if built, _ := p.opChangeByBookIndexBuilt(); !built {
		t.Fatal("not built after backfill")
	}
	staleGen := p.opChangeByBookGen.Load()
	sawScan, sawTrusted := false, false
	opChangeByBookBackfillAfterChunk = func(int) error {
		// A reader that read the sentinel just before the delete committed.
		p.opChangeByBookBuiltAt.Store(staleGen + 1)
		built, err := p.opChangeByBookIndexBuilt()
		if err != nil {
			return err
		}
		sawScan = !built
		if p.opChangeByBookIndexTrusted() {
			sawTrusted = true
		}
		return nil
	}
	if _, err := p.RebuildOpChangeByBookIndex(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !sawScan || sawTrusted {
		t.Fatalf("a reader could use the index mid-rebuild (sentinel seen missing=%t, trusted=%t)", sawScan, sawTrusted)
	}
	if built, _ := p.opChangeByBookIndexBuilt(); !built {
		t.Fatal("not built after rebuild")
	}
	if !p.opChangeByBookIndexTrusted() {
		t.Fatal("a successful rebuild did not trust the index")
	}
}

func TestOpchangeIndex_Verify(t *testing.T) {
	p := newOpChangeTestStore(t)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1"})
	rawPutOpChange(t, p, &OperationChange{ID: "c2", OperationID: "op1"})
	if err := p.db.Set(opChangeKey("op9", "bad"), []byte("{"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	rep, err := p.VerifyOpChangeByBookIndex(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SentinelSet || rep.Rows != 3 || rep.Indexable != 1 || rep.MissingEntries != 1 ||
		rep.Undecodable != 1 || rep.UnmarkedUndecodable != 1 {
		t.Fatalf("pre-backfill report = %+v", rep)
	}
	mustBackfillOpChange(t, p)
	rep, err = p.VerifyOpChangeByBookIndex(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.SentinelSet || rep.MissingEntries != 0 || rep.UnmarkedUndecodable != 0 || rep.Undecodable != 1 {
		t.Fatalf("post-backfill report = %+v", rep)
	}
}

// TestOpchangeIndex_PruneLeavesNoOrphans: nightly retention must take each
// pruned row's index entry with it, so no orphan entries accumulate.
func TestOpchangeIndex_PruneLeavesNoOrphans(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	for i := 0; i < 50; i++ {
		if err := p.CreateOperationChange(&OperationChange{
			OperationID: fmt.Sprintf("op%d", i%5), BookID: fmt.Sprintf("b%d", i%7),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Move some rows between books first, so pruned rows have moved entries.
	all, err := p.GetOperationChanges("op1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range all {
		c.BookID = "moved"
		if err := p.CreateOperationChange(c); err != nil {
			t.Fatal(err)
		}
	}
	if n := countOpChangeIndexKeys(t, p); n != 50 {
		t.Fatalf("index keys before prune = %d, want 50", n)
	}
	n, err := p.PruneOperationChanges(time.Now().Add(time.Second))
	if err != nil || n != 50 {
		t.Fatalf("prune = %d, %v; want 50", n, err)
	}
	if n := countOpChangeIndexKeys(t, p); n != 0 {
		t.Fatalf("index keys after pruning every row = %d, want 0", n)
	}
}

// TestOpchangeIndex_DanglingEntrySkipped: an entry whose row is gone (e.g. a
// backfill that re-Set the entry after a concurrent prune) is skipped, never
// an error.
func TestOpchangeIndex_DanglingEntrySkipped(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	keep := &OperationChange{OperationID: "op1", BookID: "b1"}
	if err := p.CreateOperationChange(keep); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set(opChangeByBookKey("b1", opChangeKey("op0", "gone")), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetBookChanges("b1")
	if err != nil || len(got) != 1 || got[0].ID != keep.ID {
		t.Fatalf("GetBookChanges with a dangling entry = %v, %v; want only the live row", changeIDs(got), err)
	}
}

// TestOpchangeIndex_OperationDeletesKeepJournal: the per-op delete paths
// (DeleteOperationV2, used by registry.Discard, and DeleteOperationWithLogs)
// do not delete journal rows, so they have no index entries to delete. Pin it:
// if either ever starts deleting opchange rows, it must take the entries too.
func TestOpchangeIndex_OperationDeletesKeepJournal(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	if err := p.InsertOperationV2(OperationV2Row{ID: "op1", DefID: "test.def", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := p.CreateOperationChange(&OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, deleted, err := p.DeleteOperationV2("op1", []string{"completed"}); err != nil || !deleted {
		t.Fatalf("DeleteOperationV2 = %v, %v", deleted, err)
	}
	if err := p.DeleteOperationWithLogs("op1"); err != nil {
		t.Fatal(err)
	}
	rows, err := p.GetOperationChanges("op1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("journal rows after op deletes = %d, %v; want 1", len(rows), err)
	}
	if n := countOpChangeIndexKeys(t, p); n != 1 {
		t.Fatalf("index keys = %d, want 1 (row and entry both kept)", n)
	}
	assertIndexedMatchesScan(t, p, []string{"b1"})
}

// TestOpchangeIndex_RollbackRowNotHidden (review probe P1): a row a pre-index
// binary writes after the sentinel is set must never be hidden. Before this
// boot's ensure the reader scans; the ensure's verify finds the row missing,
// rebuilds, and only then trusts the index, which then returns the row.
func TestOpchangeIndex_RollbackRowNotHidden(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustBackfillOpChange(t, p)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "B", CreatedAt: time.Now()})
	got, err := p.GetBookChanges("B")
	if err != nil {
		t.Fatal(err)
	}
	scan, _ := p.getBookChangesScan("B")
	if len(got) != len(scan) || len(got) != 1 {
		t.Fatalf("ROLLBACK HAZARD: GetBookChanges returns %d rows, scan returns %d", len(got), len(scan))
	}

	ens := mustTrustOpChange(t, p)
	if ens.Verify.MissingEntries != 1 || !ens.Rebuilt {
		t.Fatalf("ensure = %+v; want MissingEntries=1 and a rebuild", ens)
	}
	got, err = getBookChangesVia(t, p, "B")
	if err != nil || len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("indexed GetBookChanges(B) = %v, %v; want the rolled-back row", changeIDs(got), err)
	}
}

// TestOpchangeIndex_TrustedRollbackAcrossReboot: a process that trusted the
// index, then a rollback binary writes, then a new process opens the same
// store: it must not inherit trust from the sentinel.
func TestOpchangeIndex_TrustedRollbackAcrossReboot(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPebbleStoreInMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustTrustOpChange(t, p)
	// Simulate "the old binary ran" by writing a raw row, then a fresh
	// process: a new PebbleStore value over the same db has no trust.
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "B"})
	fresh := &PebbleStore{db: p.db}
	if fresh.opChangeByBookIndexTrusted() {
		t.Fatal("a new process trusted the index without verifying")
	}
	if got, err := fresh.GetBookChanges("B"); err != nil || len(got) != 1 {
		t.Fatalf("fresh GetBookChanges(B) = %v, %v; want the row via the scan", changeIDs(got), err)
	}
	_ = p.Close()
}

// TestOpchangeIndex_ColonBookIDNotIndexed (review probe P2): book ids with ':'
// would collide on the index key ("a:b" + op1:c1 and "a" + b:op1:c1 share
// opchange_by_book:a:b:op1:c1), so they get no entries and GetBookChanges
// scans for them; pruning the "a" row must not disturb "a:b".
func TestOpchangeIndex_ColonBookIDNotIndexed(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	old := time.Now().Add(-48 * time.Hour)
	if err := p.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op1", BookID: "a:b"}); err != nil {
		t.Fatal(err)
	}
	if got := opChangeEntries(t, p, "op1", "c1"); len(got) != 0 {
		t.Fatalf("colon book id got index entries %v", got)
	}
	if err := p.CreateOperationChange(&OperationChange{ID: "op1:c1", OperationID: "b", BookID: "a"}); err != nil {
		t.Fatal(err)
	}
	// Exactly one index key exists, and it is row Y's (book "a").
	if n := countOpChangeIndexKeys(t, p); n != 1 {
		t.Fatalf("index keys = %d, want 1 (only book a's row)", n)
	}
	rawPutOpChange(t, p, &OperationChange{ID: "op1:c1", OperationID: "b", BookID: "a", CreatedAt: old})
	if n, err := p.PruneOperationChanges(time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
	got, err := p.GetBookChanges("a:b")
	scan, _ := p.getBookChangesScan("a:b")
	if err != nil || len(got) != 1 || len(got) != len(scan) {
		t.Fatalf("COLLISION: book a:b GetBookChanges = %v, %v; scan = %v", changeIDs(got), err, changeIDs(scan))
	}
	assertIndexedMatchesScan(t, p, []string{"a", "a:b"})
	if n := countOpChangeIndexKeys(t, p); n != 0 {
		t.Fatalf("index keys after prune = %d, want 0", n)
	}
	// Backfill and verify apply the same rule: a raw colon row is neither
	// indexed by a rebuild nor counted as missing by the verify.
	rawPutOpChange(t, p, &OperationChange{ID: "c2", OperationID: "op2", BookID: "x:y"})
	rep, err := p.VerifyOpChangeByBookIndex(context.Background(), nil)
	if err != nil || rep.MissingEntries != 0 || rep.Indexable != 0 {
		t.Fatalf("verify = %+v, %v; want colon rows not indexable", rep, err)
	}
	res, err := p.RebuildOpChangeByBookIndex(context.Background(), nil)
	if err != nil || res.Indexed != 0 {
		t.Fatalf("rebuild = %+v, %v; want nothing indexed", res, err)
	}
}

// TestOpchangeIndex_UndecodableAfterBackfill (review probe P3): a row that
// becomes undecodable after the backfill has no marker. Before the ensure the
// reader scans and fails on it; the ensure's verify counts it unmarked and
// rebuilds (marking it) before trusting, and the indexed reader then fails
// on it too.
func TestOpchangeIndex_UndecodableAfterBackfill(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.CreateOperationChange(&OperationChange{OperationID: "op1", BookID: "B"}); err != nil {
		t.Fatal(err)
	}
	mustBackfillOpChange(t, p)
	if err := p.db.Set([]byte("opchange:op2:zz"), []byte("{not json"), nil); err != nil {
		t.Fatal(err)
	}
	_, ierr := p.GetBookChanges("B")
	_, serr := p.getBookChangesScan("B")
	if serr == nil || ierr == nil {
		t.Fatalf("DIVERGENCE: GetBookChanges err=%v scan err=%v; both must fail", ierr, serr)
	}
	ens := mustTrustOpChange(t, p)
	if ens.Verify.UnmarkedUndecodable != 1 || !ens.Rebuilt || ens.Rebuild.Undecodable != 1 {
		t.Fatalf("ensure = %+v; want UnmarkedUndecodable=1 and a rebuild that marks it", ens)
	}
	if _, err := getBookChangesVia(t, p, "B"); err == nil {
		t.Fatal("indexed GetBookChanges ignored an undecodable row after the ensure")
	}
}

// TestOpchangeIndex_PruneChunksLeaveNoOrphans: a prune larger than one chunk
// commits in several batches and still takes every row's entry with it.
func TestOpchangeIndex_PruneChunksLeaveNoOrphans(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldChunk := opChangePruneChunk
	opChangePruneChunk = 4
	t.Cleanup(func() { opChangePruneChunk = oldChunk })
	mustTrustOpChange(t, p)
	const rows = 23 // > 5 chunks, last one partial
	for i := 0; i < rows; i++ {
		if err := p.CreateOperationChange(&OperationChange{
			OperationID: fmt.Sprintf("op%d", i%3), BookID: fmt.Sprintf("b%d", i%5),
		}); err != nil {
			t.Fatal(err)
		}
	}
	keep := &OperationChange{OperationID: "op9", BookID: "b1"}
	time.Sleep(2 * time.Millisecond)
	cut := time.Now()
	time.Sleep(2 * time.Millisecond)
	if err := p.CreateOperationChange(keep); err != nil {
		t.Fatal(err)
	}
	n, err := p.PruneOperationChanges(cut)
	if err != nil || n != rows {
		t.Fatalf("prune = %d, %v; want %d", n, err, rows)
	}
	if k := countOpChangeIndexKeys(t, p); k != 1 {
		t.Fatalf("index keys after prune = %d, want 1 (the kept row)", k)
	}
	rep, err := p.VerifyOpChangeByBookIndex(context.Background(), nil)
	if err != nil || rep.Rows != 1 || rep.MissingEntries != 0 {
		t.Fatalf("verify after prune = %+v, %v", rep, err)
	}
}

// TestOpchangeIndex_PruneSparesRowRewrittenMidPrune: a row the prune's
// iterator saw as old, then rewritten under the same id (new CreatedAt, new
// book) before the chunk commits, must survive with its new entry.
func TestOpchangeIndex_PruneSparesRowRewrittenMidPrune(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	t.Cleanup(func() { opChangePruneBeforeFlush = nil })
	old := time.Now().Add(-48 * time.Hour)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1", CreatedAt: old})
	rawPutOpChange(t, p, &OperationChange{ID: "c2", OperationID: "op1", BookID: "b1", CreatedAt: old})
	mustTrustOpChange(t, p) // the raw rows get entries via the ensure's rebuild
	opChangePruneBeforeFlush = func() {
		opChangePruneBeforeFlush = nil
		if err := p.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op1", BookID: "b2"}); err != nil {
			t.Error(err)
		}
	}
	n, err := p.PruneOperationChanges(time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1 (only the untouched old row)", n, err)
	}
	got, err := getBookChangesVia(t, p, "b2")
	if err != nil || len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("GetBookChanges(b2) = %v, %v; want the rewritten row", changeIDs(got), err)
	}
	if e := opChangeEntries(t, p, "op1", "c1"); !reflect.DeepEqual(e, []string{"b2"}) {
		t.Fatalf("rewritten row entries = %v, want [b2]", e)
	}
	assertIndexedMatchesScan(t, p, []string{"b1", "b2"})
}

// TestOpchangeIndex_JournalLockBlocksRewriteDuringPruneCommit pins the journal
// RWMutex. The hook runs inside the prune chunk's locked section, after it
// re-read row c1 as old (book b1) and staged the delete of c1 and its b1
// entry. A CreateOperationChange rewriting c1 onto b2 starts there; it must
// block on the read lock until the chunk commits. Without the lock it would
// commit first and the chunk's delete would then erase the rewrite, leaving an
// orphan b2 entry.
func TestOpchangeIndex_JournalLockBlocksRewriteDuringPruneCommit(t *testing.T) {
	p := newOpChangeTestStore(t)
	t.Cleanup(func() { opChangePruneBeforeCommit = nil })
	old := time.Now().Add(-48 * time.Hour)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1", CreatedAt: old})
	mustTrustOpChange(t, p) // the raw row gets its entry via the ensure's rebuild

	rewriteDone := make(chan error, 1)
	var doneBeforeCommit bool
	opChangePruneBeforeCommit = func() {
		opChangePruneBeforeCommit = nil
		go func() {
			rewriteDone <- p.CreateOperationChange(&OperationChange{ID: "c1", OperationID: "op1", BookID: "b2"})
		}()
		select {
		case <-rewriteDone:
			doneBeforeCommit = true
		case <-time.After(300 * time.Millisecond):
		}
	}
	n, err := p.PruneOperationChanges(time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1 (the stale view of c1)", n, err)
	}
	if doneBeforeCommit {
		t.Fatal("rewrite completed while the prune chunk held the journal lock")
	}
	select {
	case err := <-rewriteDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rewrite never completed after the prune released the lock")
	}
	got, err := getBookChangesVia(t, p, "b2")
	if err != nil || len(got) != 1 || got[0].ID != "c1" {
		t.Fatalf("GetBookChanges(b2) = %v, %v; want the rewritten row to survive", changeIDs(got), err)
	}
	if e := opChangeEntries(t, p, "op1", "c1"); !reflect.DeepEqual(e, []string{"b2"}) {
		t.Fatalf("entries for c1 = %v, want exactly [b2] (no orphan, no b1)", e)
	}
	assertIndexedMatchesScan(t, p, []string{"b1", "b2"})
}

// TestOpchangeIndex_MarkRevertedSkipsRowPrunedAfterScan: a row pruned between
// MarkOperationChangesReverted's scan and its lock is not resurrected, and the
// rows still present are marked in the same call.
func TestOpchangeIndex_MarkRevertedSkipsRowPrunedAfterScan(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	t.Cleanup(func() { opChangeMarkAfterScan = nil })
	old := time.Now().Add(-48 * time.Hour)
	rawPutOpChange(t, p, &OperationChange{ID: "c1", OperationID: "op1", BookID: "b1", CreatedAt: old})
	if err := p.CreateOperationChange(&OperationChange{ID: "c2", OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	mustTrustOpChange(t, p)
	opChangeMarkAfterScan = func() {
		opChangeMarkAfterScan = nil
		if n, err := p.PruneOperationChanges(time.Now().Add(-time.Hour)); err != nil || n != 1 {
			t.Errorf("prune = %d, %v; want 1 (c1)", n, err)
		}
	}
	if err := p.MarkOperationChangesReverted("op1", []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetOperationChanges("op1")
	if err != nil || len(got) != 1 || got[0].ID != "c2" || got[0].RevertedAt == nil {
		t.Fatalf("rows after mark = %v, %v; want only c2, reverted (c1 stays pruned)", changeIDs(got), err)
	}
	if e := opChangeEntries(t, p, "op1", "c1"); len(e) != 0 {
		t.Fatalf("pruned row regained entries %v", e)
	}
	assertIndexedMatchesScan(t, p, []string{"b1"})
}

// TestOpchangeIndex_RebuildWaitRespectsContext: a rebuild queued behind
// another index pass gives up when its context ends, and heartbeats while it
// waits.
func TestOpchangeIndex_RebuildWaitRespectsContext(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldBeat := opChangeIdxWaitHeartbeat
	opChangeIdxWaitHeartbeat = 5 * time.Millisecond
	t.Cleanup(func() { opChangeIdxWaitHeartbeat = oldBeat })
	unlock, err := p.lockOpChangeIdxRun(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	var mu sync.Mutex
	waits := 0
	_, err = p.RebuildOpChangeByBookIndex(ctx, func(phase string, _ int) {
		if phase == "waiting" {
			mu.Lock()
			waits++
			mu.Unlock()
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context deadline", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if waits == 0 {
		t.Fatal("no heartbeat while waiting for the slot")
	}
}

// TestOpchangeIndex_RebuildProgressPerChunk: the rebuild reports once per
// committed chunk.
func TestOpchangeIndex_RebuildProgressPerChunk(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldChunk := opChangeByBookBackfillChunk
	opChangeByBookBackfillChunk = 3
	t.Cleanup(func() { opChangeByBookBackfillChunk = oldChunk })
	for i := 0; i < 10; i++ {
		rawPutOpChange(t, p, &OperationChange{ID: fmt.Sprintf("c%02d", i), OperationID: "op1", BookID: "b1"})
	}
	var calls []int
	res, err := p.RebuildOpChangeByBookIndex(context.Background(), func(phase string, rows int) {
		if phase == "rebuild" {
			calls = append(calls, rows)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 6, 9, 10}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("progress calls = %v, want %v (res %+v)", calls, want, res)
	}
}

// TestOpchangeIndex_ResetClearsTrust: Reset wipes the journal and the index;
// it drops trust after its commit, so readers scan until the next ensure.
func TestOpchangeIndex_ResetClearsTrust(t *testing.T) {
	p := newOpChangeTestStore(t)
	mustTrustOpChange(t, p)
	if err := p.Reset(); err != nil {
		t.Fatal(err)
	}
	if p.opChangeByBookIndexTrusted() {
		t.Fatal("trust survived Reset")
	}
	if err := p.CreateOperationChange(&OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := p.GetBookChanges("b1"); err != nil || len(got) != 1 {
		t.Fatalf("GetBookChanges after Reset = %v, %v", changeIDs(got), err)
	}
}

// TestOpchangeIndex_FailedRebuildLeavesUntrusted: a rebuild clears trust at
// its start and sets it only on success, so a cut rebuild leaves readers on
// the scan, where an entry-less row is still returned.
func TestOpchangeIndex_FailedRebuildLeavesUntrusted(t *testing.T) {
	p := newOpChangeTestStore(t)
	oldChunk := opChangeByBookBackfillChunk
	opChangeByBookBackfillChunk = 2
	t.Cleanup(func() { opChangeByBookBackfillChunk = oldChunk; opChangeByBookBackfillAfterChunk = nil })
	for i := 0; i < 5; i++ {
		if err := p.CreateOperationChange(&OperationChange{OperationID: "op1", BookID: "b1"}); err != nil {
			t.Fatal(err)
		}
	}
	mustTrustOpChange(t, p)
	rawPutOpChange(t, p, &OperationChange{ID: "raw", OperationID: "op0", BookID: "b1"})
	errCut := errors.New("cut")
	opChangeByBookBackfillAfterChunk = func(int) error { return errCut }
	if _, err := p.RebuildOpChangeByBookIndex(context.Background(), nil); !errors.Is(err, errCut) {
		t.Fatalf("rebuild err = %v, want the cut", err)
	}
	if p.opChangeByBookIndexTrusted() {
		t.Fatal("a failed rebuild left the index trusted")
	}
	if got, err := p.GetBookChanges("b1"); err != nil || len(got) != 6 {
		t.Fatalf("GetBookChanges after a failed rebuild = %v, %v; want all 6 rows (scan)", changeIDs(got), err)
	}
}
