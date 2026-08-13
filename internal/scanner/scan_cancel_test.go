// file: internal/scanner/scan_cancel_test.go
// version: 1.0.0
// guid: 4f6a2d81-95e3-4c07-b1a8-6d20c9f3e574
// last-edited: 2026-08-12

// Regression tests for a scan that ignored its context.
//
// PerformScan's folder loop checked only log.IsCanceled() — the operation's own
// stop flag — and never ctx.Err(). A cancelled CONTEXT therefore did not stop
// the walk: the loop carried on into every remaining folder, each one failed its
// metadata pass with "context canceled", and the scan still reported success.
//
// Measured on production during the 4h15m scan of 2026-08-11: 2,406 folders were
// processed that way inside a single run.
//
// The second half of the defect lived in scanFolder: when ProcessBooksParallel
// returned an error it was logged and then execution FELL THROUGH to
// AutoOrganizeFn, so books whose metadata had never been extracted were filed
// anyway — with whatever title/author the scan started with, frequently empty.
// The same production run logged 7,561 "safeRename refusing to overwrite" and
// 3,481 organize-collision candidates, with 848 books landing on the single path
// "Unknown Author/Unknown Title/Unknown Title - Unknown Author.mp3".

package scanner

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TestPerformScanStopsOnCanceledContext pins the loop guard. With a cancelled
// context the scan must abort and say so, not walk every folder.
func TestPerformScanStopsOnCanceledContext(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{
				{Path: "/nonexistent/one", Enabled: true},
				{Path: "/nonexistent/two", Enabled: true},
				{Path: "/nonexistent/three", Enabled: true},
			}, nil
		},
	}

	ss := NewScanService(mockDB)

	organizeCalls := 0
	ss.AutoOrganizeFn = func(_ context.Context, _ []Book, _ logger.Logger) {
		organizeCalls++
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the scan even starts

	err := ss.PerformScan(ctx, &ScanRequest{}, logger.New("test"))

	if err == nil {
		t.Fatal("a scan run under a cancelled context must report an error, not success — " +
			"reporting success is what let 2,406 folders fail their metadata pass unnoticed")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cancel") {
		t.Errorf("the error should identify cancellation as the cause; got %v", err)
	}
	if organizeCalls != 0 {
		t.Errorf("auto-organize must not run for a cancelled scan; ran %d times", organizeCalls)
	}
}

// TestPerformScanRunsWhenContextIsLive is the discriminating half: the new guard
// must not abort a healthy scan. Without this, a fix that returned an error
// unconditionally would look identical on the test above.
func TestPerformScanRunsWhenContextIsLive(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{}, nil
		},
	}

	ss := NewScanService(mockDB)

	if err := ss.PerformScan(context.Background(), &ScanRequest{}, logger.New("test")); err != nil {
		t.Fatalf("a live context with nothing to scan must still succeed; got %v", err)
	}
}
