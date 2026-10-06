// file: internal/plugins/maintenance/duration_backfill_counters_test.go
// version: 1.1.0
// guid: 01caa381-c1de-491b-b20f-5e348267cd4e
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// counterStore is a one-book MockStore for the summary-counter tests.
func counterStore(book database.Book, segs []database.BookFile) *database.MockStore {
	books := []database.Book{book}
	return &database.MockStore{
		CountAllBooksFunc:       func() (int, error) { return len(books), nil },
		GetAllBooksFullFromFunc: pageBooksFullFrom(books),
		GetBookFilesFunc:        func(string) ([]database.BookFile, error) { return segs, nil },
	}
}

// runCounterBackfill runs the op and returns its final summary line.
func runCounterBackfill(t *testing.T, store *database.MockStore, params map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	rep := &fakeReporter{}
	if err := New(fakeDeps{store: store}).runDurationBackfill(context.Background(), raw, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rep.logs) == 0 {
		t.Fatal("no summary logged")
	}
	return rep.logs[len(rep.logs)-1]
}

// A recently-verified skip is its own count. It used to be added to
// estimated-segments, so that number was mostly books nobody probed.
func TestDurationBackfill_RecentlyVerifiedHasOwnCounter(t *testing.T) {
	verified := time.Now().Add(-time.Hour)
	book := database.Book{ID: "b1", Duration: new(100), DurationVerifiedAt: &verified}
	summary := runCounterBackfill(t, counterStore(book, nil), map[string]any{"dry_run": true})
	assertSummaryHas(t, summary, "recently-verified-skipped=1", "estimated-segments=0",
		"missing-on-disk=0", "read-errors=0", "write-errors=0")
}

// A segment whose file is gone is missing-on-disk, with the path as the
// example, and is not a read error.
func TestDurationBackfill_MissingOnDiskSplitFromReadErrors(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone.mp3")
	pid := "pid-1"
	book := database.Book{ID: "b1", Duration: new(100), ITunesPersistentID: &pid}
	// iTunes-linked, so the stored-duration shortcut is off and the file is read.
	segs := []database.BookFile{{ID: "s1", BookID: "b1", FilePath: gone, Duration: 50, ITunesPersistentID: pid}}
	summary := runCounterBackfill(t, counterStore(book, segs), map[string]any{"dry_run": true})
	assertSummaryHas(t, summary, "missing-on-disk=1 (e.g. "+gone+")", "read-errors=0",
		"recently-verified-skipped=0", "write-errors=0")
}

// A file that exists but cannot be read is a read error, not missing-on-disk.
func TestDurationBackfill_UnreadableFileIsReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	locked := filepath.Join(t.TempDir(), "locked.mp3")
	if err := os.WriteFile(locked, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	pid := "pid-1"
	book := database.Book{ID: "b1", Duration: new(100), ITunesPersistentID: &pid}
	segs := []database.BookFile{{ID: "s1", BookID: "b1", FilePath: locked, Duration: 50, ITunesPersistentID: pid}}
	summary := runCounterBackfill(t, counterStore(book, segs), map[string]any{"dry_run": true})
	assertSummaryHas(t, summary, "read-errors=1 (e.g. "+locked+")", "missing-on-disk=0")
}

// A failed segment write is a write error; it used to be counted as a read error.
func TestDurationBackfill_WriteFailureIsWriteError(t *testing.T) {
	book := database.Book{ID: "b1", Duration: new(100)}
	segs := []database.BookFile{{ID: "s1", BookID: "b1", FilePath: "/x/01.mp3", Duration: 50, AcoustIDFingerprintDurationSec: 1800}}
	store := counterStore(book, segs)
	store.UpdateBookFilesFunc = func(context.Context, []*database.BookFile, func(int, bool)) (int, error) {
		return 0, errors.New("disk full")
	}
	summary := runCounterBackfill(t, store, map[string]any{"dry_run": false})
	assertSummaryHas(t, summary, "write-errors=1", "read-errors=0", "missing-on-disk=0")
}

// recordReadFailure keeps the FIRST example path of each kind and counts all.
func TestRecordReadFailure_ClassifiesAndKeepsFirstExample(t *testing.T) {
	res := bookProcessResult{book: database.Book{ID: "b1"}}
	lim := &readWarnLimiter{}
	recordReadFailure(&res, "/a", &os.PathError{Op: "open", Path: "/a", Err: os.ErrNotExist}, lim)
	recordReadFailure(&res, "/b", &os.PathError{Op: "open", Path: "/b", Err: os.ErrNotExist}, lim)
	recordReadFailure(&res, "/c", os.ErrPermission, lim)
	recordReadFailure(&res, "/d", nil, lim) // probe returned no duration
	if res.missingOnDisk != 2 || res.missingExample != "/a" {
		t.Errorf("missing: got %d/%q, want 2/\"/a\"", res.missingOnDisk, res.missingExample)
	}
	if res.readErrs != 2 || res.readErrExample != "/c" {
		t.Errorf("read errors: got %d/%q, want 2/\"/c\"", res.readErrs, res.readErrExample)
	}
}

// Warnings are capped per run but counting is not: every failure past the cap
// is still counted (and kept out of the log), and the limiter reports how many
// it refused. Missing-on-disk files log at debug and do not use the cap.
func TestReadWarnLimiter_CapsWarningsNotCounts(t *testing.T) {
	res := bookProcessResult{book: database.Book{ID: "b1"}}
	lim := &readWarnLimiter{}
	for range readWarnLimit + 5 {
		recordReadFailure(&res, "/c", os.ErrPermission, lim)
	}
	for range 3 {
		recordReadFailure(&res, "/m", os.ErrNotExist, lim)
	}
	if res.readErrs != readWarnLimit+5 {
		t.Errorf("readErrs = %d, want %d", res.readErrs, readWarnLimit+5)
	}
	if res.missingOnDisk != 3 {
		t.Errorf("missingOnDisk = %d, want 3", res.missingOnDisk)
	}
	if got := lim.suppressed(); got != 5 {
		t.Errorf("suppressed = %d, want 5", got)
	}
}
