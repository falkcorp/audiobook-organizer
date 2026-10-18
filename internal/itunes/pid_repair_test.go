// file: internal/itunes/pid_repair_test.go
// version: 1.3.0
// guid: 3e7b0a94-6c21-4d58-8f39-2a1c7e5b0d64
// last-edited: 2026-10-18

package itunes

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newRepairTestStore returns a real PebbleStore backed by a temp dir — the plan
// reads the book_file rows and the book_file_pid index, so a real store (not a
// mock) is required.
func newRepairTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	// Required, not optional — see PebbleStore.WaitForWarmup. Writes made while
	// the async warmup is still snapshotting have their memdb write-through
	// dropped, and the published memdb then omits them. Without this the repair
	// tests read back a book_file that is in Pebble but missing from memdb.
	store.WaitForWarmup()
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
	aoLoc, ok := canonicalWinLocationForFile(aoPath, "PID", mappings)
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

// TestComputePIDRepairPlanSameFile exercises the common (97.5%) same_file case
// end to end against a real store: two rows carry one PID; the plan keeps one
// row, lists the other as the row to clear, and writes nothing (the plan is
// read-only, so both rows still carry the PID afterwards).
func TestComputePIDRepairPlanSameFile(t *testing.T) {
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
	if len(groups) != 1 || len(groups[0].losers) != 1 {
		t.Fatalf("groups = %+v, want one group with one loser", groups)
	}
	g := groups[0]
	if g.pid != pid || g.keep.ID == g.losers[0].file.ID {
		t.Errorf("group = pid %q keep %q loser %q, want pid %q and distinct keeper and loser",
			g.pid, g.keep.ID, g.losers[0].file.ID, pid)
	}
	if ids := map[string]bool{f1.ID: true, f2.ID: true}; !ids[g.keep.ID] || !ids[g.losers[0].file.ID] {
		t.Errorf("keeper %q / loser %q are not the two seeded rows", g.keep.ID, g.losers[0].file.ID)
	}

	// Planning writes nothing: both rows still carry the PID, paths untouched.
	for _, bid := range []string{b1.ID, b2.ID} {
		files, _ := store.GetBookFiles(bid)
		for i := range files {
			if files[i].ITunesPersistentID != pid {
				t.Errorf("planning cleared the PID on %s", files[i].ID)
			}
			if files[i].FilePath != path {
				t.Errorf("file path mutated: %q", files[i].FilePath)
			}
		}
	}
}
