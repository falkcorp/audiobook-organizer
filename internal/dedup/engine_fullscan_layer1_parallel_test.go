// file: internal/dedup/engine_fullscan_layer1_parallel_test.go
// version: 1.1.0
// guid: c3d4e5f6-a7b8-49c0-8d1e-2f3a4b5c6d7e
// last-edited: 2026-07-05

// Regression tests for CONC-4: FullScan's main pass now splits into a
// parallel Pass 1 (Layer-1 exact checks — checkExactFileHash/ISBN/Title/
// duration — sharded via registry.RunItems) followed by the unchanged
// serial Pass 2 (Layer-2 embedding batch accumulation + circuit breaker,
// which has loop-carried state and must stay sequential).
//
// TestFullScanLayer1Parallel_SameCandidatesAsSerial proves the two-pass
// FullScan produces the EXACT same candidate set as an independent serial
// reference over a fixture where all four Layer-1 checks fire for every
// pair simultaneously (shared ISBN, shared near-identical title, shared
// duration, shared file hash) — i.e. many emitters racing to
// upsertCandidateWithLiveLabel the SAME pair from different signals at
// once. Run under -race to prove EmbeddingStore.UpsertCandidateNew's
// internal locking makes concurrent upserts of the same pair safe and
// idempotent, as documented on the Pass 1 comment in FullScan.
//
// TestFullScanLayer1AutoMergeConcurrent_NoRace exercises the
// review-critical addition this task made: handleFileHashMatch can call
// mergeService.MergeBooks when AutoMergeEnabled is set, and MergeBooks
// itself does an unguarded read-modify-write per book with no internal
// locking. The fixture wires many DISJOINT auto-merge pairs so the
// expected end state is fully deterministic regardless of goroutine
// scheduling; the test proves every pair merged exactly once, into the
// correct winner, with no data race on the shared mock store — verifying
// the mergeMu guard documented on the Engine struct.
package dedup

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fullScanLayer1FixtureBooks builds n books that all share one ISBN13, one
// near-identical title, one duration, one author, and one file hash — so
// checkExactFileHash, checkExactISBN, checkExactTitle, and checkDurationMatch
// ALL fire for (most) pairs, exercising concurrent upserts of the SAME pair
// key from multiple Layer-1 signals at once. Duration is set so
// hasPlausibleAudio/hasKnownShortDuration don't suppress the pair, and
// IsPrimaryVersion is left nil (isNonPrimaryVersion treats nil as primary)
// so upsertExactCandidate's primary gate passes.
func fullScanLayer1FixtureBooks(n int) []database.Book {
	isbn := "9780000000099"
	dur := 3600
	authorID := 1
	fileHash := "SHARED-HASH-1"
	books := make([]database.Book, n)
	for i := 0; i < n; i++ {
		books[i] = database.Book{
			ID:       fmt.Sprintf("L1-%03d", i),
			Title:    "The Same Layer1 Book",
			ISBN13:   &isbn,
			Duration: &dur,
			AuthorID: &authorID,
			FileHash: &fileHash,
		}
	}
	return books
}

// wireFullScanLayer1Mock points a MockStore's read paths at the given book
// set. GetBookByFileHash always resolves to books[0] (mirrors a real
// unique file-hash index having exactly one owner per hash value, so
// checkExactFileHash forms a "star" of pairs against books[0] rather than a
// fabricated full clique) — the ISBN/title/duration checks independently
// scan all books via GetAllBooks/GetBooksByAuthorIDCore and form the full
// clique on their own, so the combined want-set is still deterministic.
func wireFullScanLayer1Mock(mock *database.MockStore, books []database.Book) {
	byID := make(map[string]database.Book, len(books))
	for _, b := range books {
		byID[b.ID] = b
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		out := make([]database.Book, len(books))
		copy(out, books)
		return out, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if b, ok := byID[id]; ok {
			return &b, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: "Layer1 Author"}, nil
	}
	mock.GetBooksByAuthorIDCoreFunc = func(authorID int) ([]database.BookCore, error) {
		out := make([]database.BookCore, len(books))
		for i := range books {
			out[i] = books[i].Core()
		}
		return out, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		b := books[0]
		return &b, nil
	}
}

