// file: internal/plugins/dedup/full_scan_test.go
// version: 1.0.1
// guid: c3d4e5f6-a7b8-49c0-b1d2-e3f4a5b6c7d8
// last-edited: 2026-07-07

// Tests for the dedup.full-scan op's dual-phase progress reporting.
//
// Context: Engine.FullScan runs two full passes over every book — a
// "scan" pass (Layer 1 exact + Layer 2 embedding checks) and a "score" pass
// (unified composite scoring). The "score" pass used to report progress
// zero times, leaving the operation log silent for however long the
// CPU-heavy scoring pass took (observed: 25+ minutes on a ~29K-book
// library, 100% CPU, zero log output). These tests confirm both phases now
// report progress through their own sdk.Progress tracker, and that the
// progress messages carry a rate/ETA suffix.

package dedup

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/stretchr/testify/require"
)

// recordingReporter is a mockReporter that also records every
// UpdateProgress message, so tests can assert on phase-tagged progress
// text without needing a real operation-log backend.
type recordingReporter struct {
	mockReporter
	messages []string
}

func (r *recordingReporter) UpdateProgress(current, total int, message string) error {
	r.messages = append(r.messages, message)
	return nil
}

// fullScanMockStore builds a MockStore with n primary books that have no
// AuthorID, FileHash, or ISBN/ASIN — every Layer 1 exact-match check
// (checkExactFileHash aside, which still calls GetBookFiles) short-circuits
// immediately, and runUnifiedScoringForBook returns nil immediately because
// there are no pending embedding candidates. This keeps the test fast and
// deterministic while still exercising both of FullScan's real loops.
func fullScanMockStore(n int) *database.MockStore {
	primary := true
	books := make([]database.Book, n)
	for i := range books {
		books[i] = database.Book{
			ID:               "book-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Title:            "Untitled",
			IsPrimaryVersion: &primary,
		}
	}
	cores := make([]database.BookCore, len(books))
	for i := range books {
		cores[i] = books[i].Core()
	}
	return &database.MockStore{
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			return books, nil
		},
		// STOREFID W5d-1 moved the engine's getAllBooks/getAllBooksUnfiltered
		// onto GetAllBooksCore, so FullScan now reads through this method —
		// return the same fixture (as Core) rather than the mock's nil default,
		// or the scan sees 0 books.
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return cores, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return nil, nil
		},
	}
}

// TestRunFullScan_ReportsBothPhasesDistinctly verifies that runFullScan
// drives two separate progress trackers — one for the "scan" phase
// (Layer 1/2) and one for the "score" phase (unified composite scoring) —
// and that both surface distinct, phase-labeled messages culminating in
// their own completion lines, ending with the overall "Dedup scan
// complete" finisher.
func TestRunFullScan_ReportsBothPhasesDistinctly(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := fullScanMockStore(25) // > 10 so the every-10th cadence fires mid-scan
	eng := dedupengine.NewEngine(es, ms, nil, nil, merge.NewService(ms))

	p := &Plugin{engine: eng, store: ms, embeddingStore: es}
	reporter := &recordingReporter{}

	err := p.runFullScan(context.Background(), nil, reporter)
	require.NoError(t, err)

	var scanMsgs, scoreMsgs []string
	for _, m := range reporter.messages {
		switch {
		case strings.Contains(m, "Scanning books"):
			scanMsgs = append(scanMsgs, m)
		case strings.Contains(m, "Composing scores"):
			scoreMsgs = append(scoreMsgs, m)
		}
	}

	if len(scanMsgs) == 0 {
		t.Fatal("expected at least one 'Scanning books' progress message")
	}
	if len(scoreMsgs) == 0 {
		t.Fatal("expected at least one 'Composing scores' progress message — this is the previously-silent phase")
	}

	// At least one message per phase (after the first, which has no
	// elapsed time to compute a rate from) should carry the rate/ETA
	// suffix so operators get a rough completion estimate.
	hasETA := func(msgs []string) bool {
		for _, m := range msgs {
			if strings.Contains(m, "books/sec") && strings.Contains(m, "remaining") {
				return true
			}
		}
		return false
	}
	if !hasETA(scanMsgs) {
		t.Errorf("expected at least one 'scan' phase message with a books/sec ETA suffix, got: %v", scanMsgs)
	}
	if !hasETA(scoreMsgs) {
		t.Errorf("expected at least one 'score' phase message with a books/sec ETA suffix, got: %v", scoreMsgs)
	}

	// The score phase's own completion line must appear (distinct from the
	// scan phase's "Scanning books: N / N" line), followed by the overall
	// finisher.
	foundScoreComplete := false
	foundOverallComplete := false
	for _, m := range reporter.messages {
		if strings.Contains(m, "Composing scores complete") {
			foundScoreComplete = true
		}
		if strings.Contains(m, "Dedup scan complete") {
			foundOverallComplete = true
		}
	}
	if !foundScoreComplete {
		t.Error("expected a 'Composing scores complete' message for the score phase")
	}
	if !foundOverallComplete {
		t.Error("expected the overall 'Dedup scan complete' finisher message")
	}
}

// TestRunFullScan_EmptyLibraryStillReportsBothTrackers verifies that even
// with zero books (FullScan's progress callback never fires because
// total<=0 guards it), runFullScan still creates both the "scan" and
// "score" fallback trackers so the op doesn't panic on a nil *sdk.Progress
// and still emits a completion message.
func TestRunFullScan_EmptyLibraryStillReportsBothTrackers(t *testing.T) {
	es := newTestEmbeddingStorePurge(t)
	ms := fullScanMockStore(0)
	eng := dedupengine.NewEngine(es, ms, nil, nil, merge.NewService(ms))

	p := &Plugin{engine: eng, store: ms, embeddingStore: es}
	reporter := &recordingReporter{}

	err := p.runFullScan(context.Background(), nil, reporter)
	require.NoError(t, err)

	foundOverallComplete := false
	for _, m := range reporter.messages {
		if strings.Contains(m, "Dedup scan complete") {
			foundOverallComplete = true
		}
	}
	if !foundOverallComplete {
		t.Error("expected the overall 'Dedup scan complete' finisher message even for an empty library")
	}
}
