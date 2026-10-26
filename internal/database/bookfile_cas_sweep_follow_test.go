// file: internal/database/bookfile_cas_sweep_follow_test.go
// version: 1.1.0
// guid: 3c8e1b52-94d7-4a6f-b0e1-7d2a5c9f4e18
// last-edited: 2026-10-26

package database

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// How the book_file sweeps follow, drop and retry a row that changed between
// their read and their commit (reloadSweepRow, retrySweepRows), and how the
// delete paths treat a row stored under a key that names another book.

// A sweep that finds its row missing at the key it read follows it to the key
// the row's own bookID and ID spell. A row whose stored book disagrees with the
// key it sits at can never be found there, so the sweep stops with
// ErrBookFileKeyMismatch instead of calling it busy until its budget runs out.
func TestCAS_SweepBookFileSegDrop_StopsOnARowWhoseBookDisagreesWithItsKey(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000M", "05.m4b", "AQADtAcSRU")
	other := "book-gotham-elsewhere"
	// The row's id index names a key under another book, where a row sits whose
	// JSON says it belongs to a third.
	misfiled := bookFileKey(other, row.ID)
	injectBeforeCommit(t, 1, func(int) {
		bad := *row
		bad.BookID = "book-gotham-third"
		data, err := json.Marshal(&bad)
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := s.db.Delete(bookFileKey(book.ID, row.ID), pebble.NoSync); err != nil {
			t.Errorf("delete: %v", err)
		}
		if err := s.db.Set(misfiled, data, pebble.NoSync); err != nil {
			t.Errorf("set: %v", err)
		}
		if err := s.db.Set([]byte("book_file_id:"+row.ID), misfiled, pebble.NoSync); err != nil {
			t.Errorf("set index: %v", err)
		}
	})

	_, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if !errors.Is(err, ErrBookFileKeyMismatch) {
		t.Fatalf("err = %v, want one wrapping ErrBookFileKeyMismatch", err)
	}
	if errors.Is(err, ErrBookFileConflict) {
		t.Errorf("err = %v: a damaged row is not a busy one, and no retry will clear it", err)
	}
}

// The delete paths build the key they re-read from the row's stored BookID. A
// row filed under another book's key is reported as such, not as a row that
// vanished (which each of them retries until it reports a busy row).
func TestCAS_Deletes_RefuseARowWhoseBookDisagreesWithItsKey(t *testing.T) {
	deletes := map[string]func(f *casFixture, elsewhere string) error{
		"DeleteBookFile": func(f *casFixture, _ string) error { return f.s.DeleteBookFile(f.file.ID) },
		"DeleteBookFilesByIDs": func(f *casFixture, elsewhere string) error {
			_, err := f.s.DeleteBookFilesByIDs(refsOf(elsewhere, f.file.ID))
			return err
		},
		"DeleteBookFilesForBook": func(f *casFixture, _ string) error { return f.s.DeleteBookFilesForBook(f.book.ID) },
	}
	for name, del := range deletes {
		t.Run(name, func(t *testing.T) {
			f := newCASFixture(t)
			elsewhere := "book-gotham-elsewhere"
			rewriteRaw(t, f.s, f.book.ID, f.file.ID, func(r *BookFile) { r.BookID = elsewhere })

			err := del(f, elsewhere)
			if !errors.Is(err, ErrBookFileKeyMismatch) {
				t.Fatalf("err = %v, want one wrapping ErrBookFileKeyMismatch", err)
			}
			if errors.Is(err, ErrBookFileConflict) {
				t.Errorf("err = %v: a damaged row is not a busy one", err)
			}
			if row, _, rerr := f.s.readBookFileRow(f.book.ID, f.file.ID); rerr != nil || row == nil {
				t.Errorf("the damaged row must be left as stored: row=%v err=%v", row, rerr)
			}
		})
	}
}

// A row deleted between the sweep's read and its commit leaves a tombstone; the
// sweep drops it rather than writing it back, and, every row now being swept,
// wipes the LSH index.
func TestCAS_ClearAllAcoustIDFingerprints_DropsARowDeletedMidSweep(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	mustInsertBookFile(t, store, "batman-d", synthRaw(4, 57600))
	if !store.HasLSHIndex("batman-d") {
		t.Fatal("fixture: the LSH index must hold the fingerprinted row before the sweep")
	}
	injectBeforeCommit(t, 1, func(int) {
		// A delete, as its commit leaves the store: no row, a tombstone. (The
		// public delete takes the owner stripe the victim holds in this hook.)
		if err := store.db.Delete(bookFileKey("book-batman-d", "batman-d"), pebble.NoSync); err != nil {
			t.Errorf("delete: %v", err)
		}
		b := store.db.NewBatch()
		if err := stageBookFileTombstone(b, "batman-d"); err != nil {
			t.Errorf("tombstone: %v", err)
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			t.Errorf("commit tombstone: %v", err)
		}
	})

	cleared, total, err := store.ClearAllAcoustIDFingerprints(context.Background(), 100, nil)
	if err != nil {
		t.Fatalf("err = %v, want none: a row deleted mid-sweep is not trouble", err)
	}
	if cleared != 0 || total != 1 {
		t.Errorf("cleared=%d total=%d, want 0/1: the row was deleted, not cleared", cleared, total)
	}
	if got, _, _ := store.readBookFileRow("book-batman-d", "batman-d"); got != nil {
		t.Errorf("the deleted row was written back: %+v", got)
	}
	if store.HasLSHIndex("batman-d") {
		t.Error("no row is left unswept, so the LSH index must have been wiped")
	}
}

