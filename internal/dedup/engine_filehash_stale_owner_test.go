// file: internal/dedup/engine_filehash_stale_owner_test.go
// version: 1.0.0
// guid: 4e7c1b9a-3d2f-4a86-b5e0-8c9d1f2a3b4c
// last-edited: 2026-09-02

package dedup

import (
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestCheckExactFileHash_SoftDeletedIndexOwner_IsNotADuplicate pins dedup bug
// hunt F2 at the engine layer. The book:hash: index is single-valued and does
// not filter soft-deleted rows, so after a merge whose winner did not own the
// index entry, the live winner's next scan finds its own soft-deleted loser
// as the "other" book. That is a stale pointer, not a duplicate: no merge, no
// candidate, no error.
func TestCheckExactFileHash_SoftDeletedIndexOwner_IsNotADuplicate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = true

	hash := "HASH-STALE"
	yes := true
	group := "VG-1"
	winner := &database.Book{ID: "LIVE", Title: "Same Title", FileHash: &hash, VersionGroupID: &group}
	loser := &database.Book{ID: "GONE", Title: "Same Title", FileHash: &hash, VersionGroupID: &group, MarkedForDeletion: &yes}

	mock.GetBookByFileHashFunc = func(h string) (*database.Book, error) {
		if h == hash {
			cp := *loser
			return &cp, nil
		}
		return nil, nil
	}
	var updates atomic.Int32
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updates.Add(1)
		return b, nil
	}

	merged, err := engine.checkExactFileHash(winner, "")
	if err != nil {
		t.Fatalf("checkExactFileHash: %v", err)
	}
	if merged {
		t.Fatal("a soft-deleted index owner must not be merged again")
	}
	if n := updates.Load(); n != 0 {
		t.Fatalf("no book row may be written for a stale pair; got %d UpdateBook calls", n)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("a stale pair must not become a review candidate; got %d", len(cands))
	}
}

// Two LIVE members of one version group are already merged; the exact-hash
// pass must not merge them again or re-emit them as a candidate.
func TestCheckExactFileHash_SameVersionGroup_IsNotADuplicate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = true

	hash := "HASH-GROUPED"
	group := "VG-2"
	a := &database.Book{ID: "A", Title: "Grouped", FileHash: &hash, VersionGroupID: &group}
	b := &database.Book{ID: "B", Title: "Grouped", FileHash: &hash, VersionGroupID: &group}
	mock.GetBookByFileHashFunc = func(h string) (*database.Book, error) { cp := *b; return &cp, nil }
	var updates atomic.Int32
	mock.UpdateBookFunc = func(id string, bk *database.Book) (*database.Book, error) {
		updates.Add(1)
		return bk, nil
	}

	merged, err := engine.checkExactFileHash(a, "")
	if err != nil {
		t.Fatalf("checkExactFileHash: %v", err)
	}
	if merged || updates.Load() != 0 {
		t.Fatalf("already-grouped pair must be left alone: merged=%v updates=%d", merged, updates.Load())
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("grouped pair must not become a candidate; got %d", len(cands))
	}
}

// A soft-deleted index owner with NO version group (a user deleted the book
// outright, or the row predates grouping) and auto-merge OFF would otherwise
// reach upsertExactCandidate and put a deleted row in the review queue. The
// engine-level skip is what stops it; MergeBooks' own guard never runs on
// this path.
func TestCheckExactFileHash_SoftDeletedUngroupedOwner_NoCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	hash := "HASH-UNGROUPED"
	yes := true
	live := &database.Book{ID: "LIVE2", Title: "Deleted Twin", FileHash: &hash}
	gone := &database.Book{ID: "GONE2", Title: "Deleted Twin", FileHash: &hash, MarkedForDeletion: &yes}
	mock.GetBookByFileHashFunc = func(h string) (*database.Book, error) { cp := *gone; return &cp, nil }

	merged, err := engine.checkExactFileHash(live, "")
	if err != nil || merged {
		t.Fatalf("checkExactFileHash: merged=%v err=%v", merged, err)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("a soft-deleted row must never become a review candidate; got %d", len(cands))
	}
}

// The scanned book, not the index owner, must survive when it is the one with
// a file row. handleFileHashMatch used to force `other` (the index owner) as
// primary; that put the file-less owner over the book that could play.
func TestCheckExactFileHash_AutoMerge_ElectsOnMeritNotIndexOwnership(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	engine.AutoMergeEnabled = true

	hash := "HASH-MERIT"
	title := "Merit Book"
	scanned := database.Book{ID: "SCANNED", Title: title, FileHash: &hash, Format: "mp3"}
	owner := database.Book{ID: "OWNER", Title: title, FileHash: &hash, Format: "m4b"} // BookIsBetter would pick it on format
	rs := newMergeRaceStore([]database.Book{scanned, owner})

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return rs.get(id), nil }
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) { return rs.update(id, b) }
	mock.GetBookByFileHashFunc = func(h string) (*database.Book, error) { return rs.get("OWNER"), nil }
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		if bookID == "SCANNED" {
			return []database.BookFile{{ID: "f", BookID: bookID, FilePath: "/lib/scanned.mp3"}}, nil
		}
		return nil, nil
	}

	merged, err := engine.checkExactFileHash(rs.get("SCANNED"), "")
	if err != nil {
		t.Fatalf("checkExactFileHash: %v", err)
	}
	if !merged {
		t.Fatal("expected an auto-merge")
	}
	s, o := rs.get("SCANNED"), rs.get("OWNER")
	if s.IsPrimaryVersion == nil || !*s.IsPrimaryVersion || (s.MarkedForDeletion != nil && *s.MarkedForDeletion) {
		t.Fatalf("the book with a file row must be the live primary; got primary=%v deleted=%v", s.IsPrimaryVersion, s.MarkedForDeletion)
	}
	if o.MarkedForDeletion == nil || !*o.MarkedForDeletion {
		t.Fatal("the file-less index owner must be the soft-deleted loser")
	}
}
