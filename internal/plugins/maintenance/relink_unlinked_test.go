// file: internal/plugins/maintenance/relink_unlinked_test.go
// version: 1.1.0
// guid: 2f6a41d8-90b3-4c57-8e12-b5d70c3a9f46
// last-edited: 2026-08-24

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
)

// TestRelinkOne_Directory_CreatesEveryTrackAndAggregatesOnce covers relinkOne's
// multi-file path, which had no test at all before the batch conversion.
//
// It creates one book_file per audio file in the folder and, because those rows
// are now written as one batch, re-adds the book's totals once rather than once
// per row. The book-level FileSize is the observable: it must equal the sum of
// the rows, which only holds if the recompute ran after the batch committed.
//
// Duration is deliberately NOT asserted as non-zero. This path leaves per-file
// duration at 0 on purpose — seeding the book's total onto every track would
// multiply the runtime by the track count — so maintenance.duration-backfill
// fills it in later.
func TestRelinkOne_Directory_CreatesEveryTrackAndAggregatesOnce(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	files := []string{"Chapter 01.mp3", "Chapter 02.mp3", "Chapter 03.mp3"}
	dir := makeFolder(t, files...)
	bk, err := s.CreateBook(&database.Book{Title: "Unlinked Folder Book", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if bk.FileSize != nil {
		t.Fatalf("precondition: a freshly created book carries no FileSize, got %d", *bk.FileSize)
	}

	// Armed immediately before the call so only relinkOne's own recomputes are
	// counted, not anything CreateBook did during setup.
	readLogs := aggtest.Capture(t)

	n, err := relinkOne(s, linkintegrity.Finding{
		BookID:   bk.ID,
		FilePath: dir,
		Shape:    linkintegrity.ShapeDirectory,
	})
	if err != nil {
		t.Fatalf("relinkOne: %v", err)
	}

	// THE ASSERTION THIS TEST IS NAMED FOR. Everything below checks that the
	// aggregate is CORRECT; only this checks that it was computed ONCE.
	//
	// Those are different claims, and this test asserted only the first one until
	// 2026-08-24 while being named ...AndAggregatesOnce. The final FileSize equals
	// the sum of the rows whether the recompute ran once or once per row — the last
	// one of N produces exactly the totals the only one of 1 does — so the
	// three-per-row version of this loop passed it. Measured: 3 invocations, green.
	//
	// Counted as invocations rather than writes for the same reason: only the first
	// recompute finds anything to change, so a per-row regression still emits a
	// single "updated" line. See aggtest.CountInvocations.
	if got := aggtest.CountInvocations(readLogs(), bk.ID); got != 1 {
		t.Fatalf("RecomputeBookAggregates ran %d times for %d files, want exactly 1; "+
			"the batch write is recomputing per row again", got, len(files))
	}
	if n != len(files) {
		t.Fatalf("created = %d, want %d", n, len(files))
	}

	stored, err := s.GetBookFiles(bk.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(stored) != len(files) {
		t.Fatalf("stored rows = %d, want %d", len(stored), len(files))
	}

	var wantSize int64
	for _, f := range stored {
		wantSize += f.FileSize
	}
	if wantSize == 0 {
		t.Fatal("fixture produced zero total size; the aggregate assertion would be vacuous")
	}

	got, err := s.GetBookByID(bk.ID)
	if err != nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if got.FileSize == nil {
		t.Fatal("book FileSize was never recomputed after the batch create")
	}
	if *got.FileSize != wantSize {
		t.Fatalf("book FileSize = %d, want %d (sum of its rows)", *got.FileSize, wantSize)
	}
}

// TestRelinkOne_Directory_SkipsABookThatAlreadyOwnsFiles pins the guard that makes
// create-without-matching safe here.
//
// BatchCreateBookFiles never looks for an existing row, so relinkOne is only
// correct because it re-reads the book's files under the write path and returns
// early. Remove that check and re-running the repair silently doubles every row.
func TestRelinkOne_Directory_SkipsABookThatAlreadyOwnsFiles(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	files := []string{"Chapter 01.mp3", "Chapter 02.mp3"}
	dir := makeFolder(t, files...)
	bk, err := s.CreateBook(&database.Book{Title: "Already Linked", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	finding := linkintegrity.Finding{BookID: bk.ID, FilePath: dir, Shape: linkintegrity.ShapeDirectory}
	if _, err := relinkOne(s, finding); err != nil {
		t.Fatalf("first relinkOne: %v", err)
	}

	// Second run: the book now owns rows, so nothing more may be created.
	n, err := relinkOne(s, finding)
	if err != nil {
		t.Fatalf("second relinkOne: %v", err)
	}
	if n != 0 {
		t.Fatalf("second run created = %d, want 0", n)
	}

	stored, err := s.GetBookFiles(bk.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(stored) != len(files) {
		t.Fatalf("stored rows = %d, want %d — re-running the repair duplicated rows", len(stored), len(files))
	}
}
