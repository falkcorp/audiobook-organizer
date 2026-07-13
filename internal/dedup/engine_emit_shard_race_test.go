// file: internal/dedup/engine_emit_shard_race_test.go
// version: 1.0.1
// guid: 7e1f2a93-4b6c-4d5e-8f01-2a3b4c5d6e7f
// last-edited: 2026-07-12

// Race/invariant tests for CONC-3 (INIT-2 T5): the full-scan emit() no longer
// runs under one global mutex. Per-pair "already handled" state is sharded
// across acoustidEmitShardCount mutexes keyed by the canonical pair-key hash,
// so the runtime.NumCPU() RunItems worker pool no longer serializes behind a
// single lock, while the check-then-set stays atomic PER PAIR.
//
// The two low-level tests exercise emitShards.mark() directly — that is the
// EXACT check-then-set primitive emit() uses — so the 64-goroutine same-pair
// proof and the shard-collision no-loss proof transfer to the real emit path.
// The two scan-level tests confirm the wiring end to end: a mutual A<->B
// fixture makes both books' workers emit the same canonical pair concurrently
// (the maximum same-pair contention reachable through the scan, since only
// A's worker emits (A,B) and only B's emits (B,A)), and a ghost-target
// fixture pins the nil/unknown gate semantics under the new locking.
//
// TestParallelAcoustIDScan_SameCandidatesAsSerial (engine_acoustid_parallel_
// test.go) already covers the distinct-pair no-loss / four-gate case at
// 60-way parallelism; these tests add the per-pair single-emission proof.
package dedup

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// makeUsefulFP builds a base64 chromaprint payload that
// fingerprint.IsUsefulFingerprint accepts: a 4-byte header followed by
// >= MinUsefulFingerprintFrames little-endian uint32 frames. Distinct seeds
// yield distinct fingerprint strings so a mock's exact-match lookup can map
// each book's segment to a different partner file.
func makeUsefulFP(seed byte) string {
	const frames = 96 // comfortably above MinUsefulFingerprintFrames (80)
	buf := make([]byte, 4+frames*4)
	for i := range buf {
		buf[i] = seed + byte(i*7)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// TestAcoustidEmitShards_MarkSamePairClaimedOnce hammers a SINGLE pair key
// from 64 goroutines released simultaneously and asserts mark() returns true
// exactly once. This is the per-pair check-then-set atomicity invariant: same
// key -> same shard -> no two workers can both observe the pair as unhandled.
// A regression (e.g. removing the shard lock, or splitting check and set)
// would let mark() return true more than once, which in emit() is a
// double-upsert / double-emit. Runs under -race.
func TestAcoustidEmitShards_MarkSamePairClaimedOnce(t *testing.T) {
	const goroutines = 64
	shards := newAcoustidEmitShards()
	const key = "BOOK_A:BOOK_B"

	var trues atomic.Int64
	var start sync.WaitGroup
	start.Add(1) // gate so every goroutine contends at once
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			if shards.mark(key) {
				trues.Add(1)
			}
		}()
	}
	start.Done()
	wg.Wait()

	if got := trues.Load(); got != 1 {
		t.Fatalf("same pair key claimed %d times, want exactly 1 (double-emit under contention)", got)
	}
	if got := shards.count(); got != 1 {
		t.Fatalf("count()=%d, want 1", got)
	}
}

// TestAcoustidEmitShards_DistinctPairsNoLoss proves the anti-over-suppression
// side: many DISTINCT keys (far more than acoustidEmitShardCount, so keys are
// guaranteed to collide on shared shards) each claimed by several racing
// goroutines. Every distinct key must be claimed exactly once (no lost
// emission from a shard collision), and count() must equal the number of
// distinct keys. Runs under -race.
func TestAcoustidEmitShards_DistinctPairsNoLoss(t *testing.T) {
	const numKeys = 200 // >> acoustidEmitShardCount (16): forces shard sharing
	const perKey = 4    // goroutines racing to claim each key

	shards := newAcoustidEmitShards()
	keys := make([]string, numKeys)
	trues := make([]atomic.Int64, numKeys)

	var wg sync.WaitGroup
	for i := 0; i < numKeys; i++ {
		keys[i] = fmt.Sprintf("A%04d:B%04d", i, i)
		for g := 0; g < perKey; g++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				if shards.mark(keys[idx]) {
					trues[idx].Add(1)
				}
			}(i)
		}
	}
	wg.Wait()

	for i := 0; i < numKeys; i++ {
		if got := trues[i].Load(); got != 1 {
			t.Fatalf("key %s claimed %d times, want exactly 1 (lost or duplicated emission)", keys[i], got)
		}
	}
	if got := shards.count(); got != numKeys {
		t.Fatalf("count()=%d, want %d (shard collision dropped distinct entries)", got, numKeys)
	}
}

