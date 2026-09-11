// file: internal/server/metadata_candidate_resume_test.go
// version: 1.0.0
// guid: d8016715-746c-43a2-ba58-66617900815d
// last-edited: 2026-09-11

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// TestMetadataCandidateFetch_ResumeSkipsCheckpointedBooks is the test a
// ResumeRestart op with no checkpoint fails. It runs the op, cancels it partway
// through, and then resumes it the way resumeRestart would — with the last
// checkpoint overlaid on the original params — under a DIFFERENT op id, so the
// result-row filter inside Run cannot be what makes the resumed run skip work.
// Only the checkpoint can.
//
// The books do not exist, so every fetch resolves to a "book not found" result
// row without touching a metadata source; the loop, the result write, the
// done-set and the checkpoint cadence are all exercised for real.
func TestMetadataCandidateFetch_ResumeSkipsCheckpointedBooks(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	const n = 60
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("resume-book-%03d", i)
	}
	params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: ids})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	// ---- first attempt: interrupted partway ----
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &resumeRecorder{opID: "op-candidate-resume-1"}
	first.onProgress = func(nth int) {
		// The first call is the "starting" line; every later one is a finished
		// book. Cancel once roughly half the batch has been fetched, so the
		// checkpoint has both a periodic write (cadence 25) and the final one
		// on the way out to prove itself with.
		if nth == 1+n/2 {
			cancel()
		}
	}
	runErr := s.runMetadataCandidateFetchOp(ctx, params, first)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("interrupted run returned %v, want context.Canceled", runErr)
	}

	var ckpt metadataCandidateFetchOpParams
	lastCkpt := first.lastState(t, &ckpt)
	if ckpt.TotalBooks != n {
		t.Fatalf("checkpoint lost the batch size: TotalBooks = %d, want %d", ckpt.TotalBooks, n)
	}
	remaining := idSet(ckpt.BookIDs)
	if len(remaining) == 0 || len(remaining) == n {
		t.Fatalf("checkpoint owes %d of %d books; the interrupt did not land partway", len(remaining), n)
	}

	// Every book with a persisted result row must be OUT of the remaining set,
	// and every book without one must be IN it. A checkpoint that dropped an
	// unfetched book would lose it forever; one that kept a fetched book would
	// spend a provider call again.
	results, err := store.GetOperationResults(first.opID)
	if err != nil {
		t.Fatalf("GetOperationResults: %v", err)
	}
	fetched := map[string]bool{}
	for _, r := range results {
		fetched[r.BookID] = true
	}
	for _, id := range ids {
		switch {
		case fetched[id] && remaining[id]:
			t.Errorf("book %s was fetched but the checkpoint still owes it", id)
		case !fetched[id] && !remaining[id]:
			t.Errorf("book %s was never fetched but the checkpoint dropped it", id)
		}
	}

	// ---- resumed attempt: checkpoint overlaid on params, fresh op id ----
	resumedParams := overlayCheckpoint(t, params, lastCkpt)
	second := &resumeRecorder{opID: "op-candidate-resume-2"}
	if err := s.runMetadataCandidateFetchOp(context.Background(), resumedParams, second); err != nil {
		t.Fatalf("resumed run: %v", err)
	}

	resumedResults, err := store.GetOperationResults(second.opID)
	if err != nil {
		t.Fatalf("GetOperationResults (resumed): %v", err)
	}
	processed := map[string]bool{}
	for _, r := range resumedResults {
		processed[r.BookID] = true
	}
	for id := range processed {
		if !remaining[id] {
			t.Errorf("resumed run re-fetched %s, which the first attempt had already finished", id)
		}
	}
	for id := range remaining {
		if !processed[id] {
			t.Errorf("resumed run never fetched %s, which the checkpoint owed", id)
		}
	}

	// The resumed run's progress bar must start where the first attempt left
	// off and end at the original batch size, not at the size of the remainder.
	if got := second.firstProgress(t); got.current != n-len(remaining) || got.total != n {
		t.Errorf("resumed run started its progress at %d/%d, want %d/%d", got.current, got.total, n-len(remaining), n)
	}
	if got := second.lastProgress(t); got.current != n || got.total != n {
		t.Errorf("resumed run ended its progress at %d/%d, want %d/%d", got.current, got.total, n, n)
	}
}

// TestCandidateFetchCheckpointState_EmptyRemainingIsExplicit pins the field
// shape the overlay depends on: a finished batch must serialise BookIDs as an
// empty list, not omit it, or the base params' original list shows through and
// the resumed run re-fetches everything.
func TestCandidateFetchCheckpointState_EmptyRemainingIsExplicit(t *testing.T) {
	ids := []string{"a", "b"}
	done := newDoneSet(1)
	done.mark("a")
	done.mark("b")
	data, err := json.Marshal(candidateFetchCheckpointState(ids, done, 2))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := raw["book_ids"]
	if !ok {
		t.Fatalf("book_ids was omitted from the checkpoint: %s", data)
	}
	if string(got) != "[]" {
		t.Fatalf("book_ids = %s, want []", got)
	}
}

// TestDoneSet_CadenceAndOrder pins the two properties the checkpoint builders
// rely on: a checkpoint is due exactly on multiples of the cadence (a duplicate
// mark never counts twice), and the remaining set preserves the caller's order
// so the resumed run walks the batch in the sequence the user submitted.
func TestDoneSet_CadenceAndOrder(t *testing.T) {
	all := []string{"a", "b", "c", "d", "e"}
	d := newDoneSet(2)
	if d.mark("c") {
		t.Fatal("first mark should not be due at cadence 2")
	}
	if d.mark("c") {
		t.Fatal("a duplicate mark must not advance the count")
	}
	if !d.mark("a") {
		t.Fatal("second distinct mark should be due at cadence 2")
	}
	got := d.remaining(all)
	want := []string{"b", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("remaining = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remaining = %v, want %v (order must follow the caller's list)", got, want)
		}
	}
}
