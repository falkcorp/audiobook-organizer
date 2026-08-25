// file: internal/plugins/maintenance/relink_unlinked_test.go
// version: 1.1.0
// guid: 2f6a41d8-90b3-4c57-8e12-b5d70c3a9f46
// last-edited: 2026-08-24

package maintenance

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
)

// TestRelinkOne_Directory_CreatesEveryTrackAndSumsTheBook covers relinkOne's
// multi-file path, which had no test at all before the batch conversion.
//
// RENAMED 2026-08-24. This was called "...AndAggregatesOnce", which it never
// checked — MEASURED: it passes unchanged against a per-row implementation that
// recomputes three times. The name asserted a property the body did not test,
// which is worse than no test, because it reads as covered. The "once" claim now
// lives in TestRelinkOne_Directory_UsesTheBatchPath, which fails on that mutant.
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
func TestRelinkOne_Directory_CreatesEveryTrackAndSumsTheBook(t *testing.T) {
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

// TestRelinkOne_Directory_UsesTheBatchPath pins the thing this lane exists to
// protect, and that the two tests above do NOT protect.
//
// MEASURED: reverting relinkOne's directory branch to the old per-row
// createBookFileFor loop leaves BOTH of the tests above green. They assert the
// rows exist and that the book's FileSize equals their sum — and a per-row loop
// produces exactly the same rows and exactly the same sum, just at 3 aggregate
// recomputes instead of 1. The test literally named "...AndAggregatesOnce" never
// checked "once". The only observable that separates the two implementations is
// WHICH store method the call site reaches, so that is what this asserts.
//
// ⚠️ BatchCreateBookFilesFunc MUST be set. database.MockStore's
// BatchCreateBookFiles falls back to looping CreateBookFileFunc per row when the
// batch hook is nil — the mock IMPLEMENTS the regression shape — so a version of
// this test that left it unset would be blind by construction and pass either way.
func TestRelinkOne_Directory_UsesTheBatchPath(t *testing.T) {
	files := []string{"Chapter 01.mp3", "Chapter 02.mp3", "Chapter 03.mp3"}
	dir := makeFolder(t, files...)

	var batches [][]*database.BookFile
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Batched Book", FilePath: dir}, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
		CreateBookFileFunc: func(*database.BookFile) error {
			t.Error("relinkOne called CreateBookFile per row — the aggregate coalescing regressed")
			return nil
		},
		BatchCreateBookFilesFunc: func(f []*database.BookFile) error {
			batches = append(batches, f)
			return nil
		},
	}

	n, err := relinkOne(store, linkintegrity.Finding{
		BookID: "book-1", FilePath: dir, Shape: linkintegrity.ShapeDirectory,
	})
	if err != nil {
		t.Fatalf("relinkOne: %v", err)
	}
	if n != len(files) {
		t.Fatalf("created = %d, want %d", n, len(files))
	}
	if len(batches) != 1 {
		t.Fatalf("BatchCreateBookFiles called %d times, want exactly 1 for one book", len(batches))
	}
	if len(batches[0]) != len(files) {
		t.Fatalf("batch carried %d rows, want %d — every track must go in the SAME batch",
			len(batches[0]), len(files))
	}
}

// TestRelinkOne_Directory_ReportsZeroWhenTheBatchFails pins behaviour this lane
// newly introduced and left untested.
//
// The old per-row loop could return a partial count. The batch is atomic, so a
// failure means NOTHING was written and the honest answer is 0 — the count feeds
// the op's reported repair total, so returning len(bfs) here would inflate it
// with rows that do not exist.
func TestRelinkOne_Directory_ReportsZeroWhenTheBatchFails(t *testing.T) {
	dir := makeFolder(t, "Chapter 01.mp3", "Chapter 02.mp3")

	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Doomed", FilePath: dir}, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
		CreateBookFileFunc: func(*database.BookFile) error {
			t.Error("relinkOne fell back to per-row creation on batch failure")
			return nil
		},
		BatchCreateBookFilesFunc: func([]*database.BookFile) error {
			return errors.New("injected batch failure")
		},
	}

	n, err := relinkOne(store, linkintegrity.Finding{
		BookID: "book-1", FilePath: dir, Shape: linkintegrity.ShapeDirectory,
	})
	if err == nil {
		t.Fatal("a failed batch must surface as an error")
	}
	if n != 0 {
		t.Fatalf("created = %d, want 0 — the batch is atomic, so nothing was written", n)
	}
}
