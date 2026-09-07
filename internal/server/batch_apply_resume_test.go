// file: internal/server/batch_apply_resume_test.go
// version: 1.1.0
// guid: 2f6b81c4-7d05-4e39-b1a8-93c05e7d264f
// last-edited: 2026-09-07

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
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
	// order is the completion sequence. It is the anti-vacuity evidence: the
	// channel above makes "b5 finished before b3 and b4" a fact of the test
	// rather than a hope about the scheduler, and that IS the gap.
	var order []string

	err := opsregistry.RunItems(context.Background(), rec, ids,
		func(_ context.Context, id string) error {
			if id == "b3" || id == "b4" {
				<-fiveDone
			}
			mu.Lock()
			finished[id] = true
			order = append(order, id)
			mu.Unlock()
			// Release b3/b4 only AFTER b5 has recorded itself. Closing first
			// made the recorded order race the causal one: b3 and b4 woke
			// immediately and could reach the mutex before b5 did, so `order`
			// came out [... b3 b4 b5] on a loaded runner even though b5's work
			// had provably finished first. The channel orders the WORK; this
			// orders the OBSERVATION of it, and the test reads the latter.
			if id == "b5" {
				once.Do(func() { close(fiveDone) })
			}
			return nil
		},
		opsregistry.RunItemsOptions{
			Concurrency:     4,
			ErrMode:         opsregistry.ErrModeCollect,
			CheckpointEvery: 1,
			CheckpointStateFn: func(_ context.Context, watermark int) error {
				st := batchApplyCheckpointState(ids, true, len(ids), watermark, nil)
				mu.Lock()
				dropped := ids[:len(ids)-len(st.BookIDs)]
				for _, d := range dropped {
					if !finished[d] {
						violations = append(violations, d)
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

	// Anti-vacuity: the out-of-order completion really happened. b5 finishing
	// before b3 and b4 is what stalls the watermark at 3, and the channel makes
	// it a fact rather than a hope about the scheduler.
	//
	// This deliberately does NOT assert that a checkpoint was written while the
	// gap was open, which an earlier version of this test did and which CI
	// failed. RunItems suppresses that checkpoint on purpose: maybeCheckpoint
	// returns early on `mark <= lastCkpt` (run_items.go), because a stalled
	// watermark would otherwise rewrite the same value once per completion. So
	// b5's completion writes nothing, and whether any LATER checkpoint's
	// callback observes b5 in `finished` before b3/b4 also land is a race
	// between goroutines reaching ckptMu — near-certain on a developer machine,
	// not on a loaded runner. Asserting it tested the scheduler, not the code.
	//
	// The safety property is checked by the `violations` loop above, which runs
	// against EVERY checkpoint and does not care when they were taken.
	mu.Lock()
	completionOrder := append([]string(nil), order...)
	mu.Unlock()
	fiveAt := slices.Index(completionOrder, "b5")
	switch {
	case fiveAt < 0:
		t.Fatalf("b5 never completed; order = %v", completionOrder)
	case fiveAt > slices.Index(completionOrder, "b3"),
		fiveAt > slices.Index(completionOrder, "b4"):
		t.Fatalf("b5 did not finish ahead of b3/b4, so the watermark never "+
			"stalled behind a gap and the test proved nothing; order = %v",
			completionOrder)
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
	// Both halves run the SAME books through the same options and differ only in
	// what the per-item function returns for "hopeless". Asserting only the
	// returns-nil half would pin nothing: with every item returning nil the
	// watermark reaches the end no matter what the design intends, so the test
	// would pass against an implementation that had lost the property entirely.
	// The returns-error half is what gives the other half its meaning -- it
	// shows the book's fate genuinely hinges on runOne's return value, which is
	// the single line of the op this test is really about.
	const hopeless = "hopeless"
	ids := []string{"good1", hopeless, "good2"}

	run := func(t *testing.T, itemErr error) batchApplyOpParams {
		t.Helper()
		rec := &ckptRecorder{}
		err := opsregistry.RunItems(context.Background(), rec, ids,
			func(_ context.Context, id string) error {
				if id == hopeless {
					return itemErr
				}
				return nil
			},
			opsregistry.RunItemsOptions{
				Concurrency:     1,
				ErrMode:         opsregistry.ErrModeCollect,
				CheckpointEvery: 1,
				CheckpointStateFn: func(_ context.Context, watermark int) error {
					return rec.Checkpoint(batchApplyCheckpointState(ids, true, len(ids), watermark, nil))
				},
			})
		if itemErr == nil && err != nil {
			t.Fatalf("RunItems: %v", err)
		}
		final, ok := rec.last()
		if !ok {
			t.Fatal("no checkpoint written")
		}
		return final
	}

	// The mechanism: a NON-nil return holds the book in the remaining set, so a
	// resume would come back to it on every restart, forever.
	t.Run("returning an error retries the book forever", func(t *testing.T) {
		final := run(t, errors.New("no cached candidates"))
		if !slices.Contains(final.BookIDs, hopeless) {
			t.Fatalf("a failed item left the remaining set (%v); RunItems' watermark no longer "+
				"depends on the return value, so this op's design no longer means what it says",
				final.BookIDs)
		}
	})

	// What the op actually does: runOne counts the per-book outcome and returns
	// nil, so the book advances the watermark and a restart does not retry it.
	// That is the user's requirement -- "don't let one bad item constantly make
	// it fail".
	t.Run("returning nil lets the book leave the set", func(t *testing.T) {
		final := run(t, nil)
		if slices.Contains(final.BookIDs, hopeless) {
			t.Fatalf("a book that could not be applied stayed in the remaining set (%v); "+
				"every restart would retry it forever, which is the failure mode this design removes",
				final.BookIDs)
		}
	})
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
			got := batchApplyCheckpointState(ids, true, 3, tc.watermark, nil)
			if len(got.BookIDs) != tc.wantLeft {
				t.Fatalf("remaining = %v, want %d ids", got.BookIDs, tc.wantLeft)
			}
			if !got.WriteBack {
				t.Error("WriteBack was dropped from the checkpoint")
			}
		})
	}
}

// TestBatchApplyCheckpointState_GateDeferredBooksStayOwed pins the one case
// where the remaining set is NOT just the suffix. A book the write-back gate
// never let through had nothing applied for it — not even the database half —
// but runOne returned nil so the loop could keep going, which puts it below the
// watermark. The suffix alone would drop it silently, which is the exact
// failure this whole PR exists to prevent, one layer down.
func TestBatchApplyCheckpointState_GateDeferredBooksStayOwed(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}

	t.Run("a deferred book below the watermark is carried", func(t *testing.T) {
		// Watermark 3 means a, b and c left the set; b was deferred.
		got := batchApplyCheckpointState(ids, true, 4, 3, []string{"b"})
		want := []string{"d", "b"}
		if !slices.Equal(got.BookIDs, want) {
			t.Fatalf("remaining = %v, want %v", got.BookIDs, want)
		}
	})

	t.Run("a deferred book still in the suffix is not duplicated", func(t *testing.T) {
		// A gap can hold the watermark behind a book that was deferred, so the
		// same id arrives from both sources. Carrying it twice would apply it
		// twice on resume.
		got := batchApplyCheckpointState(ids, true, 4, 1, []string{"c"})
		want := []string{"b", "c", "d"}
		if !slices.Equal(got.BookIDs, want) {
			t.Fatalf("remaining = %v, want %v", got.BookIDs, want)
		}
	})

	t.Run("the caller's slice is not written through", func(t *testing.T) {
		// remaining is a SUBSLICE of ids, so appending to it without cloning
		// would overwrite ids' own backing array past the watermark. Nothing
		// downstream would report that; the corruption would just show up as a
		// wrong resume set on the next checkpoint.
		src := []string{"a", "b", "c", "d"}
		_ = batchApplyCheckpointState(src[:3], true, 4, 2, []string{"a"})
		if src[3] != "d" {
			t.Fatalf("caller's slice was written through: src = %v", src)
		}
	})
}
