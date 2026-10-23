// file: internal/dedup/engine_filehash_holders_test.go
// version: 1.0.0
// guid: 5b8e2d71-9c40-4f13-a6d8-3e7a0c1b9f52
// last-edited: 2026-10-22

package dedup

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// holderFixture wires a mock whose single-answer lookup names the holder with
// the smallest id (as the real index does) while BooksByFileHash lists them all.
func holderFixture(mock *database.MockStore, hash string, holders ...*database.Book) {
	mock.GetBookByFileHashFunc = func(h string) (*database.Book, error) {
		if h == hash && len(holders) > 0 {
			return holders[0], nil
		}
		return nil, nil
	}
	mock.BooksByFileHashFunc = func(h string) ([]*database.Book, error) {
		if h == hash {
			return holders, nil
		}
		return nil, nil
	}
	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }
}

// The single-answer lookup names the oldest holder, so checking the OLDEST book
// was told "yourself" and found no duplicate: the pair was only noticed when the
// newer book happened to be checked. A check of the older book must queue the
// pair on its own.
func TestCheckExactFileHash_OlderBookSeesTheNewerHolder(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	hash := "HASH-OLDER"
	older := &database.Book{ID: "01-OLDER", Title: "First Title", FileHash: &hash}
	newer := &database.Book{ID: "02-NEWER", Title: "Second Title", FileHash: &hash}
	holderFixture(mock, hash, older, newer)
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == older.ID {
			return older, nil
		}
		return newer, nil
	}

	merged, err := engine.checkExactFileHash(older, "")
	if err != nil {
		t.Fatalf("checkExactFileHash: %v", err)
	}
	if merged {
		t.Fatal("different titles must not auto-merge")
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("checking the older book must queue its pair with the newer holder; got %d candidates", len(cands))
	}
	ids := map[string]bool{cands[0].EntityAID: true, cands[0].EntityBID: true}
	if !ids[older.ID] || !ids[newer.ID] {
		t.Fatalf("candidate names %s and %s, want the older and the newer book", cands[0].EntityAID, cands[0].EntityBID)
	}
}

// A trashed holder ahead of a live one in id order must not hide the live one.
func TestCheckExactFileHash_TrashedHolderDoesNotHideALiveOne(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	hash := "HASH-TRASHED-FIRST"
	yes := true
	self := &database.Book{ID: "03-SELF", Title: "Self Title", FileHash: &hash}
	trashed := &database.Book{ID: "01-TRASHED", Title: "Old Title", FileHash: &hash, MarkedForDeletion: &yes}
	live := &database.Book{ID: "02-LIVE", Title: "Live Title", FileHash: &hash}
	holderFixture(mock, hash, trashed, live, self)
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return self, nil }

	if _, err := engine.checkExactFileHash(self, ""); err != nil {
		t.Fatalf("checkExactFileHash: %v", err)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("want exactly the pair with the live holder, got %d candidates", len(cands))
	}
	for _, id := range []string{cands[0].EntityAID, cands[0].EntityBID} {
		if id == trashed.ID {
			t.Fatalf("the trashed holder %s must not be paired", trashed.ID)
		}
	}
}

// A store that cannot list the holders cannot say there is no duplicate, so the
// book-level check errors instead of passing.
func TestCheckExactFileHash_StoreWithoutHolderListingFailsClosed(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	hash := "HASH-OPAQUE"
	book := &database.Book{ID: "01-BOOK", Title: "T", FileHash: &hash}
	holderFixture(mock, hash, book)
	engine.bookStore = struct{ Store }{engine.bookStore} // embeds only the interface's methods

	if _, err := engine.checkExactFileHash(book, ""); !errors.Is(err, database.ErrNoFileHashHolderLookup) {
		t.Fatalf("want ErrNoFileHashHolderLookup, got %v", err)
	}
}

// The scoring collector has the same shape: the older book must get a signal
// for the newer holder, and a trashed holder must give none.
func TestCollectExactFileHash_OlderBookAndTrashedHolders(t *testing.T) {
	hash := "HASH-COLLECT"
	yes := true
	older := &database.Book{ID: "01-OLDER", FileHash: &hash}
	newer := &database.Book{ID: "02-NEWER", FileHash: &hash}
	trashed := &database.Book{ID: "03-TRASHED", FileHash: &hash, MarkedForDeletion: &yes}
	mock := &database.MockStore{}
	holderFixture(mock, hash, older, newer, trashed)

	sigs, err := CollectExactFileHash(mock, older)
	if err != nil {
		t.Fatalf("CollectExactFileHash: %v", err)
	}
	if len(sigs) != 1 || sigs[0].Kind != unified.SigExactFile {
		t.Fatalf("want one exact-file signal (newer holder only), got %+v", sigs)
	}
	if want := "01-OLDER ↔ 02-NEWER"; !strings.Contains(sigs[0].Evidence, want) {
		t.Fatalf("signal evidence %q does not name %q", sigs[0].Evidence, want)
	}
}
