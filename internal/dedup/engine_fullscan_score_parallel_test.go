// file: internal/dedup/engine_fullscan_score_parallel_test.go
// version: 1.0.0
// guid: 7e5b9f24-4a3d-4b8e-9c2a-1d6f8e0b3a57
// last-edited: 2026-07-05

// Regression test for CONC-2: FullScan's unified-scoring second pass is now
// sharded across a bounded worker pool (registry.RunItems) instead of running
// single-threaded. This proves the parallel pass persists the EXACT same
// candidate set (Layer/Band/FormulaVersion/Similarity per pair) as an
// independently-run serial reference that calls the same per-book Layer-1 +
// runUnifiedScoringForBook methods directly in a plain `for` loop — i.e. the
// same code path the pre-parallelization loop used. Run under -race to prove
// the shared PebbleStore-backed EmbeddingStore/bookStore have no data race
// under concurrent per-book scoring.

package dedup

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fullScanScoreFixtureBooks builds N books that all share one ISBN13 (and an
// identical title), so checkExactISBN's O(N) scan path writes a pending
// "exact" candidate for every pair, which the unified-scoring pass then
// re-scores (ISBN match is high-confidence, so composed.Band is non-empty
// and the pair is persisted with unified fields). Duration is set above
// minFingerprintMatchSeconds so hasKnownShortDuration/hasPlausibleAudio don't
// suppress the pair, and IsPrimaryVersion is left nil (isNonPrimaryVersion
// treats nil as primary) so upsertExactCandidate's primary gate passes.
func fullScanScoreFixtureBooks(n int) []database.Book {
	isbn := "9780000000042"
	dur := 3600
	books := make([]database.Book, n)
	for i := 0; i < n; i++ {
		books[i] = database.Book{
			ID:       fmt.Sprintf("SB%03d", i),
			Title:    "The Same Scored Book",
			ISBN13:   &isbn,
			Duration: &dur,
		}
	}
	return books
}

// wireFullScanScoreMock points a MockStore's GetAllBooks/GetBookByID at the
// given book set (defensive copies so the engine can never observe test-side
// mutation and vice versa).
func wireFullScanScoreMock(mock *database.MockStore, books []database.Book) {
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
}

// canonicalizeScoredCandidates projects the persisted candidate rows to a
// map keyed by the canonical unordered pair, dropping volatile fields (row
// ID, timestamps) that are allowed to differ between two independent stores
// while everything scoring-relevant must match exactly.
type scoredPair struct {
	layer          string
	band           string
	formulaVersion string
	similarity     float64
}

func canonicalizeScoredCandidates(t *testing.T, cands []database.DedupCandidate) map[string]scoredPair {
	t.Helper()
	out := make(map[string]scoredPair, len(cands))
	for _, c := range cands {
		key := pairKeyFor(c.EntityAID, c.EntityBID)
		if _, dup := out[key]; dup {
			t.Fatalf("duplicate candidate row for pair %s", key)
		}
		var sim float64
		if c.Similarity != nil {
			sim = *c.Similarity
		}
		out[key] = scoredPair{layer: c.Layer, band: c.Band, formulaVersion: c.FormulaVersion, similarity: sim}
	}
	return out
}

// runFullScanLayer1AndScoreSerially replicates the PRE-CONC-2 sequential
// loop bodies directly (Layer 1 checks, then a plain serial for-loop calling
// runUnifiedScoringForBook once per book) to produce the ground-truth serial
// candidate set on an independent engine/store, without duplicating any
// scoring/collector logic.
func runFullScanLayer1AndScoreSerially(t *testing.T, engine *Engine, books []database.Book) {
	t.Helper()
	ctx := context.Background()

	for i := range books {
		book := &books[i]
		authorName := ""
		if _, err := engine.checkExactFileHash(book, authorName); err != nil {
			t.Fatalf("checkExactFileHash(%s): %v", book.ID, err)
		}
		if err := engine.checkExactISBN(book); err != nil {
			t.Fatalf("checkExactISBN(%s): %v", book.ID, err)
		}
		if err := engine.checkExactTitle(book, authorName); err != nil {
			t.Fatalf("checkExactTitle(%s): %v", book.ID, err)
		}
		if err := engine.checkDurationMatch(book); err != nil {
			t.Fatalf("checkDurationMatch(%s): %v", book.ID, err)
		}
	}

	for i := range books {
		book := &books[i]
		if err := engine.runUnifiedScoringForBook(ctx, book, ""); err != nil {
			t.Fatalf("runUnifiedScoringForBook(%s): %v", book.ID, err)
		}
	}
}

// TestParallelFullScanUnifiedScoring_SameResultAsSerial is the CONC-2 parity
// test: FullScan's parallel scoring pass (registry.RunItems, Concurrency ==
// runtime.NumCPU()) must persist the exact same candidate set that a plain
// serial loop over runUnifiedScoringForBook produces for the identical input.
func TestParallelFullScanUnifiedScoring_SameResultAsSerial(t *testing.T) {
	// Sized well above a typical runtime.NumCPU() so the RunItems pool
	// genuinely shards work across multiple goroutines, and every book's
	// candidate set is contended (fully-connected clique via shared ISBN —
	// C(n,2) pairs) so upsertCandidateWithLiveLabel races on the same rows
	// from both directions of each pair.
	const numBooks = 14

	// --- Serial reference: independent engine/store, same fixture. ---
	engineB, mockB, esB := setupTestEngine(t)
	booksB := fullScanScoreFixtureBooks(numBooks)
	wireFullScanScoreMock(mockB, booksB)
	runFullScanLayer1AndScoreSerially(t, engineB, booksB)

	wantCands, _, err := esB.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("serial reference ListCandidates: %v", err)
	}
	want := canonicalizeScoredCandidates(t, wantCands)
	if len(want) == 0 {
		t.Fatalf("fixture is vacuous: expected >0 scored candidate pairs but got 0")
	}
	// Full clique: C(numBooks,2) unordered pairs expected.
	wantPairCount := numBooks * (numBooks - 1) / 2
	if len(want) != wantPairCount {
		t.Fatalf("serial reference produced %d pairs, want %d (C(%d,2)) — fixture assumptions drifted", len(want), wantPairCount, numBooks)
	}

	// --- Parallel: through the real FullScan entry point. ---
	engineA, mockA, esA := setupTestEngine(t)
	booksA := fullScanScoreFixtureBooks(numBooks)
	wireFullScanScoreMock(mockA, booksA)

	type prog struct{ done, total int }
	var progs []prog
	err = engineA.FullScan(context.Background(), func(phase string, done, total int) {
		if phase != "score" {
			return
		}
		progs = append(progs, prog{done, total})
	})
	if err != nil {
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
			t.Fatalf("missing expected scored pair %s (lost update in parallel scan)", key)
		}
		if g != w {
			t.Fatalf("scored pair %s mismatch: parallel=%+v serial=%+v", key, g, w)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected scored pair %s (spurious emit in parallel scan)", key)
		}
	}

	// Progress: "score"-phase callbacks only, final done == total == numBooks,
	// monotonically non-decreasing (mutex-serialized counter in
	// progressCallbackReporter), no lost/duplicated increments.
	if len(progs) == 0 {
		t.Fatal("score-phase progress callback never invoked")
	}
	last := 0
	for i, p := range progs {
		if p.total != numBooks {
			t.Fatalf("progress[%d].total = %d, want %d", i, p.total, numBooks)
		}
		if p.done < last {
			t.Fatalf("progress not monotonic: progress[%d].done=%d < previous %d", i, p.done, last)
		}
		last = p.done
	}
	if last != numBooks {
		t.Fatalf("final score progress done = %d, want %d", last, numBooks)
	}
}
