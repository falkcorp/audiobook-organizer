// file: internal/dedup/engine_booksig_parallel_test.go
// version: 1.0.0
// guid: 3c9e7d21-6b48-4f0a-9d2e-8a1f5c04b7e6
// last-edited: 2026-07-05

// Regression tests for CONC-1: BookSignatureScan's O(n²) pairwise loop is now
// sharded across a bounded worker pool (registry.RunItems) instead of running
// single-threaded. These tests prove the parallel pass emits the EXACT same
// candidate set as an independent serial reference (no lost/duplicated updates
// through the mutex-guarded `emitted` map + DB write) and that the progress
// callback stays monotonic with no lost increments. Run under -race to prove
// the shared-state guarding is correct.

package dedup

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// sigWord builds a valid base64 book signature whose 4096 uint32 words are all
// equal to word. Two signatures built from the same word are identical (sim
// 1.0); signatures from words with a large Hamming distance fall well below
// FuzzyMinSimilarity. Empty mask => all-real => overlap == 4096 (>= 512).
func sigWord(word uint32) string {
	buf := make([]byte, fingerprint.BookSignatureFixedLength*4)
	for i := 0; i < fingerprint.BookSignatureFixedLength; i++ {
		binary.LittleEndian.PutUint32(buf[i*4:], word)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// pairKeyFor mirrors BookSignatureScan's canonical unordered pair key.
func pairKeyFor(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}

// serialReferencePairs recomputes the candidate pair set with a plain O(n²)
// nested loop over the SAME books the engine scans, replicating the engine's
// overlap>=512 and sim>=FuzzyMinSimilarity gates exactly. This is the ground
// truth the parallel pass must match.
func serialReferencePairs(t *testing.T, books []database.Book) map[string]struct{} {
	t.Helper()
	const minOverlapWords = 512
	want := make(map[string]struct{})
	for i := range books {
		sigA := *books[i].BookSigV1
		maskA := ""
		if books[i].BookSigV1Mask != nil {
			maskA = *books[i].BookSigV1Mask
		}
		for j := i + 1; j < len(books); j++ {
			sigB := *books[j].BookSigV1
			maskB := ""
			if books[j].BookSigV1Mask != nil {
				maskB = *books[j].BookSigV1Mask
			}
			sim, overlap, err := fingerprint.BookSignatureSimilarityMasked(sigA, sigB, maskA, maskB)
			if err != nil || overlap < minOverlapWords {
				continue
			}
			if sim >= fingerprint.FuzzyMinSimilarity {
				want[pairKeyFor(books[i].ID, books[j].ID)] = struct{}{}
			}
		}
	}
	return want
}

func TestParallelBookSignatureScan_SameCandidatesAsSerial(t *testing.T) {
	// MockStore-backed engine: feeds the fixture books (with signatures intact)
	// straight into the scan via GetAllBooksFunc. This avoids the memdb read
	// projection (which strips BookSigV1) and exercises the exact emit/guard
	// path under test. Candidates land in the real EmbeddingStore (es).
	engine, mock, es := setupTestEngine(t)

	// Fixture sized well above NumCPU so the outer loop genuinely shards and
	// many emit() calls contend on the guarded map/DB write. 4 groups of 10
	// books, each group sharing one signature => 4*C(10,2)=180 matching pairs;
	// cross-group signatures are far below FuzzyMinSimilarity, so they never
	// emit. Distinct group words guarantee low cross-group similarity.
	groupWords := []uint32{0x00000000, 0xFFFFFFFF, 0x0000FFFF, 0xFFFF0000}
	const perGroup = 10
	var books []database.Book
	byID := make(map[string]database.Book)
	for g, word := range groupWords {
		sig := sigWord(word)
		for k := 0; k < perGroup; k++ {
			id := fmt.Sprintf("g%d-%02d", g, k)
			b := database.Book{ID: id, Title: "Book " + id, BookSigV1: &sig}
			books = append(books, b)
			byID[id] = b
		}
	}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		// Return a defensive copy so the scan can never observe test-side
		// mutation and vice-versa.
		out := make([]database.Book, len(books))
		copy(out, books)
		return out, nil
	}
	// captureLiveLabel -> dataset.BuildExample reads books by ID; provide them
	// so live-capture doesn't error (best-effort, but keeps the path realistic).
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if b, ok := byID[id]; ok {
			return &b, nil
		}
		return nil, nil
	}

	// Ground truth: independent serial nested loop over the exact scanned set.
	scanned, err := engine.getAllBooks()
	if err != nil {
		t.Fatalf("getAllBooks: %v", err)
	}
	withSig := scanned[:0:0]
	for _, b := range scanned {
		if b.BookSigV1 != nil && *b.BookSigV1 != "" {
			withSig = append(withSig, b)
		}
	}
	want := serialReferencePairs(t, withSig)
	if len(want) == 0 {
		t.Fatalf("fixture is vacuous: expected >0 matching pairs but got 0")
	}

	// Record every progress callback to prove the atomic counter is monotonic
	// with no lost increments.
	type prog struct{ done, total int }
	var progs []prog

	if err := engine.BookSignatureScan(context.Background(), func(done, total int) {
		progs = append(progs, prog{done, total})
	}); err != nil {
		t.Fatalf("BookSignatureScan: %v", err)
	}

	// Exact candidate-set equality (both directions). Limit high so pagination
	// (default 50) can't truncate the result.
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	got := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		key := pairKeyFor(c.EntityAID, c.EntityBID)
		if _, dup := got[key]; dup {
			t.Fatalf("duplicate candidate emitted for pair %s", key)
		}
		got[key] = struct{}{}
	}

	if len(got) != len(want) {
		t.Fatalf("candidate count mismatch: got %d, want %d", len(got), len(want))
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing expected candidate pair %s (lost update in parallel scan)", key)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected candidate pair %s (spurious emit in parallel scan)", key)
		}
	}

	// Progress: at least one call, final done == total, monotonic non-decreasing,
	// and total constant == number of scanned books.
	if len(progs) == 0 {
		t.Fatal("progress callback never invoked")
	}
	total := len(withSig)
	last := 0
	for i, p := range progs {
		if p.total != total {
			t.Fatalf("progress[%d].total = %d, want %d", i, p.total, total)
		}
		if p.done < last {
			t.Fatalf("progress not monotonic: progress[%d].done=%d < previous %d", i, p.done, last)
		}
		last = p.done
	}
	if last != total {
		t.Fatalf("final progress done = %d, want %d", last, total)
	}
}
