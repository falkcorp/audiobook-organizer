// file: internal/dedup/engine_part_vs_whole_test.go
// version: 1.0.0
// guid: 6d3a9f21-4b7c-4e58-9a01-2f5c8d6e7b34
// last-edited: 2026-07-01

// Regression guard for CONS-15: upsertExactCandidate must reject a pair where
// one side is a single-file book whose duration is a small fraction of the
// other side's multi-file total duration, even when titles/identifiers
// otherwise match — a lone chapter mis-tagged with the whole book's metadata
// must not be merged as a 100%-confidence exact duplicate. Ordinary
// single-file-vs-single-file duplicates of comparable duration must still be
// emitted normally.
package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestUpsertExactCandidate_PartVsWholeGuard(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	b := primaryBook("B", "Real Book Title")

	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "A":
			// Single file, short duration relative to B's total.
			return []database.BookFile{{ID: "FA1", BookID: "A", Duration: 600}}, nil
		case "B":
			// Ten files totalling far more than A's single file.
			files := make([]database.BookFile, 0, 10)
			for i := 0; i < 10; i++ {
				files = append(files, database.BookFile{ID: "FB" + string(rune('0'+i)), BookID: "B", Duration: 3600})
			}
			return files, nil
		}
		return nil, nil
	}

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for part-vs-whole pair, got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_ComparableSingleFilesStillPersisted(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	b := primaryBook("B", "Real Book Title")

	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "A":
			return []database.BookFile{{ID: "FA1", BookID: "A", Duration: 3600}}, nil
		case "B":
			return []database.BookFile{{ID: "FB1", BookID: "B", Duration: 3650}}, nil
		}
		return nil, nil
	}

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Errorf("expected 1 candidate for comparable single-file pair, got %d: %+v", len(cands), cands)
	}
}