// TestAcoustidEmitShards_ShardForStable pins the property that makes the
// invariant hold: the same key always resolves to the same shard.
func TestAcoustidEmitShards_ShardForStable(t *testing.T) {
	shards := newAcoustidEmitShards()
	for _, k := range []string{"", "A:B", "BOOK_A:BOOK_B", "zzz:aaa"} {
		// Two separate calls with the same key must resolve identically
		// (determinism). Bound to distinct locals so the equality check reads as
		// a genuine cross-call comparison rather than a tautology.
		first, second := shards.shardFor(k), shards.shardFor(k)
		if first != second {
			t.Fatalf("shardFor(%q) is not stable across calls", k)
		}
	}
}

// TestAcoustIDScan_ConcurrentSamePair_SingleCandidate drives the real scan
// with a mutual A<->B fixture: A's segment exact-matches B's file and B's
// segment exact-matches A's file, so A's worker emits (A,B) while B's worker
// emits (B,A) — the same canonical pair — concurrently through
// registry.RunItems. Exactly one candidate must be stored. Runs under -race.
func TestAcoustIDScan_ConcurrentSamePair_SingleCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	fpA := makeUsefulFP(1)
	fpB := makeUsefulFP(2)
	if fpA == fpB || !fingerprint.IsUsefulFingerprint(fpA) || !fingerprint.IsUsefulFingerprint(fpB) {
		t.Fatalf("fixture fingerprints drifted: fpA==fpB=%v useful(A)=%v useful(B)=%v",
			fpA == fpB, fingerprint.IsUsefulFingerprint(fpA), fingerprint.IsUsefulFingerprint(fpB))
	}

	bookA := database.Book{ID: "BOOK_A", Title: "Book A"}
	bookB := database.Book{ID: "BOOK_B", Title: "Book B"}
	fileA := database.BookFile{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/book.m4b", AcoustIDSeg0: fpA}
	fileB := database.BookFile{ID: "FILE_B", BookID: "BOOK_B", FilePath: "/lib/b/book.m4b", AcoustIDSeg0: fpB}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return []database.Book{bookA, bookB}, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "BOOK_A":
			return []database.BookFile{fileA}, nil
		case "BOOK_B":
			return []database.BookFile{fileB}, nil
		}
		return nil, nil
	}
	// A's segment resolves to B's file and vice versa, so both workers emit
	// the SAME canonical pair (BOOK_A, BOOK_B) at the same time.
	mock.GetBookFileByAcoustIDFunc = func(fp string) (*database.BookFile, error) {
		switch fp {
		case fpA:
			return &fileB, nil
		case fpB:
			return &fileA, nil
		}
		return nil, nil
	}

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book", Layer: "acoustid", Status: "pending", Limit: 1_000_000,
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("same pair emitted concurrently from both workers produced %d candidates, want exactly 1: %+v", len(cands), cands)
	}
	if key := pairKeyFor(cands[0].EntityAID, cands[0].EntityBID); key != pairKeyFor("BOOK_A", "BOOK_B") {
		t.Fatalf("candidate pair = %s, want BOOK_A:BOOK_B", key)
	}
}

// TestAcoustIDScan_NilAndUnknownGatesDoNotSuppress pins the conservative
// nil/unknown semantics under the new locking: when a match target is NOT in
// the scanned book slice (a "ghost" reachable only via the exact-match index),
// bookForIdentifierGate's fallback returns nil and identifiersConflict(book,
// nil) is false (never blocks), and parentDirForBook returns "" (unknown) so
// the same-directory suppression never fires. The pair must still be emitted.
func TestAcoustIDScan_NilAndUnknownGatesDoNotSuppress(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	fpA := makeUsefulFP(3)
	if !fingerprint.IsUsefulFingerprint(fpA) {
		t.Fatalf("fixture fingerprint drifted: not useful")
	}

	bookA := database.Book{ID: "BOOK_A", Title: "Book A"}
	fileA := database.BookFile{ID: "FILE_A", BookID: "BOOK_A", FilePath: "/lib/a/book.m4b", AcoustIDSeg0: fpA}
	// GHOST is the match target but is NOT returned by GetAllBooks, so it is
	// never pre-seeded into booksByID/parentDirCache — it forces the lazy
	// store fallbacks that must return nil / "" without suppressing.
	ghostFile := database.BookFile{ID: "FILE_GHOST", BookID: "GHOST", FilePath: "/lib/ghost/book.m4b", AcoustIDSeg0: fpA}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return []database.Book{bookA}, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "BOOK_A":
			return []database.BookFile{fileA}, nil
		case "GHOST":
			return nil, nil // unknown parent dir -> "" -> must NOT suppress
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return nil, nil // GHOST un-fetchable -> nil gate book -> must NOT block
	}
	mock.GetBookFileByAcoustIDFunc = func(fp string) (*database.BookFile, error) {
		if fp == fpA {
			return &ghostFile, nil
		}
		return nil, nil
	}

	if err := engine.AcoustIDScan(context.Background(), nil); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book", Layer: "acoustid", Status: "pending", Limit: 1_000_000,
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("nil-gate/unknown-dir pair should still emit: got %d candidates, want 1: %+v", len(cands), cands)
	}
	if key := pairKeyFor(cands[0].EntityAID, cands[0].EntityBID); key != pairKeyFor("BOOK_A", "GHOST") {
		t.Fatalf("candidate pair = %s, want BOOK_A:GHOST", key)
	}
}
