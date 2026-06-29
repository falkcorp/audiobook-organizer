// file: internal/dedup/engine_identifier_gate_test.go
// version: 1.0.0
// guid: 6e6934a1-44e9-45a5-b789-c71b541d7f74
// last-edited: 2026-06-28

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestIdentifiersConflict(t *testing.T) {
	tests := []struct {
		name string
		a    *database.Book
		b    *database.Book
		want bool
	}{
		{
			name: "different both-present ISBN13 conflicts after hyphen normalization",
			a:    &database.Book{ISBN13: strPtr("978-0-00-000001-1")},
			b:    &database.Book{ISBN13: strPtr("9780000000028")},
			want: true,
		},
		{
			name: "same normalized ISBN13 keeps pair",
			a:    &database.Book{ISBN13: strPtr("978-0-00-000001-1")},
			b:    &database.Book{ISBN13: strPtr("9780000000011")},
			want: false,
		},
		{
			name: "missing identifier is conservative and keeps pair",
			a:    &database.Book{ISBN13: strPtr("9780000000011")},
			b:    &database.Book{},
			want: false,
		},
		{
			name: "same ASIN keeps pair after case normalization",
			a:    &database.Book{ASIN: strPtr("b00case001")},
			b:    &database.Book{ASIN: strPtr("B00CASE001")},
			want: false,
		},
		{
			name: "different both-present ASIN conflicts after case normalization",
			a:    &database.Book{ASIN: strPtr("b00case001")},
			b:    &database.Book{ASIN: strPtr("B00CASE002")},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := identifiersConflict(tt.a, tt.b); got != tt.want {
				t.Fatalf("identifiersConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpsertExactCandidate_DropsConflictingIdentifiers(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	conflictA := primaryBook("CONFLICT_A", "Shared Intro")
	conflictA.ISBN13 = strPtr("978-0-00-000001-1")
	conflictB := primaryBook("CONFLICT_B", "Shared Intro")
	conflictB.ISBN13 = strPtr("9780000000028")
	sameA := primaryBook("SAME_A", "Shared Intro")
	sameA.ISBN13 = strPtr("978-0-00-000003-5")
	sameB := primaryBook("SAME_B", "Shared Intro")
	sameB.ISBN13 = strPtr("9780000000035")
	missingA := primaryBook("MISSING_A", "Shared Intro")
	missingA.ISBN13 = strPtr("9780000000042")
	missingB := primaryBook("MISSING_B", "Shared Intro")

	if err := engine.upsertExactCandidate(conflictA, conflictB, "acoustid", 1.0); err != nil {
		t.Fatalf("upsert conflict pair: %v", err)
	}
	if err := engine.upsertExactCandidate(sameA, sameB, "acoustid", 1.0); err != nil {
		t.Fatalf("upsert same-id pair: %v", err)
	}
	if err := engine.upsertExactCandidate(missingA, missingB, "acoustid", 1.0); err != nil {
		t.Fatalf("upsert missing-id pair: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 2 {
		t.Fatalf("expected same-id and missing-id pairs only, got %d: %+v", len(cands), cands)
	}
	for _, c := range cands {
		if c.EntityAID == "CONFLICT_A" || c.EntityBID == "CONFLICT_A" ||
			c.EntityAID == "CONFLICT_B" || c.EntityBID == "CONFLICT_B" {
			t.Fatalf("conflicting ISBN13 pair leaked into candidates: %+v", c)
		}
	}
}

func TestAcoustIDScan_DropsConflictingIdentifiers(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	bookA := primaryBook("BOOK_A", "Shared Intro A")
	bookA.ISBN13 = strPtr("9780000000011")
	bookB := primaryBook("BOOK_B", "Shared Intro B")
	bookB.ISBN13 = strPtr("9780000000028")
	books := map[string]*database.Book{"BOOK_A": bookA, "BOOK_B": bookB}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		if offset > 0 {
			return nil, nil
		}
		return []database.Book{*bookA, *bookB}, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return books[id], nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return []database.BookFile{{
			ID:           "FILE_" + bookID,
			BookID:       bookID,
			FilePath:     "/library/" + bookID + "/intro.mp3",
			AcoustIDSeg0: validFP80,
			AcoustIDSeg1: "different-" + bookID,
			AcoustIDSeg2: "different2-" + bookID,
			AcoustIDSeg3: "different3-" + bookID,
			AcoustIDSeg4: "different4-" + bookID,
			AcoustIDSeg5: "different5-" + bookID,
			AcoustIDSeg6: "different6-" + bookID,
		}}, nil
	}
	mock.GetBookFileByAcoustIDFunc = func(fp string) (*database.BookFile, error) {
		if fp != validFP80 {
			return nil, nil
		}
		return &database.BookFile{
			ID:       "FILE_BOOK_B",
			BookID:   "BOOK_B",
			FilePath: "/library/BOOK_B/intro.mp3",
		}, nil
	}

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}
	if cands := pendingCandidates(t, es); len(cands) != 0 {
		t.Fatalf("expected conflicting ISBN13s to drop shared-fingerprint pair, got %+v", cands)
	}
}
