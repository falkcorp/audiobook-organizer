// file: internal/plugins/maintenance/orphan_book_files_repoint_plan_test.go
// version: 1.0.0
// guid: 7830431a-11dc-4bee-97c0-82047798cb98
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The repoint plan is read-only and classifies each orphan row by the
// strongest evidence available. Orphans are made the way production made
// them — a file row whose book row is gone — by writing the file row for a
// book ID that has no row (DeleteBook now refuses to create them).
func TestPlanOrphanRepoints_ClassifiesAndNeverWrites(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A live directory book: an orphan row inside its directory has one owner.
	dirBook, err := store.CreateBook(&database.Book{Title: "Dir", FilePath: "/lib/A/Book"})
	if err != nil {
		t.Fatal(err)
	}
	// A live book owning /lib/B/x.m4b: an orphan row for the same path is a
	// duplicate reference.
	owner, err := store.CreateBook(&database.Book{Title: "Owner", FilePath: "/lib/B/x.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "owned", BookID: owner.ID, FilePath: "/lib/B/x.m4b"}); err != nil {
		t.Fatal(err)
	}
	// Merge survivor recorded on a tombstone of the missing book.
	survivor, err := store.CreateBook(&database.Book{Title: "Survivor", FilePath: "/lib/S/s.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	sid := survivor.ID
	if err := store.CreateBookTombstone(&database.Book{ID: "ghost-merged", Title: "gone", MergedIntoBookID: &sid}); err != nil {
		t.Fatal(err)
	}

	orphans := []database.BookFileCore{
		{ID: "o-dir", BookID: "ghost-1", FilePath: "/lib/A/Book/01.mp3"},
		{ID: "o-dup", BookID: "ghost-2", FilePath: "/lib/B/x.m4b"},
		{ID: "o-merge", BookID: "ghost-merged", FilePath: "/elsewhere/m.mp3"},
		{ID: "o-none", BookID: "ghost-3", FilePath: "/nowhere/n.mp3"},
	}
	plan, err := planOrphanRepoints(context.Background(), store, orphans)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ class, target string }{
		"o-dir":   {orphanClassPathOwner, dirBook.ID},
		"o-dup":   {orphanClassDuplicate, ""},
		"o-merge": {orphanClassMergeSurvivor, survivor.ID},
		"o-none":  {orphanClassUnresolved, ""},
	}
	if len(plan.Items) != len(want) {
		t.Fatalf("items = %d, want %d: %+v", len(plan.Items), len(want), plan.Items)
	}
	for _, it := range plan.Items {
		w := want[it.BookFileID]
		if it.Class != w.class || it.TargetBookID != w.target {
			t.Errorf("%s: class=%s target=%s, want %s/%s (%s)", it.BookFileID, it.Class, it.TargetBookID, w.class, w.target, it.Detail)
		}
	}
	if plan.PlannedRepoints != 2 || plan.OrphanBookIDs != 4 {
		t.Errorf("planned=%d orphanBooks=%d, want 2/4", plan.PlannedRepoints, plan.OrphanBookIDs)
	}
	// Read-only: the owner's row is untouched and nothing was moved to it.
	rows, err := store.GetBookFiles(owner.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("owner rows = %d (%v), want 1", len(rows), err)
	}
	if rows, _ := store.GetBookFiles(dirBook.ID); len(rows) != 0 {
		t.Errorf("dry run moved %d row(s) onto %s", len(rows), dirBook.ID)
	}
}
