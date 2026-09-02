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
