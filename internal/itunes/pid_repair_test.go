// file: internal/itunes/pid_repair_test.go
// version: 1.0.0
// guid: 3e7b0a94-6c21-4d58-8f39-2a1c7e5b0d64
// last-edited: 2026-07-23

package itunes

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newRepairTestStore returns a real PebbleStore backed by a temp dir — the repair
// path exercises UpdateBookFile + the book_file_pid index, so a real store (not a
// mock) is required.
func newRepairTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestPickDiffFileKeeper verifies keep-by-ITL-location, incl. the fail-safe when
// the ITL can't disambiguate (no match, or two owners canonicalize to the track
// location). Pure function — no .itl needed.
func TestPickDiffFileKeeper(t *testing.T) {
	mappings := []PathMapping{{From: "W:", To: "/mnt/bigdata/books"}}
	aoPath := "/mnt/bigdata/books/audiobook-organizer/Author/Book/01.m4b"
	itPath := "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Author/01.m4b"
	owners := []database.BookFile{
		{ID: "f_it", FilePath: itPath, ITunesPersistentID: "PID"},
		{ID: "f_ao", FilePath: aoPath, ITunesPersistentID: "PID"},
	}

	// Track sits at the AO copy (post-relocate) → keep the AO owner (index 1).
	aoLoc, ok := canonicalWinLocationForFile(aoPath, "PID", "t", mappings)
	if !ok {
		t.Fatal("aoPath should canonicalize")
	}
	if idx, ok := pickDiffFileKeeper(owners, aoLoc, mappings); !ok || idx != 1 {
		t.Errorf("keep-by-location: got idx=%d ok=%v, want idx=1 ok=true", idx, ok)
	}

	// Track location matches NO owner → ambiguous → leave untouched.
	if _, ok := pickDiffFileKeeper(owners, `W:\somewhere\else.m4b`, mappings); ok {
		t.Error("no-match should be non-ok (leave for review)")
	}
	// Empty track location → non-ok.
	if _, ok := pickDiffFileKeeper(owners, "", mappings); ok {
		t.Error("empty track location should be non-ok")
	}
}

// TestApplyPIDRepairSameFile exercises the common (97.5%) same_file case end to
// end against a real store: two rows carry one PID; after repair exactly one row
// keeps it and the index points there — no row/file deleted.
func TestApplyPIDRepairSameFile(t *testing.T) {
	store := newRepairTestStore(t)

	primary := true
	b1, _ := store.CreateBook(&database.Book{Title: "B1", FilePath: "/x/b1", IsPrimaryVersion: &primary})
	b2, _ := store.CreateBook(&database.Book{Title: "B2", FilePath: "/x/b2", IsPrimaryVersion: &primary})

	const pid = "DEADBEEF00000001"
	const path = "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Author/track.m4b"

	// Reproduce the anomaly the way history did: the forward guard blocks a second
	// CreateBookFile with the same PID, so set the dup via UpdateBookFile (no guard).
	f1 := &database.BookFile{BookID: b1.ID, FilePath: path, ITunesPersistentID: pid}
	if err := store.CreateBookFile(f1); err != nil {
		t.Fatal(err)
	}
	f2 := &database.BookFile{BookID: b2.ID, FilePath: path}
	if err := store.CreateBookFile(f2); err != nil {
		t.Fatal(err)
	}
	f2.ITunesPersistentID = pid
	if err := store.UpdateBookFile(f2.ID, f2); err != nil {
		t.Fatal(err)
	}

	groups, preview, err := ComputePIDRepairPlan(store, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview.DuplicatePIDs != 1 || preview.SameFileGroups != 1 || preview.FilesToClear != 1 {
		t.Fatalf("preview = %+v, want 1 dup / 1 same_file / 1 to-clear", preview)
	}

	if _, err := ApplyPIDRepairPlan(store, groups); err != nil {
		t.Fatal(err)
	}

	// Exactly one row keeps the PID, and the index resolves to it.
	got, err := store.GetBookFileByPID(pid)
	if err != nil || got == nil {
		t.Fatalf("GetBookFileByPID after repair: %v (nil=%v)", err, got == nil)
	}
	withPID := 0
	for _, bid := range []string{b1.ID, b2.ID} {
		files, _ := store.GetBookFiles(bid)
		for i := range files {
			if files[i].ITunesPersistentID == pid {
				withPID++
			}
			// The audio path is never touched.
			if files[i].FilePath != path {
				t.Errorf("file path mutated: %q", files[i].FilePath)
			}
		}
	}
	if withPID != 1 {
		t.Errorf("rows still carrying the PID = %d, want 1", withPID)
	}
}
