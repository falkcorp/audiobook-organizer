// file: internal/plugins/maintenance/orphan_book_files_test.go
// version: 1.3.0
// guid: 0bd4f9a2-1c3e-4f5a-8b6c-7d9e0f1a2b3c
// last-edited: 2026-08-13

package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestFindOrphanBookFiles_SoftDeletedBooksKeepTheirFiles is a data-loss guard.
//
// A soft-deleted book is in the trash, not gone — POST
// /api/v1/audiobooks/:id/restore brings it back. Its book_file rows must
// therefore NOT be reported as orphans, because callers pass the orphan set to
// DeleteBookFilesByIDs and a restore whose file rows were deleted underneath it
// restores an empty shell.
//
// This was accidentally safe until 2026-08-13: GetAllBooksCore's memdb
// implementation leaked soft-deleted rows, so they landed in the valid-owner
// set by way of a bug. Fixing that leak is what made the explicit
// ListSoftDeletedBooks union load-bearing. On prod at the time of the fix that
// was 3,953 books — the losers of the July dedup drain.
func TestFindOrphanBookFiles_SoftDeletedBooksKeepTheirFiles(t *testing.T) {
	live := []database.Book{{ID: "book-live-1", Title: "Live"}}
	yes := true
	trashed := []database.Book{
		{ID: "book-trashed-1", Title: "Trashed", MarkedForDeletion: &yes},
	}
	files := []database.BookFileCore{
		{ID: "f1", BookID: "book-live-1", FilePath: "/lib/live.m4b"},
		{ID: "f2", BookID: "book-trashed-1", FilePath: "/lib/trashed.m4b"},
		{ID: "f3", BookID: "book-ghost-9", FilePath: "/lib/really-orphan.m4b"},
	}

	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		// Models the FIXED contract: soft-deleted rows are excluded here.
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{live[0].Core()}, nil
		},
		ListSoftDeletedBooksFunc: func(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
			return trashed, nil
		},
	}

	orphans, _, _, err := findOrphanBookFiles(context.Background(), store)
	if err != nil {
		t.Fatalf("findOrphanBookFiles returned error: %v", err)
	}

	got := make(map[string]bool, len(orphans))
	for _, o := range orphans {
		got[o.ID] = true
	}
	if got["f2"] {
		t.Error("f2 belongs to a SOFT-DELETED book and was reported as an orphan; " +
			"its owner is restorable, so deleting this row destroys the restore target")
	}
	if got["f1"] {
		t.Error("f1 belongs to a LIVE book and must never be an orphan")
	}
	// Non-vacuity: the scan must still find genuine orphans, or a function that
	// returned nothing at all would pass the assertions above.
	if !got["f3"] {
		t.Error("f3 has no owning book at all and must still be reported as an orphan")
	}
}

// TestFindOrphanBookFiles_FailsClosedWhenSoftDeletedSetUnavailable pins the
// error-handling choice: without the soft-deleted set the scan cannot tell a
// restorable book's files from real garbage, and its caller DELETES what it
// returns. Failing open here would report every soft-deleted book's files as
// orphans on a transient read error.
func TestFindOrphanBookFiles_FailsClosedWhenSoftDeletedSetUnavailable(t *testing.T) {
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{{ID: "f1", BookID: "ghost"}}, nil
		},
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return nil, nil
		},
		ListSoftDeletedBooksFunc: func(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
			return nil, errors.New("pebble read failed")
		},
	}

	orphans, _, _, err := findOrphanBookFiles(context.Background(), store)
	if err == nil {
		t.Fatalf("expected an error when the soft-deleted set is unreadable, got %d orphans", len(orphans))
	}
	if orphans != nil {
		t.Errorf("expected no orphan list alongside the error, got %d", len(orphans))
	}
}

// TestFindOrphanBookFiles_ReportOnly verifies the core scan: given a mix of
// book_files where some BookIDs reference existing books and some don't, the
// function returns exactly the orphan rows without touching the database.
//
// This mirrors the G6 scenario where a partial merge or pre-existing data
// inconsistency leaves book_file rows pointing at a now-missing book_id.
func TestFindOrphanBookFiles_ReportOnly(t *testing.T) {
	// Three valid books, plus one "ghost" book ID that was deleted directly
	// (bypassing the normal cascade), simulating a partial merge.
	books := []database.Book{
		{ID: "book-keep-1", Title: "Kept 1"},
		{ID: "book-keep-2", Title: "Kept 2"},
		{ID: "book-keep-3", Title: "Kept 3"},
	}
	files := []database.BookFileCore{
		{ID: "f1", BookID: "book-keep-1", FilePath: "/lib/a.m4b"},
		{ID: "f2", BookID: "book-keep-2", FilePath: "/lib/b.m4b"},
		{ID: "f3", BookID: "book-ghost-9", FilePath: "/lib/orphan-1.m4b"}, // orphan
		{ID: "f4", BookID: "book-keep-3", FilePath: "/lib/c.m4b"},
		{ID: "f5", BookID: "book-ghost-9", FilePath: "/lib/orphan-2.m4b"}, // orphan
		{ID: "f6", BookID: "", FilePath: "/lib/orphan-empty.m4b"},         // empty-id orphan
	}

	var deleteCalls []string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		DeleteBookFileFunc: func(id string) error {
			deleteCalls = append(deleteCalls, id)
			return nil
		},
	}

	orphans, totalFiles, totalBooks, err := findOrphanBookFiles(context.Background(), store)
	if err != nil {
		t.Fatalf("findOrphanBookFiles returned error: %v", err)
	}
	if totalFiles != len(files) {
		t.Errorf("totalFiles = %d, want %d", totalFiles, len(files))
	}
	if totalBooks != len(books) {
		t.Errorf("totalBooks = %d, want %d", totalBooks, len(books))
	}
	if got, want := len(orphans), 3; got != want {
		t.Fatalf("len(orphans) = %d, want %d (orphans: %+v)", got, want, orphans)
	}

	// The exact orphan IDs should be f3, f5, f6.
	wantIDs := map[string]bool{"f3": true, "f5": true, "f6": true}
	for _, o := range orphans {
		if !wantIDs[o.ID] {
			t.Errorf("unexpected orphan id %q (book_id=%q)", o.ID, o.BookID)
		}
		delete(wantIDs, o.ID)
	}
	for missing := range wantIDs {
		t.Errorf("expected orphan id %q not returned", missing)
	}

	// Report-only mode: DeleteBookFile MUST NOT have been called.
	if len(deleteCalls) != 0 {
		t.Errorf("report-only scan called DeleteBookFile %d times: %v",
			len(deleteCalls), deleteCalls)
	}
}

// TestFindOrphanBookFiles_NoOrphans verifies the clean-library case returns an
// empty slice with no error.
func TestFindOrphanBookFiles_NoOrphans(t *testing.T) {
	books := []database.Book{{ID: "b1"}, {ID: "b2"}}
	files := []database.BookFileCore{
		{ID: "f1", BookID: "b1"},
		{ID: "f2", BookID: "b2"},
		{ID: "f3", BookID: "b1"},
	}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
	}
	orphans, _, _, err := findOrphanBookFiles(context.Background(), store)
	if err != nil {
		t.Fatalf("findOrphanBookFiles returned error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected no orphans, got %d", len(orphans))
	}
}