// A row written before the id index existed, and moved before the sweep's
// commit, has no index entry and no tombstone: the sweep finds it by scanning.
func TestCAS_SweepBookFileSegDrop_FollowsAMovedRowWithNoIdIndex(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000N", "06.m4b", "AQADtAcSRT")
	dst, err := s.CreateBook(&Book{Title: "Batman: The Long Halloween", FilePath: "/lib/Gotham/Long-Halloween"})
	if err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) {
		moved := *row
		moved.BookID = dst.ID
		data, merr := json.Marshal(&moved)
		if merr != nil {
			t.Errorf("marshal: %v", merr)
			return
		}
		if derr := s.db.Delete(bookFileKey(book.ID, row.ID), pebble.NoSync); derr != nil {
			t.Errorf("delete: %v", derr)
		}
		if serr := s.db.Set(bookFileKey(dst.ID, row.ID), data, pebble.NoSync); serr != nil {
			t.Errorf("set: %v", serr)
		}
	})
	scansBefore := s.bookFileIDScans.Load()

	res, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rewrite != 1 || res.Conflicts != 0 {
		t.Errorf("result = %+v, want the moved row rewritten and nothing left unswept", res)
	}
	if s.bookFileIDScans.Load() == scansBefore {
		t.Error("the row has no index entry and no tombstone: the scan is the only way to find it, and it did not run")
	}
	if got, _ := s.GetBookFileByID(dst.ID, row.ID); got == nil || got.AcoustIDSeg0 != "" {
		t.Errorf("moved row = %+v, want it under the new book with its segs dropped", got)
	}
}

// A row a chunk's commit gave up on (its whole budget lost to churn) is retried
// with a fresh budget after the rest of the sweep, and is swept once the churn
// has stopped.
func TestCAS_SweepBookFileSegDrop_RetryPassClearsARowTheFirstPassGaveUpOn(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000P", "07.m4b", "AQADtAcSRS")
	n := injectBeforeCommit(t, maxStageRetries, func(i int) { setDuration(t, s, row, 100+i) })

	res, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *n != maxStageRetries {
		t.Fatalf("churn ran %d times, want %d (the first pass's whole budget)", *n, maxStageRetries)
	}
	if res.Rewrite != 1 || res.Conflicts != 0 {
		t.Errorf("result = %+v, want the row swept by the retry pass and none left busy", res)
	}
	if got, _ := s.GetBookFileByID(book.ID, row.ID); got == nil || got.AcoustIDSeg0 != "" {
		t.Errorf("row = %+v, want its segs dropped", got)
	}
}

// A chunk refused over one churning row re-stages that row only: the other
// rows' bytes did not change, so their staging is reused, not redone.
func TestCAS_SweepChunk_RestagesOnlyTheRowsThatChanged(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Batman: Hush", FilePath: "/lib/Gotham/Hush"})
	if err != nil {
		t.Fatal(err)
	}
	const rows = 6
	var chunk []sweepRowRef
	var churned *BookFile
	for i := 0; i < rows; i++ {
		row := &BookFile{
			ID: "01HUSH00000000000000000R" + string(rune('A'+i)), BookID: book.ID,
			FilePath: "/lib/Gotham/Hush/r" + string(rune('A'+i)) + ".m4b", Duration: 100,
		}
		writeLegacyBookFileRow(t, s, row)
		_, raw, rerr := s.readBookFileRowAt(bookFileKey(book.ID, row.ID))
		if rerr != nil {
			t.Fatal(rerr)
		}
		chunk = append(chunk, sweepRowRef{key: bookFileKey(book.ID, row.ID), raw: raw})
		if i == 0 {
			churned = row
		}
	}
	stagings := map[string]int{}
	applied := 0
	stage := func(b *GatedBatch, r sweepRowRef) (func(), bool, error) {
		stagings[string(r.key)]++
		return func() { applied++ }, true, b.Set(r.key, r.raw, nil)
	}
	const lostRounds = 3
	injectBeforeCommit(t, lostRounds, func(i int) { setDuration(t, s, churned, 100+i) })

	busy, err := s.commitBookFileSweepChunk(chunk, stage)
	if err != nil || len(busy) != 0 {
		t.Fatalf("busy=%v err=%v, want the chunk committed after %d lost rounds", busy, err, lostRounds)
	}
	for _, r := range chunk {
		want := 1
		if string(r.key) == string(bookFileKey(book.ID, churned.ID)) {
			want = lostRounds + 1
		}
		if got := stagings[string(r.key)]; got != want {
			t.Errorf("%s staged %d times, want %d", r.key, got, want)
		}
	}
	if applied != rows {
		t.Errorf("applied ran %d times, want once per row (%d): a refused round's counters must not run", applied, rows)
	}
}
