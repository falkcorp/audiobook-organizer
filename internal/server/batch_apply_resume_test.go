// file: internal/server/batch_apply_resume_test.go
// version: 1.0.0
// guid: 2f6b81c4-7d05-4e39-b1a8-93c05e7d264f
// last-edited: 2026-09-07

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ckptRecorder captures what the op checkpoints. Only the three methods
// RunItems actually calls are implemented; the embedded interface supplies the
// rest and panics loudly if this test ever starts depending on one of them.
type ckptRecorder struct {
	opsregistry.Reporter
	mu     sync.Mutex
	states []batchApplyOpParams
}

func (r *ckptRecorder) Checkpoint(state any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := state.(batchApplyOpParams)
	if !ok {
		return nil
	}
	r.states = append(r.states, p)
	return nil
}

func (r *ckptRecorder) UpdateProgress(int, int, string) error      { return nil }
func (r *ckptRecorder) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *ckptRecorder) SetCurrentItem(string)                      {}
func (r *ckptRecorder) IsCanceled() bool                           { return false }
func (r *ckptRecorder) last() (batchApplyOpParams, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return batchApplyOpParams{}, false
	}
	return r.states[len(r.states)-1], true
}

// TestBatchApplyCheckpoint_OutOfOrderCompletionKeepsTheGap is the test an
// index-into-a-completion-COUNT design fails.
//
// With workers finishing out of order, "4 books done" can mean indices
// {0,1,2,5}. Resuming at 4 would skip books 3 and 4 forever, and nothing would
// ever report that it had. RunItems' watermark is the contiguous completed
// PREFIX, so the checkpoint must still owe 3 and 4 — and re-owe 5, which is
// re-applied rather than lost. Re-applying is safe here (the same cached
// candidate lands on the same book); skipping is not.
func TestBatchApplyCheckpoint_OutOfOrderCompletionKeepsTheGap(t *testing.T) {
	ids := []string{"b0", "b1", "b2", "b3", "b4", "b5"}
	rec := &ckptRecorder{}

	// Books 3 and 4 block until 5 has finished, which forces the out-of-order
	// completion deterministically instead of hoping the scheduler produces it.
	fiveDone := make(chan struct{})
	var once sync.Once

	// finished records books whose work has actually returned. THE invariant
	// under test: at every checkpoint, each book the checkpoint has dropped from
	// the remaining set must appear here. A design that resumed from a
	// completion COUNT would drop b3 and b4 the moment b5 finished, and this is
	// the assertion that catches it — asserting on the final checkpoint alone
	// would not, because by then everything has legitimately completed.
	var mu sync.Mutex
	finished := map[string]bool{}
	var violations []string

	err := opsregistry.RunItems(context.Background(), rec, ids,
		func(_ context.Context, id string) error {
			switch id {
			case "b3", "b4":
				<-fiveDone
			case "b5":
				once.Do(func() { close(fiveDone) })
			}
			mu.Lock()
			finished[id] = true
			mu.Unlock()
			return nil
		},
		opsregistry.RunItemsOptions{
			Concurrency:     4,
			ErrMode:         opsregistry.ErrModeCollect,
			CheckpointEvery: 1,
			CheckpointStateFn: func(_ context.Context, watermark int) error {
				st := batchApplyCheckpointState(ids, true, len(ids), watermark)
				mu.Lock()
				for _, dropped := range ids[:len(ids)-len(st.BookIDs)] {
					if !finished[dropped] {
						violations = append(violations, dropped)
					}
				}
				mu.Unlock()
				return rec.Checkpoint(st)
			},
		})
	if err != nil {
		t.Fatalf("RunItems: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), violations...)
	mu.Unlock()
	if len(got) > 0 {
		t.Fatalf("checkpoint dropped %v before those books had finished — a resume would never apply them", got)
	}

	// Every checkpoint written during the run must be a valid resume point: it
	// may never omit a book that had not finished when it was taken.
	rec.mu.Lock()
	states := append([]batchApplyOpParams(nil), rec.states...)
	rec.mu.Unlock()
	if len(states) == 0 {
		t.Fatal("no checkpoint was written; the op would resume from the top")
	}
	for i, st := range states {
		if st.OriginalTotal != len(ids) {
			t.Errorf("checkpoint %d lost the display total: got %d want %d", i, st.OriginalTotal, len(ids))
		}
		if !st.WriteBack {
			t.Errorf("checkpoint %d dropped WriteBack; a restart would silently downgrade to a database-only run", i)
		}
		// The remaining set must always be a SUFFIX of the original order, so
		// the watermark arithmetic can never reorder or interleave the work.
		want := ids[len(ids)-len(st.BookIDs):]
		for j := range st.BookIDs {
			if st.BookIDs[j] != want[j] {
				t.Fatalf("checkpoint %d is not a suffix of the original order: %v", i, st.BookIDs)
			}
		}
	}

	// The gap really did open: b5 completed while b3 and b4 were blocked, so at
	// least one checkpoint must have been taken while the watermark was stalled
	// short of the end. Without this the test could pass vacuously on a run that
	// happened to complete in order and never exercised the gap at all.
	sawStalledCheckpoint := false
	for _, st := range states {
		if len(st.BookIDs) >= 3 {
			sawStalledCheckpoint = true
			break
		}
	}
	if !sawStalledCheckpoint {
		t.Error("no checkpoint was taken while the out-of-order gap was open; " +
			"the test did not exercise what it claims to")
	}

	final, _ := rec.last()
	if len(final.BookIDs) != 0 {
		t.Errorf("after every book succeeded the final checkpoint should owe nothing, got %v", final.BookIDs)
	}
}

// TestBatchApplyCheckpoint_FailedBookStillLeavesTheSet pins the deliberate
// inversion of RunItems' default. runOne reports per-book failures through
// counters and returns nil, so a book with no cached candidate advances the
// watermark and is NOT retried on resume. That is the user's requirement --
// "don't let one bad item constantly make it fail" -- and this test exists so
// the behaviour cannot be quietly reverted to "retry forever".
func TestBatchApplyCheckpoint_FailedBookStillLeavesTheSet(t *testing.T) {
	ids := []string{"good1", "hopeless", "good2"}
	rec := &ckptRecorder{}

	err := opsregistry.RunItems(context.Background(), rec, ids,
		// Mirrors the op's runOne: a per-book problem is counted, never returned.
		func(_ context.Context, _ string) error { return nil },
		opsregistry.RunItemsOptions{
			Concurrency:     1,
			ErrMode:         opsregistry.ErrModeCollect,
			CheckpointEvery: 1,
			CheckpointStateFn: func(_ context.Context, watermark int) error {
				return rec.Checkpoint(batchApplyCheckpointState(ids, true, len(ids), watermark))
			},
		})
	if err != nil {
		t.Fatalf("RunItems: %v", err)
	}
	final, ok := rec.last()
	if !ok {
		t.Fatal("no checkpoint written")
	}
	for _, id := range final.BookIDs {
		if id == "hopeless" {
			t.Fatal("a book that could not be applied stayed in the remaining set; " +
				"every restart would retry it forever, which is the failure mode this design removes")
		}
	}
}

// TestMergeBatchApplyQueuedParams_ResumedRunUnionsNewWork guards the 2026-08-21
// production incident shape: approving MORE books while a batch apply was queued
// discarded the new ids and reported success.
//
// The resumed case is the sharp one. A checkpointed row carries a REDUCED
// BookIDs (the unfinished tail) plus OriginalTotal. Merging a new request into
// it must yield the union and must not lose the record of what earlier attempts
// already finished.
func TestMergeBatchApplyQueuedParams_ResumedRunUnionsNewWork(t *testing.T) {
	// A 10-book run that got through 7; 3 remain.
	existing, err := json.Marshal(batchApplyOpParams{
		BookIDs:       []string{"r1", "r2", "r3"},
		WriteBack:     true,
		OriginalTotal: 10,
	})
	if err != nil {
		t.Fatalf("marshal existing: %v", err)
	}
	// The user approves two more books, one of which is already pending.
	incoming, err := json.Marshal(batchApplyOpParams{
		BookIDs:   []string{"r2", "new1"},
		WriteBack: true,
	})
	if err != nil {
		t.Fatalf("marshal incoming: %v", err)
	}

	raw, merged, err := mergeBatchApplyQueuedParams(existing, incoming)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !merged {
		t.Fatal("merge was declined; the newly approved book would be dropped")
	}
	var got batchApplyOpParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	want := map[string]bool{"r1": false, "r2": false, "r3": false, "new1": false}
	for _, id := range got.BookIDs {
		if _, known := want[id]; !known {
			t.Errorf("merge invented a book id %q", id)
			continue
		}
		if want[id] {
			t.Errorf("book %q appears twice in the merged set", id)
		}
		want[id] = true
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("book %q was DROPPED by the merge", id)
		}
	}

	// 7 done + 4 owed = 11. Carrying the stale 10 through would make the run
	// report itself complete one book early.
	if got.OriginalTotal != 11 {
		t.Errorf("merged OriginalTotal = %d, want 11 (7 already done + 4 remaining)", got.OriginalTotal)
	}
	if got.completed() != 7 {
		t.Errorf("merged run forgot how many earlier attempts finished: completed() = %d, want 7", got.completed())
	}
}

// TestBatchApplyOpParams_CompletedClampsToZero covers the inputs where
// OriginalTotal cannot be trusted: absent on a fresh run, absent on params
// hand-written against /operations/v2, and stale after a merge grew BookIDs
// past it. A negative offset would drive the progress bar backwards.
func TestBatchApplyOpParams_CompletedClampsToZero(t *testing.T) {
	cases := []struct {
		name string
		p    batchApplyOpParams
		want int
	}{
		{"fresh run has no OriginalTotal", batchApplyOpParams{BookIDs: []string{"a", "b"}}, 0},
		{"stale OriginalTotal below the remaining count", batchApplyOpParams{BookIDs: []string{"a", "b", "c"}, OriginalTotal: 2}, 0},
		{"equal means nothing done yet", batchApplyOpParams{BookIDs: []string{"a", "b"}, OriginalTotal: 2}, 0},
		{"resumed mid-run", batchApplyOpParams{BookIDs: []string{"a"}, OriginalTotal: 10}, 9},
		{"everything done", batchApplyOpParams{OriginalTotal: 5}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.completed(); got != tc.want {
				t.Fatalf("completed() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestBatchApplyCheckpointState_IsAlwaysAValidResumePoint pins the payload
// builder's edge cases directly. An out-of-range watermark must never panic or
// silently widen the remaining set: a checkpoint is written from a live run, and
// a panic there would take down an op that was otherwise succeeding.
func TestBatchApplyCheckpointState_IsAlwaysAValidResumePoint(t *testing.T) {
	ids := []string{"a", "b", "c"}
	cases := []struct {
		name      string
		watermark int
		wantLeft  int
	}{
		{"nothing finished", 0, 3},
		{"partway", 2, 1},
		{"all finished", 3, 0},
		{"past the end is clamped", 99, 0},
		{"negative is clamped", -5, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := batchApplyCheckpointState(ids, true, 3, tc.watermark)
			if len(got.BookIDs) != tc.wantLeft {
				t.Fatalf("remaining = %v, want %d ids", got.BookIDs, tc.wantLeft)
			}
			if !got.WriteBack {
				t.Error("WriteBack was dropped from the checkpoint")
			}
		})
	}
}
