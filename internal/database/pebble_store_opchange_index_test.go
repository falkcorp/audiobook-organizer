// file: internal/database/pebble_store_opchange_index_test.go
// version: 1.0.0
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
	mustBackfillOpChange(t, p)
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
	mustBackfillOpChange(t, p)
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
	mustBackfillOpChange(t, p)
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

// TestOpChangeIndex_FallbackBeforeSentinel: until the backfill sentinel is
// set, GetBookChanges must scan, so rows a pre-index binary wrote (no entry)
// are still returned; once it is set, it reads the index.
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
	got, err = p.GetBookChanges("b1")
	if err != nil || len(got) != 2 {
		t.Fatalf("post-backfill GetBookChanges = %v, %v; want both rows", changeIDs(got), err)
	}
	// Prove the index path is in use now: a row written without an entry is
	// invisible to it (this is exactly the rollback hazard the rebuild fixes).
	rawPutOpChange(t, p, &OperationChange{ID: "c3", OperationID: "op3", BookID: "b1"})
	got, _ = p.GetBookChanges("b1")
	if len(got) != 2 {
		t.Fatalf("GetBookChanges after a raw write = %v; want the index path (2 rows)", changeIDs(got))
	}
	res, err := p.RebuildOpChangeByBookIndex(context.Background())
	if err != nil || res.Indexed != 3 {
		t.Fatalf("rebuild = %+v, %v; want 3 indexed", res, err)
	}
	got, _ = p.GetBookChanges("b1")
	if len(got) != 3 {
		t.Fatalf("GetBookChanges after rebuild = %v; want 3 rows", changeIDs(got))
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
	assertIndexedMatchesScan(t, p, []string{"b0", "b1", "b2"})
	if got, _ := p.GetBookChanges("b0"); len(got) != 5 {
		t.Fatalf("GetBookChanges(b0) = %v, want 5", changeIDs(got))
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
	if _, err := p.GetBookChanges("b1"); err == nil {
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

// assertIndexedMatchesScan compares both read paths for each book.
func assertIndexedMatchesScan(t *testing.T, p *PebbleStore, books []string) {
	t.Helper()
	for _, book := range books {
		if book == "" {
			continue // never indexed: GetBookChanges("") always scans
		}
		want, werr := p.getBookChangesScan(book)
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
			mustBackfillOpChange(t, p)

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
			// And the public entry point takes the index path for every
			// non-empty book.
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
	mustBackfillOpChange(t, p)
	if built, _ := p.opChangeByBookIndexBuilt(); !built {
		t.Fatal("not built after backfill")
	}
	staleGen := p.opChangeByBookGen.Load()
	sawScan := false
	opChangeByBookBackfillAfterChunk = func(int) error {
		// A reader that read the sentinel just before the delete committed.
		p.opChangeByBookBuiltAt.Store(staleGen + 1)
		built, err := p.opChangeByBookIndexBuilt()
		if err != nil {
			return err
		}
		sawScan = !built
		return nil
	}
	if _, err := p.RebuildOpChangeByBookIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawScan {
		t.Fatal("a reader trusted the index mid-rebuild")
	}
	if built, _ := p.opChangeByBookIndexBuilt(); !built {
		t.Fatal("not built after rebuild")
	}
}
