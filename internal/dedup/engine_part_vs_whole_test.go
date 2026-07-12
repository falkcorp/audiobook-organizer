// file: internal/dedup/engine_part_vs_whole_test.go
// version: 1.1.0
// guid: 6d3a9f21-4b7c-4e58-9a01-2f5c8d6e7b34
// last-edited: 2026-07-11

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

// TestIsPartVsWholeMismatchAlignedRatio pins the veto boundary after aligning
// partVsWholeDurationRatioMax from 0.6 to 0.5 (INIT-1 T8), matching the
// dataset miner's partVsWholeRatioMax. The whole side is a two-file book
// totalling 1000; the part side is a single file whose duration sets the ratio.
//   - ratio 0.49 (<0.5): still vetoed as a mismatch.
//   - ratio 0.55 ([0.5,0.6)): NO LONGER vetoed — anti-over-suppression proof
//     that the veto narrowed rather than widened (was suppressed at 0.6).
//   - ratio 0.70 (>0.6): unchanged, never vetoed.
//
// Zero/unknown-duration handling (non-positive part or whole → false) is left
// exactly as-is and asserted below.
func TestIsPartVsWholeMismatchAlignedRatio(t *testing.T) {
	const wholeTotal = 1000 // two files of 500 each

	cases := []struct {
		name         string
		partDuration int
		wantMismatch bool
	}{
		{"ratio_0.49_vetoed", 490, true},
		{"ratio_0.55_not_vetoed_narrowed_band", 550, false},
		{"ratio_0.70_not_vetoed_unchanged", 700, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, mock, _ := setupTestEngine(t)

			a := primaryBook("A", "Real Book Title")
			b := primaryBook("B", "Real Book Title")

			mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
				switch bookID {
				case "A":
					// Single-file "part" side.
					return []database.BookFile{{ID: "FA1", BookID: "A", Duration: tc.partDuration}}, nil
				case "B":
					// Two-file "whole" side totalling wholeTotal.
					return []database.BookFile{
						{ID: "FB1", BookID: "B", Duration: wholeTotal / 2},
						{ID: "FB2", BookID: "B", Duration: wholeTotal / 2},
					}, nil
				}
				return nil, nil
			}

			if got := engine.isPartVsWholeMismatch(a, b); got != tc.wantMismatch {
				t.Errorf("isPartVsWholeMismatch(part=%d, whole=%d) = %v, want %v",
					tc.partDuration, wholeTotal, got, tc.wantMismatch)
			}
		})
	}
}

// TestIsPartVsWholeMismatchNonPositiveDurations asserts the zero/unknown
// duration guard is unchanged: a non-positive part or whole total is never a
// mismatch, regardless of the ratio constant.
func TestIsPartVsWholeMismatchNonPositiveDurations(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	b := primaryBook("B", "Real Book Title")

	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "A":
			// Zero-duration single file (unknown duration).
			return []database.BookFile{{ID: "FA1", BookID: "A", Duration: 0}}, nil
		case "B":
			return []database.BookFile{
				{ID: "FB1", BookID: "B", Duration: 500},
				{ID: "FB2", BookID: "B", Duration: 500},
			}, nil
		}
		return nil, nil
	}

	if engine.isPartVsWholeMismatch(a, b) {
		t.Error("isPartVsWholeMismatch with zero part duration = true, want false (guard must stay)")
	}
}
