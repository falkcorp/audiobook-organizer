// file: internal/plugins/dedup/dataset_backfill_test.go
// version: 1.0.0
// guid: 2f8ff156-b5ec-4480-ac97-27acc54fd013
// last-edited: 2026-07-05

// End-to-end test for the dedup.dataset-backfill op against a real PebbleStore
// + EmbeddingStore, added alongside the CONC-8 memoize-then-parallelize change
// (registry.RunItems over the candidate loop plus a mutex-guarded book-lookup
// cache in memoizedBuilderAdapter).
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// createBookNoFiles (a book with zero BookFile rows, so BookFeatures.FilesExist
// is false and dataset.Classify's missingFile rule fires "not_dup") is defined
// once in rebuild_gold_labels_test.go and reused here.

// TestDatasetBackfill_ParallelMatchesSerialOutput exercises the RunItems-backed
// parallel path (CONC-8) with enough candidates to spread across every
// runtime.NumCPU() worker, and with a single "hub" book referenced by every
// candidate so the mutex-guarded book-lookup cache in
// memoizedBuilderAdapter.GetBook is actually contended across goroutines.
// Every candidate's classification is deterministic given the fixture
// (missingFile fires not_dup; a normal-duration/size pair with no shared
// signature is left unlabeled), independent of item order or worker count —
// that determinism is the "parallel == serial" guarantee, since
// registry.RunItems with Concurrency==1 and Concurrency>1 both drive the same
// per-item function. Run with -race to catch any unguarded shared state.
func TestDatasetBackfill_ParallelMatchesSerialOutput(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	const numLeaves = 60 // comfortably exceeds runtime.NumCPU() on any CI box
	hub := createBookWithHashedFile(t, pebble, "BackfillHub", "backfillhubhash0001")

	var wantNotDup, wantUnlabeled []int64
	for i := 0; i < numLeaves; i++ {
		missingFiles := i%2 == 0
		var leaf string
		if missingFiles {
			leaf = createBookNoFiles(t, pebble, fmt.Sprintf("NoFileLeaf%03d", i))
		} else {
			// Same duration/size as the hub -> duration ratio 1.0, no
			// part-vs-whole or stub signal, and no shared hash -> unlabeled.
			leaf = createBookWithHashedFile(t, pebble, fmt.Sprintf("NormalLeaf%03d", i), fmt.Sprintf("distincthash%04d", i))
		}
		cand := candidateID(t, es, hub, leaf)
		if missingFiles {
			wantNotDup = append(wantNotDup, cand)
		} else {
			wantUnlabeled = append(wantUnlabeled, cand)
		}
	}

	p := &Plugin{store: pebble, embeddingStore: es}
	if err := p.runDatasetBackfill(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("runDatasetBackfill: %v", err)
	}

	for _, cand := range wantNotDup {
		ex, err := es.GetLabeledExample(cand)
		if err != nil {
			t.Fatalf("GetLabeledExample(%d): %v", cand, err)
		}
		if ex == nil {
			t.Fatalf("candidate %d: expected not_dup/rule label, got nil", cand)
		}
		if ex.Label != "not_dup" || ex.LabelSource != "rule" {
			t.Fatalf("candidate %d: label=%q source=%q; want not_dup/rule", cand, ex.Label, ex.LabelSource)
		}
		// missingFile candidates must be suppressed (status -> dismissed).
		cands, _, err := es.ListCandidates(database.CandidateFilter{Status: "dismissed", Limit: 1_000_000})
		if err != nil {
			t.Fatalf("ListCandidates(dismissed): %v", err)
		}
		found := false
		for _, c := range cands {
			if c.ID == cand {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("candidate %d: expected status=dismissed after apply", cand)
		}
	}

	for _, cand := range wantUnlabeled {
		ex, err := es.GetLabeledExample(cand)
		if err != nil {
			t.Fatalf("GetLabeledExample(%d): %v", cand, err)
		}
		if ex == nil {
			t.Fatalf("candidate %d: expected an unlabeled example row to still be written, got nil", cand)
		}
		if ex.Label != "" {
			t.Fatalf("candidate %d: expected no rule label, got label=%q source=%q", cand, ex.Label, ex.LabelSource)
		}
	}
}

func TestDatasetBackfill_DryRunWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	hub := createBookWithHashedFile(t, pebble, "DryHub", "dryhubhash0001")
	leaf := createBookNoFiles(t, pebble, "DryLeafNoFiles")
	cand := candidateID(t, es, hub, leaf)

	p := &Plugin{store: pebble, embeddingStore: es}
	if err := p.runDatasetBackfill(context.Background(), json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Fatalf("runDatasetBackfill dry-run: %v", err)
	}
	if ex, err := es.GetLabeledExample(cand); err != nil {
		t.Fatalf("GetLabeledExample: %v", err)
	} else if ex != nil {
		t.Fatal("dry-run must write nothing")
	}
}