// TestFullScanLayer1Parallel_SameCandidatesAsSerial is the CONC-4 parity
// test: FullScan's parallel Layer-1 pass (registry.RunItems, Concurrency ==
// runtime.NumCPU()) followed by the serial Layer-2/scoring passes must
// persist the exact same candidate set that the pre-parallelization plain
// serial loop (runFullScanLayer1AndScoreSerially, unchanged from the CONC-2
// test) produces for the identical input.
func TestFullScanLayer1Parallel_SameCandidatesAsSerial(t *testing.T) {
	// Sized well above a typical runtime.NumCPU() so the RunItems pool
	// genuinely shards work across multiple goroutines.
	const numBooks = 16

	// --- Serial reference: independent engine/store, same fixture. ---
	engineB, mockB, esB := setupTestEngine(t)
	booksB := fullScanLayer1FixtureBooks(numBooks)
	wireFullScanLayer1Mock(mockB, booksB)
	runFullScanLayer1AndScoreSerially(t, engineB, booksB)

	wantCands, _, err := esB.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("serial reference ListCandidates: %v", err)
	}
	want := canonicalizeScoredCandidates(t, wantCands)
	if len(want) == 0 {
		t.Fatalf("fixture is vacuous: expected >0 candidate pairs but got 0")
	}
	// ISBN/title/duration each independently scan every book and form a
	// full clique; file-hash's star is a subset of that clique.
	wantPairCount := numBooks * (numBooks - 1) / 2
	if len(want) != wantPairCount {
		t.Fatalf("serial reference produced %d pairs, want %d (C(%d,2)) — fixture assumptions drifted", len(want), wantPairCount, numBooks)
	}

	// --- Parallel: through the real (two-pass) FullScan entry point. ---
	engineA, mockA, esA := setupTestEngine(t)
	booksA := fullScanLayer1FixtureBooks(numBooks)
	wireFullScanLayer1Mock(mockA, booksA)

	if err := engineA.FullScan(context.Background(), nil); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	gotCands, _, err := esA.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("parallel ListCandidates: %v", err)
	}
	got := canonicalizeScoredCandidates(t, gotCands)

	if len(got) != len(want) {
		t.Fatalf("candidate count mismatch: parallel got %d, serial want %d", len(got), len(want))
	}
	for key, w := range want {
		g, ok := got[key]
		if !ok {
			t.Fatalf("missing expected pair %s (lost update in parallel Layer-1 pass)", key)
		}
		if g != w {
			t.Fatalf("pair %s mismatch: parallel=%+v serial=%+v", key, g, w)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected pair %s (spurious emit in parallel Layer-1 pass)", key)
		}
	}
}

// mergeRaceStore is a small mutex-guarded book map standing in for the
// GetBookByID/UpdateBook halves of database.Store, used by
// TestFullScanLayer1AutoMergeConcurrent_NoRace so MergeBooks' read-modify-
// write sequence has somewhere real to land. Guarded by mu because,
// absent the mergeMu fix under test, concurrent Layer-1 workers could call
// MergeBooks at the same time and race on these very maps.
type mergeRaceStore struct {
	mu    sync.Mutex
	books map[string]*database.Book
}

func newMergeRaceStore(books []database.Book) *mergeRaceStore {
	s := &mergeRaceStore{books: make(map[string]*database.Book, len(books))}
	for i := range books {
		b := books[i]
		s.books[b.ID] = &b
	}
	return s
}

func (s *mergeRaceStore) get(id string) *database.Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.books[id]
	if !ok || b == nil {
		return nil
	}
	cp := *b
	return &cp
}

func (s *mergeRaceStore) update(id string, b *database.Book) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *b
	s.books[id] = &cp
	out := cp
	return &out, nil
}

