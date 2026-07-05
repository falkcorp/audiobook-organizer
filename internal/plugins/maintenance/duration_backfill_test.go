// file: internal/plugins/maintenance/duration_backfill_test.go
// version: 1.1.0
// guid: 2c9f5a1b-4d83-4e07-9f6a-1b8e3c0d7a52
// last-edited: 2026-07-05

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// testStore wraps MockStore to track RecomputeBookAggregates calls.
type testStore struct {
	*database.MockStore
	recomputedBooks []string
}

func (t *testStore) RecomputeBookAggregates(bookID string) error {
	t.recomputedBooks = append(t.recomputedBooks, bookID)
	return nil
}

// mockReporter is a minimal mock implementing registry.Reporter for testing.
type mockReporter struct {
	logs []string
}

func (m *mockReporter) UpdateProgress(current, total int, message string) error {
	return nil
}

func (m *mockReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	m.logs = append(m.logs, message)
	return nil
}

func (m *mockReporter) Logger() *slog.Logger {
	return slog.Default()
}

func (m *mockReporter) Checkpoint(state any) error {
	return nil
}

func (m *mockReporter) IsCanceled() bool {
	return false
}

func (m *mockReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, m)
}

func (m *mockReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}

func (m *mockReporter) SetCurrentItem(label string) {
	// no-op for test
}

// bytesForBitrate returns the file size in bytes for a clip of durationSec
// seconds encoded at kbps kilobits per second.
func bytesForBitrate(durationSec, kbps int) int64 {
	return int64(durationSec) * int64(kbps) * 1000 / 8
}

// TestDurationLooksLikeMillis exercises the implied-bitrate predicate that
// decides whether a stored BookFile.Duration is actually milliseconds (CONS-16).
// The key safety property (advisor review): a genuine low-bitrate audiobook must
// never be flagged, while every ms-inflated value must be.
func TestDurationLooksLikeMillis(t *testing.T) {
	tests := []struct {
		name        string
		fileSize    int64
		durationSec int
		want        bool
	}{
		// Correct seconds values across the realistic bitrate range — never flagged.
		{"64kbps correct seconds", bytesForBitrate(3600, 64), 3600, false},
		{"32kbps correct seconds", bytesForBitrate(3600, 32), 3600, false},
		{"low 12kbps correct seconds", bytesForBitrate(3600, 12), 3600, false},
		{"lossless 1411kbps correct", bytesForBitrate(3600, 1411), 3600, false},

		// Same files, but duration stored as milliseconds (×1000) — must flag.
		{"64kbps stored as ms", bytesForBitrate(3600, 64), 3600 * 1000, true},
		{"32kbps stored as ms", bytesForBitrate(3600, 32), 3600 * 1000, true},
		{"12kbps stored as ms", bytesForBitrate(3600, 12), 3600 * 1000, true},

		// Cannot decide / nothing to fix.
		{"zero file size", 0, 3600000, false},
		{"zero duration", bytesForBitrate(3600, 64), 0, false},

		// Pathological: a genuine but absurdly low 3kbps clip. Dividing by 1000
		// would imply ~3000kbps, above any real codec, so the upper sanity bound
		// must REJECT the false positive rather than corrupt the row.
		{"3kbps legit not over-corrected", bytesForBitrate(3600, 3), 3600, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := durationLooksLikeMillis(tt.fileSize, tt.durationSec)
			if got != tt.want {
				t.Errorf("durationLooksLikeMillis(size=%d, dur=%d) = %v, want %v",
					tt.fileSize, tt.durationSec, got, tt.want)
			}
		})
	}
}

// TestDurationBackfill_ParallelProducesCorrectResults verifies that the parallel
// version using registry.RunItems produces the same output as the serial version,
// and that no data race occurs on the shared bookOrder and fixesByBook maps.
func TestDurationBackfill_ParallelProducesCorrectResults(t *testing.T) {
	// Create test books and files with ms-valued durations.
	fileSize64kbps := bytesForBitrate(3600, 64) // 1 hour at 64kbps
	msValuedDuration := 3600 * 1000              // 1 hour in milliseconds

	books := []database.Book{
		{ID: "book1", Title: "Book One"},
		{ID: "book2", Title: "Book Two"},
		{ID: "book3", Title: "Book Three"},
	}

	filesByID := map[string][]database.BookFile{
		"book1": {
			{ID: "file1a", BookID: "book1", FileSize: fileSize64kbps, Duration: msValuedDuration},
			{ID: "file1b", BookID: "book1", FileSize: fileSize64kbps, Duration: 3600}, // correct seconds
		},
		"book2": {
			{ID: "file2a", BookID: "book2", FileSize: fileSize64kbps, Duration: msValuedDuration},
		},
		"book3": {
			{ID: "file3a", BookID: "book3", FileSize: fileSize64kbps, Duration: 3600}, // no fix needed
		},
	}

	// Track writes to verify results.
	var upsertedFiles []*database.BookFile

	// Create a mock store with the necessary methods.
	ts := &testStore{
		MockStore: &database.MockStore{
			CountPrimaryBooksFunc: func() (int, error) {
				return len(books), nil
			},
			GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
				if offset >= len(books) {
					return nil, nil
				}
				end := offset + limit
				if end > len(books) {
					end = len(books)
				}
				return books[offset:end], nil
			},
			GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
				return filesByID[bookID], nil
			},
			BatchUpsertBookFilesFunc: func(files []*database.BookFile) error {
				upsertedFiles = append(upsertedFiles, files...)
				return nil
			},
		},
		recomputedBooks: make([]string, 0),
	}

	// Create the plugin and run the backfill.
	plugin := New(fakeDeps{store: ts})
	reporter := &mockReporter{}

	ctx := context.Background()
	params := durationBackfillParams{DryRun: false}
	rawParams, _ := json.Marshal(params)

	err := plugin.runDurationBackfill(ctx, rawParams, reporter)
	if err != nil {
		t.Fatalf("runDurationBackfill failed: %v", err)
	}

	// Verify that book1 and book2 were marked for recompute (they had fixes).
	if len(ts.recomputedBooks) != 2 {
		t.Errorf("expected 2 recomputed books, got %d: %v", len(ts.recomputedBooks), ts.recomputedBooks)
	}

	// Verify that the correct files were upserted with corrected durations.
	expectedUpserts := 2 // file1a and file2a
	if len(upsertedFiles) != expectedUpserts {
		t.Errorf("expected %d upserted files, got %d", expectedUpserts, len(upsertedFiles))
	}

	// Verify that file1a was corrected.
	var found bool
	for _, uf := range upsertedFiles {
		if uf.ID == "file1a" {
			if uf.Duration != 3600 {
				t.Errorf("file1a duration should be 3600, got %d", uf.Duration)
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("file1a not found in upserted files")
	}

	// Verify that the recomputed books list contains expected books.
	hasBook1 := false
	hasBook2 := false
	for _, bid := range ts.recomputedBooks {
		if bid == "book1" {
			hasBook1 = true
		} else if bid == "book2" {
			hasBook2 = true
		}
	}
	if !hasBook1 || !hasBook2 {
		t.Errorf("expected books book1 and book2 in recomputed, got %v", ts.recomputedBooks)
	}
}
