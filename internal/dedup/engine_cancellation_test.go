// file: internal/dedup/engine_cancellation_test.go
// version: 1.0.0
// guid: 6a1d4c8e-9b32-4f71-8e05-3c9a7d2b5f16
// last-edited: 2026-07-05

// Regression guard for the 2026-07-05 unresponsive-cancel incident: a
// dedup.full-scan op cancellation took 90+ seconds to take effect and
// eventually required a hard systemctl restart. Root cause was
// runUnifiedScoringForBook's per-candidate loop having no cancellation
// check — only the outer FullScan per-book loop checked ctx.Err(). This
// test locks in the fix: the per-candidate loop must notice a cancelled
// context promptly and stop processing remaining candidates, rather than
// grinding through all of them for a pathologically large candidate set.
package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestRunUnifiedScoringForBook_StopsPromptlyOnCancel(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	book := primaryBook("BOOK_A", "Book A")
	others := make(map[string]*database.Book)
	const numCandidates = 5
	for i := 0; i < numCandidates; i++ {
		id := "OTHER_" + string(rune('0'+i))
		others[id] = primaryBook(id, "Other Title "+string(rune('0'+i)))
	}

	// Seed one pending candidate between book and each of the "others" so
	// runUnifiedScoringForBook has multiple candidates to iterate for this
	// one book.
	for id, other := range others {
		if err := engine.upsertExactCandidate(book, other, "exact", 1.0); err != nil {
			t.Fatalf("seed candidate %s: %v", id, err)
		}
	}
	if got := len(pendingCandidates(t, es)); got != numCandidates {
		t.Fatalf("setup: expected %d seeded candidates, got %d", numCandidates, got)
	}

	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context partway through iteration, from inside the
	// GetBookByID lookup the per-candidate loop makes for each pending
	// candidate. This simulates the outer op being cancelled mid-scan.
	var lookups int
	const cancelAfter = 2
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		lookups++
		if lookups == cancelAfter {
			cancel()
		}
		return others[id], nil
	}

	err := engine.runUnifiedScoringForBook(ctx, book, "Author")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runUnifiedScoringForBook error = %v, want context.Canceled", err)
	}
	if lookups >= numCandidates {
		t.Errorf("runUnifiedScoringForBook processed %d/%d candidates after cancel — cancellation check did not stop the loop promptly", lookups, numCandidates)
	}
}