// TestFullScanLayer1AutoMergeConcurrent_NoRace exercises the mergeMu guard:
// with AutoMergeEnabled, checkExactFileHash's handleFileHashMatch calls the
// UNguarded merge.Service.MergeBooks, so FullScan's parallel Layer-1 pass
// must serialize those calls or risk two workers racing on the same
// underlying book rows. The fixture wires numPairs DISJOINT auto-merge
// pairs (own file hash + own matching title, no cross-pair overlap), so the
// end state is fully deterministic: run under -race, then assert every
// pair merged exactly once into the expected winner.
func TestFullScanLayer1AutoMergeConcurrent_NoRace(t *testing.T) {
	const numPairs = 20 // > typical runtime.NumCPU(); many pairs contend for mergeMu at once

	type pair struct{ loserID, winnerID, hash string }
	pairs := make([]pair, numPairs)
	var fixture []database.Book
	hashToWinner := make(map[string]string, numPairs)
	for p := 0; p < numPairs; p++ {
		loserID := fmt.Sprintf("MA-%03d", p)
		winnerID := fmt.Sprintf("MB-%03d", p)
		hash := fmt.Sprintf("HASH-%03d", p)
		title := fmt.Sprintf("Merge Book %03d", p)
		pairs[p] = pair{loserID: loserID, winnerID: winnerID, hash: hash}
		hashToWinner[hash] = winnerID
		fixture = append(fixture,
			database.Book{ID: loserID, Title: title, FileHash: &hash},
			database.Book{ID: winnerID, Title: title, FileHash: &hash},
		)
	}

	rs := newMergeRaceStore(fixture)

	engine, mock, _ := setupTestEngine(t)
	engine.AutoMergeEnabled = true

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		out := make([]database.Book, len(fixture))
		copy(out, fixture)
		return out, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return rs.get(id), nil
	}
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		return rs.update(id, book)
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		winnerID, ok := hashToWinner[hash]
		if !ok {
			return nil, nil
		}
		return rs.get(winnerID), nil
	}
	// No ISBN/AuthorID set on the fixture books, so checkExactISBN,
	// checkExactTitle, and checkDurationMatch are all no-ops here — this
	// test isolates the checkExactFileHash -> handleFileHashMatch ->
	// MergeBooks path specifically.

	if err := engine.FullScan(context.Background(), nil); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	seenGroups := make(map[string]bool, numPairs)
	for _, pr := range pairs {
		loser := rs.get(pr.loserID)
		winner := rs.get(pr.winnerID)
		if loser == nil || winner == nil {
			t.Fatalf("pair %s/%s: book missing from store after merge", pr.loserID, pr.winnerID)
		}
		if loser.MarkedForDeletion == nil || !*loser.MarkedForDeletion {
			t.Fatalf("pair %s/%s: loser %s was not soft-deleted (merge did not run, or ran on the wrong side)", pr.loserID, pr.winnerID, pr.loserID)
		}
		if winner.MarkedForDeletion != nil && *winner.MarkedForDeletion {
			t.Fatalf("pair %s/%s: winner %s was unexpectedly soft-deleted", pr.loserID, pr.winnerID, pr.winnerID)
		}
		if winner.IsPrimaryVersion == nil || !*winner.IsPrimaryVersion {
			t.Fatalf("pair %s/%s: winner %s is not marked IsPrimaryVersion", pr.loserID, pr.winnerID, pr.winnerID)
		}
		if loser.VersionGroupID == nil || winner.VersionGroupID == nil || *loser.VersionGroupID == "" {
			t.Fatalf("pair %s/%s: version group not set on both sides", pr.loserID, pr.winnerID)
		}
		if *loser.VersionGroupID != *winner.VersionGroupID {
			t.Fatalf("pair %s/%s: version group mismatch loser=%s winner=%s", pr.loserID, pr.winnerID, *loser.VersionGroupID, *winner.VersionGroupID)
		}
		if seenGroups[*winner.VersionGroupID] {
			t.Fatalf("version group %s reused across pairs — cross-pair contamination", *winner.VersionGroupID)
		}
		seenGroups[*winner.VersionGroupID] = true
	}
	if len(seenGroups) != numPairs {
		t.Fatalf("got %d distinct version groups, want %d", len(seenGroups), numPairs)
	}
}
