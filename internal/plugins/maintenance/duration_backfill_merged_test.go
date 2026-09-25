// file: internal/plugins/maintenance/duration_backfill_merged_test.go
// version: 1.2.0
// guid: 9b41e7c3-2d58-4a06-bf19-6e35c0d7a284
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// bookWithSegs builds a one-book store whose segments are the given rows, and
// records segment writes and book writes separately. A book write means either
// the total was rewritten or DurationVerifiedAt was stamped — both are things a
// book with an unresolved segment must not get.
func bookWithSegs(t *testing.T, segs []database.BookFile, segWrites, bookWrites *int) *database.MockStore {
	t.Helper()
	books := []database.Book{{ID: "b1", Title: "B", FilePath: "/lib/B", Duration: new(120)}}
	return &database.MockStore{
		CountAllBooksFunc:       func() (int, error) { return len(books), nil },
		GetAllBooksFullFromFunc: pageBooksFullFrom(books),
		GetAllBooksFunc: func(_, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			return books, nil
		},
		GetBookFilesFunc:   func(string) ([]database.BookFile, error) { return segs, nil },
		UpdateBookFileFunc: func(_ string, _ *database.BookFile) error { *segWrites++; return nil },
		UpdateBookFilesFunc: func(_ context.Context, files []*database.BookFile, after func(int, bool)) (int, error) {
			for i := range files {
				*segWrites++
				if after != nil {
					after(i, true)
				}
			}
			return len(files), nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) { *bookWrites++; return b, nil },
		ModifyBookFunc: func(_ string, fn func(*database.Book) error) (*database.Book, error) {
			*bookWrites++
			b := books[0]
			if err := fn(&b); err != nil {
				return nil, err
			}
			return &b, nil
		},
	}
}

// TestDurationBackfill_CorrectsMillisecondSegment pins the check folded in from
// the retired maintenance.duration-backfill / maintenance.purge-millisecond-durations
// ops: a stored duration that is millisecond-valued is divided by 1000 as part
// of the ordinary duration pass, not by a separate operation.
//
// 3600 s at 64 kbps is 28,800,000 bytes. Reading 3,600,000 as SECONDS against
// that size implies ~0.064 kbps, which is impossible, so it is milliseconds.
func TestDurationBackfill_CorrectsMillisecondSegment(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{{
		ID: "s1", BookID: "b1",
		FilePath: "/lib/B/01.m4b",
		FileSize: bytesForBitrate(3600, 64),
		Duration: 3600000, // milliseconds, never divided
	}}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)

	var got int
	store.UpdateBookFilesFunc = func(_ context.Context, files []*database.BookFile, after func(int, bool)) (int, error) {
		for i := range files {
			segWrites++
			got = files[i].Duration
			if after != nil {
				after(i, true)
			}
		}
		return len(files), nil
	}

	p := New(fakeDeps{store: store})
	if err := p.runDurationBackfill(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 3600 {
		t.Errorf("millisecond-valued segment corrected to %d, want 3600", got)
	}
}

// TestDurationBackfill_EmptyPathSegmentIsUnresolved pins the fix for the silent
// short-total bug. A segment with an empty FilePath used to `continue`,
// contributing 0 to the book total while leaving the book looking fully
// resolved — so the book was written SHORT by exactly that segment and then
// stamped DurationVerifiedAt, which hid the wrong number from the next run for
// SkipAgeDays. An empty path is missing evidence, not a zero-length file.
func TestDurationBackfill_EmptyPathSegmentIsUnresolved(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{
		{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 50, AcoustIDFingerprintDurationSec: 1800.0},
		{ID: "s2", BookID: "b1", FilePath: "", Duration: 0}, // no path: unresolved
	}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)

	p := New(fakeDeps{store: store})
	if err := p.runDurationBackfill(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if segWrites != 1 {
		t.Errorf("the resolvable segment must still be corrected: segment writes = %d, want 1", segWrites)
	}
	if bookWrites != 0 {
		t.Errorf("a book with an empty-path segment must not have its total written or be sealed: book writes = %d, want 0", bookWrites)
	}
}

// TestDurationBackfill_CompleteBookIsWritten is the control for the two tests
// above: with every segment resolved, the total IS written and the book IS
// sealed. Without this, "withhold the total" could pass by never writing
// anything at all.
func TestDurationBackfill_CompleteBookIsWritten(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{
		{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 50, AcoustIDFingerprintDurationSec: 1800.0},
		{ID: "s2", BookID: "b1", FilePath: "/lib/B/02.m4b", Duration: 60, AcoustIDFingerprintDurationSec: 1200.0},
	}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)

	p := New(fakeDeps{store: store})
	if err := p.runDurationBackfill(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if segWrites != 2 {
		t.Errorf("both drifted segments must be corrected: segment writes = %d, want 2", segWrites)
	}
	if bookWrites == 0 {
		t.Error("a fully resolved book must have its total written and be stamped verified, got no book writes")
	}
}

