// file: internal/database/upsert_no_resurrect_test.go
// version: 1.0.0
// guid: d2edb83b-f462-4f1e-86d2-36acb3f92f29
// last-edited: 2026-09-19

package database

import (
	"errors"
	"testing"
)

// A backfill (tag_backfill, duration_backfill) holds a row it read; a dedupe
// or DeleteBookFile then removes that row; the backfill's upsert must not
// write the deleted row back under the same ID. Reviewer probe, kept.
func TestBatchUpsert_StaleStructOfDeletedRowIsRefused(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	b, err := store.CreateBook(&Book{Title: "b", FilePath: "/lib/r/book"})
	if err != nil {
		t.Fatal(err)
	}
	row := &BookFile{BookID: b.ID, FilePath: "/lib/r/book/01.mp3", ITunesPersistentID: "PIDX"}
	if err := store.CreateBookFile(row); err != nil {
		t.Fatal(err)
	}
	other := &BookFile{BookID: b.ID, FilePath: "/lib/r/book/02.mp3"}
	if err := store.CreateBookFile(other); err != nil {
		t.Fatal(err)
	}
	stale := *row
	stale.Title = "tag from backfill"
	fresh := *other
	fresh.Title = "still here"
	if err := store.DeleteBookFile(row.ID); err != nil {
		t.Fatal(err)
	}

	err = store.BatchUpsertBookFiles([]*BookFile{&stale, &fresh})
	var refused *BookFileRowsRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *BookFileRowsRefusedError", err)
	}
	if len(refused.RefusedFileIDs) != 1 || refused.RefusedFileIDs[0] != row.ID || refused.Reasons[row.ID] == "" || refused.Committed != 1 {
		t.Fatalf("refusal = %+v, want only %s refused with a reason and 1 committed", refused, row.ID)
	}
	got, _ := store.GetBookFiles(b.ID)
	if len(got) != 1 || got[0].ID != other.ID || got[0].Title != "still here" {
		t.Fatalf("rows after upsert = %+v, want only the live row, updated", got)
	}
}

// A stale struct whose path now belongs to a DIFFERENT row (the dedupe
// survivor) must not be merged onto that row either.
func TestBatchUpsert_StaleStructNotMergedIntoSurvivor(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/r2/book"})
	dup := &BookFile{BookID: b.ID, FilePath: "/lib/r2/book/01.mp3"}
	if err := store.CreateBookFile(dup); err != nil {
		t.Fatal(err)
	}
	stale := *dup
	stale.Title = "stale"
	if err := store.DeleteBookFile(dup.ID); err != nil {
		t.Fatal(err)
	}
	survivor := &BookFile{BookID: b.ID, FilePath: "/lib/r2/book/01.mp3", Title: "survivor"}
	if err := store.CreateBookFile(survivor); err != nil {
		t.Fatal(err)
	}
	err := store.BatchUpsertBookFiles([]*BookFile{&stale})
	var refused *BookFileRowsRefusedError
	if !errors.As(err, &refused) || len(refused.RefusedFileIDs) != 1 {
		t.Fatalf("err = %v, want the stale row refused", err)
	}
	got, _ := store.GetBookFiles(b.ID)
	if len(got) != 1 || got[0].ID != survivor.ID || got[0].Title != "survivor" {
		t.Fatalf("rows = %+v, want the survivor untouched", got)
	}
}

// A row with no ID is still an insert (the scanner and the iTunes sync hand
// bare rows); a row naming a live ID is still an update.
func TestBatchUpsert_InsertsAndLiveUpdatesStillWork(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/r3/book"})
	live := &BookFile{BookID: b.ID, FilePath: "/lib/r3/book/01.mp3"}
	if err := store.CreateBookFile(live); err != nil {
		t.Fatal(err)
	}
	upd := *live
	upd.Title = "updated"
	if err := store.BatchUpsertBookFiles([]*BookFile{&upd, {BookID: b.ID, FilePath: "/lib/r3/book/02.mp3"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetBookFiles(b.ID)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
}
