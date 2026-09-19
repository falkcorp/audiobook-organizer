// file: internal/plugins/maintenance/orphan_book_files_repoint_plan_test.go
// version: 1.1.0
// guid: 7830431a-11dc-4bee-97c0-82047798cb98
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newPlanStore(t *testing.T, buildAtPath bool) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	if buildAtPath {
		if _, err := store.BackfillBookAtPathIndex(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// The repoint plan is read-only and classifies each orphan row by the
// strongest evidence available. The orphans are passed in the way
// findOrphanBookFiles hands them over; the store can no longer be made to
// hold a real one (DeleteBook and CreateBookFile both refuse), and the
// planner never re-reads the orphan's own row.
func TestPlanOrphanRepoints_ClassifiesAndNeverWrites(t *testing.T) {
	store := newPlanStore(t, true)

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

// The single-valued book_file_path index can name the ORPHAN itself while a
// live book's row for the same file exists. The old duplicate check read only
// that index, skipped the orphan's own hit, fell through to the path-owner
// step and planned a repoint that would hand the live book a second row for
// a file it already owns.
func TestPlanOrphanRepoints_DuplicateSeenWhenIndexNamesTheOrphan(t *testing.T) {
	store := newPlanStore(t, true)
	live, err := store.CreateBook(&database.Book{Title: "Live", FilePath: "/lib/D"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "live-row", BookID: live.ID, FilePath: "/lib/D/01.mp3"}); err != nil {
		t.Fatal(err)
	}
	// Stands in for the missing book: its row is written LAST, so the
	// single-valued path index now names it, not live-row.
	ghost, err := store.CreateBook(&database.Book{Title: "Ghost", FilePath: "/elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	orphan := &database.BookFile{ID: "orphan-row", BookID: ghost.ID, FilePath: "/lib/D/01.mp3"}
	if err := store.CreateBookFile(orphan); err != nil {
		t.Fatal(err)
	}
	if hit, _ := store.GetBookFileByPath("/lib/D/01.mp3"); hit == nil || hit.ID != "orphan-row" {
		t.Fatalf("fixture is vacuous: single index names %+v, want orphan-row", hit)
	}

	plan, err := planOrphanRepoints(context.Background(), store, []database.BookFileCore{
		{ID: "orphan-row", BookID: ghost.ID, FilePath: "/lib/D/01.mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if it := plan.Items[0]; it.Class != orphanClassDuplicate || it.TargetBookID != "" {
		t.Fatalf("item = %+v, want %s with no target", it, orphanClassDuplicate)
	}
}

// Two orphan rows for the same file must not both be planned onto one book.
func TestPlanOrphanRepoints_SameFileTwiceIsPlannedOnce(t *testing.T) {
	store := newPlanStore(t, true)
	dir, err := store.CreateBook(&database.Book{Title: "Dir", FilePath: "/lib/E"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planOrphanRepoints(context.Background(), store, []database.BookFileCore{
		{ID: "o1", BookID: "ghost-a", FilePath: "/lib/E/01.mp3"},
		{ID: "o2", BookID: "ghost-b", FilePath: "/lib/E/01.mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PlannedRepoints != 1 || plan.ByClass[orphanClassDupOrphan] != 1 {
		t.Fatalf("plan = %+v, want 1 repoint onto %s and 1 duplicate_of_orphan", plan, dir.ID)
	}
}

// With the book_atpath index unbuilt the path-owner step is skipped, and says
// so, instead of scanning every book row per orphan.
func TestPlanOrphanRepoints_SkipsPathOwnerWhenAtPathIndexUnbuilt(t *testing.T) {
	store := newPlanStore(t, false)
	if built, _ := store.BookAtPathIndexBuilt(); built {
		t.Skip("store builds the book_atpath index at open; nothing to test")
	}
	if _, err := store.CreateBook(&database.Book{Title: "Dir", FilePath: "/lib/F"}); err != nil {
		t.Fatal(err)
	}
	plan, err := planOrphanRepoints(context.Background(), store, []database.BookFileCore{
		{ID: "o1", BookID: "ghost", FilePath: "/lib/F/01.mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PathOwnerSkipped == "" || plan.Items[0].Class != orphanClassUnresolved {
		t.Fatalf("plan = %+v, want path-owner skipped and the row unresolved", plan)
	}
}
