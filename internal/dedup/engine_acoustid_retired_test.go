// file: internal/dedup/engine_acoustid_retired_test.go
// version: 1.0.0
// guid: 9d3e7a52-4b18-4c60-8e2f-1a6c05b9d374
// last-edited: 2026-10-26

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// acoustidCandidateCount is how many acoustid-layer candidates the scan left.
func acoustidCandidateCount(t *testing.T, es *database.EmbeddingStore) int {
	t.Helper()
	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Layer: "acoustid"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	return total
}

// lshMockStore is a MockStore that also has the Pebble-only LSH candidate
// lookup, so AcoustIDScan takes its Tier-0 branch. GetBookFileByID is the
// MockStore's own (GetBookFileByIDFunc).
type lshMockStore struct {
	*database.MockStore
	cands []string
}

func (s *lshMockStore) LookupAcoustIDCandidates(_ []byte, _ int) ([]string, error) {
	return s.cands, nil
}

// AcoustIDScan's exact-segment hit (Tier 1) through a retired row must not pair
// the scanned book with the retired row's holder. The mock does not filter, as a
// store whose getter forgot to would not, so the guard in the scan is what is
// under test. The live control in the same table fails if the guard is the only
// reason nothing was emitted.
func TestAcoustIDScan_RetiredExactHitIsNotACandidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		retired bool
		want    int
	}{
		{"retired row", true, 0},
		{"live control", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, mock, es := setupTestEngine(t)
			books := []database.Book{
				{ID: "BOOK_A", Title: "Dark Victory"},
				{ID: "BOOK_H", Title: "Dark Victory"},
			}
			wireAcoustIDScanMock(mock, books, map[string][]database.BookFile{
				"BOOK_A": {{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/book.mp3", AcoustIDSeg0: validFP80}},
			})
			hit := &database.BookFile{ID: "FILE_R", BookID: "BOOK_H", FilePath: "/lib/h/book.mp3", Retired: tc.retired}
			mock.GetBookFileByAcoustIDFunc = func(fp string) (*database.BookFile, error) {
				if fp == validFP80 {
					return hit, nil
				}
				return nil, nil
			}

			if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
				t.Fatalf("AcoustIDScan: %v", err)
			}
			if got := acoustidCandidateCount(t, es); got != tc.want {
				t.Fatalf("exact hit through a %s: want %d candidate(s), got %d", tc.name, tc.want, got)
			}
		})
	}
}

// AcoustIDScan's whole-file LSH candidate (Tier 0) that is a retired row: the
// fpidx: value still names it and its BookID is the holder, so it must not pair.
func TestAcoustIDScan_RetiredLSHCandidateIsNotACandidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		retired bool
		want    int
	}{
		{"retired row", true, 0},
		{"live control", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, mock, es := setupTestEngine(t)
			books := []database.Book{
				{ID: "BOOK_A", Title: "Dark Victory"},
				{ID: "BOOK_H", Title: "Dark Victory"},
			}
			fp := makeFP(1000)
			wireAcoustIDScanMock(mock, books, map[string][]database.BookFile{
				"BOOK_A": {{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/book.mp3",
					AcoustIDFingerprint: fp, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}},
			})
			cand := &database.BookFile{ID: "FILE_R", BookID: "BOOK_H", FilePath: "/lib/h/book.mp3", Retired: tc.retired,
				AcoustIDFingerprint: fp, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}
			mock.GetBookFileByIDFunc = func(_, fileID string) (*database.BookFile, error) {
				if fileID == "FILE_R" {
					return cand, nil
				}
				return nil, nil
			}
			engine.bookStore = &lshMockStore{MockStore: mock, cands: []string{"FILE_R"}}

			if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
				t.Fatalf("AcoustIDScan: %v", err)
			}
			if got := acoustidCandidateCount(t, es); got != tc.want {
				t.Fatalf("LSH candidate through a %s: want %d candidate(s), got %d", tc.name, tc.want, got)
			}
		})
	}
}
