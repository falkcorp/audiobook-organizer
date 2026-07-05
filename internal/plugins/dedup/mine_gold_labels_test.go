// file: internal/plugins/dedup/mine_gold_labels_test.go
// version: 1.1.0
// guid: c5a71e38-9b20-4d64-8f12-3e6a9c7b2d05
// last-edited: 2026-07-05

// End-to-end test for the dedup.mine-gold-labels op against a real PebbleStore +
// EmbeddingStore: a candidate whose two books share a file hash is labeled
// true_dup/auto_high_conf on apply; a candidate with no shared signal is not.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func createBookWithHashedFile(t *testing.T, pebble *database.PebbleStore, title, fileHash string) string {
	t.Helper()
	b := &database.Book{Title: title, FilePath: "/audio/" + title + ".m4b"}
	created, err := pebble.CreateBook(b)
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	if err := pebble.CreateBookFile(&database.BookFile{
		BookID:   created.ID,
		FilePath: "/audio/" + title + ".m4b",
		FileHash: fileHash,
		FileSize: 5 << 20,
		Duration: 3600,
	}); err != nil {
		t.Fatalf("CreateBookFile %q: %v", title, err)
	}
	return created.ID
}

func candidateID(t *testing.T, es *database.EmbeddingStore, aID, bID string) int64 {
	t.Helper()
	sim := 0.9
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: aID, EntityBID: bID, Layer: "embedding", Similarity: &sim, Status: "pending",
	}); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	ca, cb := aID, bID
	if ca > cb {
		ca, cb = cb, ca
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Status: "pending", Limit: 100})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	for _, c := range cands {
		if c.EntityAID == ca && c.EntityBID == cb {
			return c.ID
		}
	}
	t.Fatal("candidate not found")
	return 0
}

func TestMineGoldLabels_ApplyLabelsSharedHashTrueDup(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	// Pair 1: shared file hash → high-confidence true_dup.
	dupA := createBookWithHashedFile(t, pebble, "MobyA", "sharedhash0000ff")
	dupB := createBookWithHashedFile(t, pebble, "MobyB", "sharedhash0000ff")
	dupCand := candidateID(t, es, dupA, dupB)

	// Pair 2: distinct hashes, no shared id → no signal.
	nonA := createBookWithHashedFile(t, pebble, "OtherC", "hashC1111")
	nonB := createBookWithHashedFile(t, pebble, "OtherD", "hashD2222")
	nonCand := candidateID(t, es, nonA, nonB)

	p := &Plugin{store: pebble, embeddingStore: es}
	if err := p.runMineGoldLabels(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("runMineGoldLabels: %v", err)
	}

	// The shared-hash candidate is labeled true_dup / auto_high_conf.
	ex, err := es.GetLabeledExample(dupCand)
	if err != nil {
		t.Fatalf("GetLabeledExample(dup): %v", err)
	}
	if ex == nil {
		t.Fatal("expected a label for the shared-hash candidate, got nil")
	}
	if ex.Label != "true_dup" || ex.LabelSource != "auto_high_conf" {
		t.Fatalf("label=%q source=%q; want true_dup/auto_high_conf", ex.Label, ex.LabelSource)
	}

	// The no-signal candidate is NOT labeled.
	if ex2, err := es.GetLabeledExample(nonCand); err != nil {
		t.Fatalf("GetLabeledExample(non): %v", err)
	} else if ex2 != nil {
		t.Fatalf("expected no label for the no-signal candidate, got %+v", ex2)
	}
}

func TestMineGoldLabels_DryRunWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	a := createBookWithHashedFile(t, pebble, "MobyA", "samehashaaaa")
	b := createBookWithHashedFile(t, pebble, "MobyB", "samehashaaaa")
	cand := candidateID(t, es, a, b)

	p := &Plugin{store: pebble, embeddingStore: es}
	if err := p.runMineGoldLabels(context.Background(), json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Fatalf("runMineGoldLabels dry-run: %v", err)
	}
	if ex, err := es.GetLabeledExample(cand); err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	} else if ex != nil {
		t.Fatal("dry-run must write nothing")
	}
}

// TestMineGoldLabels_ParallelMatchesSerialOutput exercises the RunItems-backed
// parallel path (CONC-8) with enough candidates to spread across every
// runtime.NumCPU() worker, and with a single "hub" book referenced by every
// candidate so the mutex-guarded book-lookup cache in
// memoizedBuilderAdapter.GetBook actually gets contended across goroutines.
// The op's output (which candidates fire true_dup, counts) is fully
// deterministic given the fixture, independent of item processing order or
// worker count — that determinism IS the "parallel == serial" guarantee, since
// registry.RunItems with Concurrency==1 (serial) and Concurrency>1 (parallel)
// both drive the exact same per-item function. Run with -race to catch any
// unguarded shared state.
func TestMineGoldLabels_ParallelMatchesSerialOutput(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	const numLeaves = 60 // comfortably exceeds runtime.NumCPU() on any CI box
	hub := createBookWithHashedFile(t, pebble, "Hub", "hubsharedhash0001")

	var wantTrueDup, wantUnlabeled []int64
	for i := 0; i < numLeaves; i++ {
		fires := i%2 == 0
		hash := "hubsharedhash0001" // shared with hub -> fires true_dup
		if !fires {
			hash = fmt.Sprintf("distincthash%04d", i) // unique -> no signal
		}
		leaf := createBookWithHashedFile(t, pebble, fmt.Sprintf("Leaf%03d", i), hash)
		cand := candidateID(t, es, hub, leaf)
		if fires {
			wantTrueDup = append(wantTrueDup, cand)
		} else {
			wantUnlabeled = append(wantUnlabeled, cand)
		}
	}

	p := &Plugin{store: pebble, embeddingStore: es}
	if err := p.runMineGoldLabels(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("runMineGoldLabels: %v", err)
	}

	for _, cand := range wantTrueDup {
		ex, err := es.GetLabeledExample(cand)
		if err != nil {
			t.Fatalf("GetLabeledExample(%d): %v", cand, err)
		}
		if ex == nil {
			t.Fatalf("candidate %d: expected true_dup/auto_high_conf label, got nil", cand)
		}
		if ex.Label != "true_dup" || ex.LabelSource != "auto_high_conf" {
			t.Fatalf("candidate %d: label=%q source=%q; want true_dup/auto_high_conf", cand, ex.Label, ex.LabelSource)
		}
	}
	for _, cand := range wantUnlabeled {
		ex, err := es.GetLabeledExample(cand)
		if err != nil {
			t.Fatalf("GetLabeledExample(%d): %v", cand, err)
		}
		if ex != nil {
			t.Fatalf("candidate %d: expected no label (no shared signal), got %+v", cand, ex)
		}
	}
}
