// file: internal/dedup/engine_acoustid_retired_test.go
// version: 1.0.0
// guid: 4e9a1c75-8b02-4d36-a7f1-3c5d92e0b814
// last-edited: 2026-10-23

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// acoustIDRetiredScanStore is a MockStore that also answers the LSH candidate
// lookup (lshCandidateStore), with one fixed candidate row, so AcoustIDScan's
// whole-file tier runs. Like a real index, it returns the row whatever its
// Retired flag says: the scan has to drop it itself.
type acoustIDRetiredScanStore struct {
	*database.MockStore
	cand *database.BookFile
}

func (s *acoustIDRetiredScanStore) LookupAcoustIDCandidates(_ []byte, _ int) ([]string, error) {
	return []string{s.cand.ID}, nil
}

func (s *acoustIDRetiredScanStore) GetBookFileByID(_, fileID string) (*database.BookFile, error) {
	if fileID == s.cand.ID {
		c := *s.cand
		return &c, nil
	}
	return nil, nil
}

// pendingBookCandidates is the engine's pending book candidates.
func pendingBookCandidates(t *testing.T, es *database.EmbeddingStore) []database.DedupCandidate {
	t.Helper()
	candidates, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	return candidates
}

// The exact (tier 1) branch of AcoustIDScan must not pair a book with a retired
// version: GetBookFileByAcoustID answers nil for a retired-only identity, and the
// scan's own Retired test keeps it right on a store that returns the row anyway
// (the mock does not filter). The control is the same row live, which pairs.
func TestAcoustIDScan_ExactHitOnRetiredRowIsNotACandidate(t *testing.T) {
	run := func(t *testing.T, retired bool) []database.DedupCandidate {
		engine, mock, es := setupTestEngine(t)
		bookA := database.Book{ID: "BOOK_A", Title: "Book A"}
		bookB := database.Book{ID: "BOOK_B", Title: "Book B"}
		fileA := database.BookFile{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/library/a/book.m4b", AcoustIDSeg0: validFP80, AcoustIDFingerprintDurationSec: 7200}
		fileB := database.BookFile{ID: "FILE_B", BookID: "BOOK_B", FilePath: "/library/b/book.m4b", AcoustIDSeg0: validFP80, AcoustIDFingerprintDurationSec: 7200, Retired: retired}
		wireAcoustIDScanMock(mock, []database.Book{bookA, bookB}, map[string][]database.BookFile{
			"BOOK_A": {fileA},
			// B's own file is not scanned as a query: only A's row can emit the pair.
			"BOOK_B": nil,
		})
		mock.GetBookFileByAcoustIDFunc = func(fp string) (*database.BookFile, error) {
			if fp == validFP80 {
				return &fileB, nil
			}
			return nil, nil
		}
		if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
			t.Fatalf("AcoustIDScan: %v", err)
		}
		return pendingBookCandidates(t, es)
	}

	if got := run(t, true); len(got) != 0 {
		t.Fatalf("an exact hit on a retired row produced %d candidate(s): %+v", len(got), got)
	}
	if got := run(t, false); len(got) != 1 {
		t.Fatalf("control: the same row, live, must produce one candidate, got %d: %+v", len(got), got)
	}
}

// The whole-file LSH (tier 0) branch of AcoustIDScan must not pair a book with a
// retired candidate: the fpidx: value still names the retired row, and its BookID
// is the holder. The control is the same candidate live, which pairs.
func TestAcoustIDScan_LSHCandidateThatIsRetiredIsNotACandidate(t *testing.T) {
	run := func(t *testing.T, retired bool) []database.DedupCandidate {
		engine, mock, es := setupTestEngine(t)
		bookA := database.Book{ID: "BOOK_A", Title: "Book A"}
		bookB := database.Book{ID: "BOOK_B", Title: "Book B"}
		fileA := database.BookFile{
			ID: "FILE_A", BookID: "BOOK_A", FilePath: "/library/a/book.m4b",
			AcoustIDFingerprint: makeFP(1000), AcoustIDFPVersion: fingerprint.PrintEncodingVersion,
		}
		cand := &database.BookFile{
			ID: "FILE_B", BookID: "BOOK_B", FilePath: "/library/b/book.m4b", Retired: retired,
			AcoustIDFingerprint: makeFP(1000), AcoustIDFPVersion: fingerprint.PrintEncodingVersion,
		}
		wireAcoustIDScanMock(mock, []database.Book{bookA, bookB}, map[string][]database.BookFile{
			"BOOK_A": {fileA},
			"BOOK_B": nil,
		})
		engine.bookStore = &acoustIDRetiredScanStore{MockStore: mock, cand: cand}
		if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
			t.Fatalf("AcoustIDScan: %v", err)
		}
		return pendingBookCandidates(t, es)
	}

	if got := run(t, true); len(got) != 0 {
		t.Fatalf("an LSH hit on a retired row produced %d candidate(s): %+v", len(got), got)
	}
	if got := run(t, false); len(got) != 1 {
		t.Fatalf("control: the same row, live, must produce one candidate, got %d: %+v", len(got), got)
	}
}
