// file: internal/plugins/maintenance/orphan_book_files_test.go
// version: 1.2.0
// guid: 0bd4f9a2-1c3e-4f5a-8b6c-7d9e0f1a2b3c
// last-edited: 2026-07-07

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

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
