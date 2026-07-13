// file: internal/plugins/dedup/dataset_backfill_test.go
// version: 1.2.0
// guid: 2f8ff156-b5ec-4480-ac97-27acc54fd013
// last-edited: 2026-07-13

// End-to-end test for the dedup.dataset-backfill op against a real PebbleStore
// + EmbeddingStore, added alongside the CONC-8 memoize-then-parallelize change
// (registry.RunItems over the candidate loop plus a mutex-guarded book-lookup
// cache in memoizedBuilderAdapter).
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// createBookStubAudio (a book whose only file is a sub-256 KiB, zero-duration
// stub, so dataset.Classify's implausibleAudio rule fires "not_dup") is defined
// once in rebuild_gold_labels_test.go and reused here. (missingFile no longer
// emits not_dup — file absence is evidence-free for dup-ness — so a stub side
// is used to exercise the not_dup dismiss path.)

// TestDatasetBackfill_ParallelMatchesSerialOutput exercises the RunItems-backed
// parallel path (CONC-8) with enough candidates to spread across every
// runtime.NumCPU() worker, and with a single "hub" book referenced by every
// candidate so the mutex-guarded book-lookup cache in
// memoizedBuilderAdapter.GetBook is actually contended across goroutines.
// Every candidate's classification is deterministic given the fixture
// (implausibleAudio fires not_dup on the stub side; a normal-duration/size pair
// with no shared signature is left unlabeled), independent of item order or worker count —
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
		stubSide := i%2 == 0
		var leaf string
		if stubSide {
			leaf = createBookStubAudio(t, pebble, fmt.Sprintf("StubLeaf%03d", i))
		} else {
			// Same duration/size as the hub -> duration ratio 1.0, no
			// part-vs-whole or stub signal, and no shared hash -> unlabeled.
			leaf = createBookWithHashedFile(t, pebble, fmt.Sprintf("NormalLeaf%03d", i), fmt.Sprintf("distincthash%04d", i))
		}
		cand := candidateID(t, es, hub, leaf)
		if stubSide {
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
		// not_dup (stub-side) candidates must be suppressed (status -> dismissed).
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

// failingUpsertStore wraps a real *database.EmbeddingStore but forces
// UpsertLabeledExample to fail for a chosen set of candidate IDs. It exists
// to test the dismiss-gate fix: a not_dup candidate whose LabeledExample
// write fails must stay "pending" (never dismissed) so it's retried on the
// next run, and the op must count the failure in upsertErrs.
type failingUpsertStore struct {
	*database.EmbeddingStore
	failIDs map[int64]bool
}

func (f *failingUpsertStore) UpsertLabeledExample(ex database.LabeledExample) error {
	if f.failIDs[ex.CandidateID] {
		return fmt.Errorf("simulated upsert failure")
	}
	return f.EmbeddingStore.UpsertLabeledExample(ex)
}

// capturingReporter records the last UpdateProgress message so the test can
// assert on the human-readable summary (which embeds upsert_errs).
type capturingReporter struct {
	fakeReporter
	lastMsg string
}

func (r *capturingReporter) UpdateProgress(_, _ int, msg string) error {
	r.lastMsg = msg
	return nil
}

// TestDatasetBackfill_ApplyUpsertFailure_NotDismissed is the regression test
// for the fix: before it, a not_dup candidate was dismissed regardless of
// whether UpsertLabeledExample succeeded, so a write failure silently
// dropped the candidate from the pending queue with no LabeledExample ever
// persisted and no error counted. After the fix, the candidate stays
// pending, no LabeledExample is written, and upsert_errs is reported.
func TestDatasetBackfill_ApplyUpsertFailure_NotDismissed(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	hub := createBookWithHashedFile(t, pebble, "FailHub", "failhubhash0001")
	leaf := createBookStubAudio(t, pebble, "FailLeafStub")
	cand := candidateID(t, es, hub, leaf)

	wrapped := &failingUpsertStore{EmbeddingStore: es, failIDs: map[int64]bool{cand: true}}
	rep := &capturingReporter{}

	if err := runDatasetBackfillWith(context.Background(), wrapped, pebble, json.RawMessage(`{"apply":true}`), rep); err != nil {
		t.Fatalf("runDatasetBackfillWith: %v", err)
	}

	// No LabeledExample should have been persisted — the upsert failed.
	ex, err := es.GetLabeledExample(cand)
	if err != nil {
		t.Fatalf("GetLabeledExample(%d): %v", cand, err)
	}
	if ex != nil {
		t.Fatalf("candidate %d: expected no labeled example persisted after upsert failure, got %+v", cand, ex)
	}

	// The candidate must remain pending — NOT dismissed — so it's retried.
	dismissed, _, err := es.ListCandidates(database.CandidateFilter{Status: "dismissed", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("ListCandidates(dismissed): %v", err)
	}
	for _, c := range dismissed {
		if c.ID == cand {
			t.Fatalf("candidate %d: must not be dismissed when its label write failed", cand)
		}
	}
	pending, _, err := es.ListCandidates(database.CandidateFilter{Status: "pending", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("ListCandidates(pending): %v", err)
	}
	found := false
	for _, c := range pending {
		if c.ID == cand {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("candidate %d: expected to remain status=pending after upsert failure", cand)
	}

	// The summary must report the upsert failure.
	if !strings.Contains(rep.lastMsg, "upsert_errs=1") {
		t.Fatalf("expected final summary to report upsert_errs=1, got %q", rep.lastMsg)
	}
}

func TestDatasetBackfill_DryRunWritesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())

	hub := createBookWithHashedFile(t, pebble, "DryHub", "dryhubhash0001")
	leaf := createBookStubAudio(t, pebble, "DryLeafStub")
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