// TestDurationBackfill_BookIDsReadsOnlyThoseBooks: with book_ids the op reads
// the named books by ID and never walks the library, and a file row stored as
// 0 under a correct book total is still corrected (the ABS "duration 0" case).
func TestDurationBackfill_BookIDsReadsOnlyThoseBooks(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 0, AcoustIDFingerprintDurationSec: 120.0}}
	store := bookWithSegs(t, segs, &segWrites, &bookWrites)
	store.GetAllBooksFullFromFunc = nil
	store.GetAllBooksFunc = func(int, int) ([]database.Book, error) {
		t.Fatal("book_ids run must not walk the library")
		return nil, nil
	}
	store.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id != "b1" {
			return nil, nil
		}
		return &database.Book{ID: "b1", Title: "B", FilePath: "/lib/B", Duration: new(120)}, nil
	}
	raw, _ := json.Marshal(map[string]any{"dry_run": false, "force": true, "book_ids": []string{"b1"}})
	p := New(fakeDeps{store: store})
	if err := p.runDurationBackfill(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if segWrites != 1 {
		t.Errorf("the zero-duration file row must be corrected: segment writes = %d, want 1", segWrites)
	}
}

// zeroRowsStore is bookWithSegs for one book with the given visibility.
func zeroRowsStore(t *testing.T, segs []database.BookFile, primary bool, state string, segWrites, bookWrites *int) *database.MockStore {
	t.Helper()
	store := bookWithSegs(t, segs, segWrites, bookWrites)
	book := database.Book{ID: "b1", Title: "B", FilePath: "/lib/B", Duration: new(3000),
		IsPrimaryVersion: &primary, LibraryState: &state, DurationVerifiedAt: new(time.Now())}
	store.GetAllBooksFullFromFunc = pageBooksFullFrom([]database.Book{book})
	return store
}

func runZeroRows(t *testing.T, store *database.MockStore) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"dry_run": false, "zero_rows_only": true})
	if err := New(fakeDeps{store: store}).runDurationBackfill(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// Zero-rows mode fills a row stored as 0 on a visible single-folder book,
// even though the book was stamped verified, and leaves a drifted non-zero
// row alone.
func TestDurationBackfill_ZeroRowsFillsOnlyZeroRows(t *testing.T) {
	var segWrites, bookWrites int
	var written []string
	segs := []database.BookFile{
		{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 0, AcoustIDFingerprintDurationSec: 1800.0},
		{ID: "s2", BookID: "b1", FilePath: "/lib/B/02.m4b", Duration: 50, AcoustIDFingerprintDurationSec: 1200.0},
	}
	store := zeroRowsStore(t, segs, true, "organized", &segWrites, &bookWrites)
	store.UpdateBookFilesFunc = func(_ context.Context, files []*database.BookFile, after func(int, bool)) (int, error) {
		for _, f := range files {
			written = append(written, f.ID)
		}
		return len(files), nil
	}
	runZeroRows(t, store)
	if len(written) != 1 || written[0] != "s1" {
		t.Errorf("zero-rows mode must write only the zero row s1, wrote %v", written)
	}
}

// A book whose rows span two folders (duplicate rows) is skipped: summing
// them after the write is what inflated totals in the full run.
func TestDurationBackfill_ZeroRowsSkipsMultiDirBooks(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{
		{ID: "s1", BookID: "b1", FilePath: "/itunes/B/01.m4b", Duration: 0, AcoustIDFingerprintDurationSec: 1800.0},
		{ID: "s2", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 1800, AcoustIDFingerprintDurationSec: 1800.0},
	}
	runZeroRows(t, zeroRowsStore(t, segs, true, "organized", &segWrites, &bookWrites))
	if segWrites != 0 || bookWrites != 0 {
		t.Errorf("multi-dir book must be skipped: segment writes=%d book writes=%d", segWrites, bookWrites)
	}
}

// A book ABS does not list (not primary) is out of scope.
func TestDurationBackfill_ZeroRowsIgnoresHiddenBooks(t *testing.T) {
	var segWrites, bookWrites int
	segs := []database.BookFile{{ID: "s1", BookID: "b1", FilePath: "/lib/B/01.m4b", Duration: 0, AcoustIDFingerprintDurationSec: 1800.0}}
	runZeroRows(t, zeroRowsStore(t, segs, false, "organized", &segWrites, &bookWrites))
	if segWrites != 0 {
		t.Errorf("non-primary book must not be touched: segment writes=%d", segWrites)
	}
}
