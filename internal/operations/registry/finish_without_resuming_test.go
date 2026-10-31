// file: internal/operations/registry/finish_without_resuming_test.go
// version: 1.0.0
// guid: 4e9a2d17-6b3c-48f5-9a1e-c5d70b82f364
// last-edited: 2026-10-30

package registry

// "Finish without resuming" (05-PR5-FINISH-ON-DECLINE): the third decision on
// a v3 run in awaiting_decision. A ResumeAsk run stopped by a shutdown skips
// its Finish; this decision runs it, once, over exactly the rows the stored
// ledger records, in an attempt that leases nothing, and ends the run.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// TestBatchRunner_FinishOnlyRunsFinishOverTheStoredLedger: after an attempt
// that completed chunks 0 and 1 of 4 and was stopped, a finish-only attempt
// runs Finish once with the Summary and marks those two chunks recorded, calls
// no Item, never reads the source, and leaves the stored ledger as it was.
func TestBatchRunner_FinishOnlyRunsFinishOverTheStoredLedger(t *testing.T) {
	store := newMemLedgerStore(t)
	const size = 10
	job, rc, cancel := openJob(t, seq(4*size), batchOpts{size: size, workers: 1},
		func(irc *ops.ItemRC, i int) error {
			irc.Mark("applied")
			return nil
		})
	defer cancel()
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op-test", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool { return len(bitsSet(l)) == 2 }})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
	}
	before, _ := store.GetLedgerV3("op-test")
	wantBits := bitsSet(*before)
	if !slices.Equal(wantBits, []int{0, 1}) {
		t.Fatalf("setup: ledger %v, want chunks 0 and 1", wantBits)
	}

	var items, finishes atomic.Int32
	var got ops.Summary
	job2, rc2, cancel2 := openJob(t, seq(4*size), batchOpts{size: size, workers: 1,
		finish: func(_ *ops.RC, s ops.Summary) { finishes.Add(1); got = s }},
		func(*ops.ItemRC, int) error { items.Add(1); return nil })
	defer cancel2()
	pagesCalled := false
	job2.Pages = func(context.Context, string, int) (ops.ItemSet, string, error) {
		pagesCalled = true
		return nil, "", errors.New("the source must not be read")
	}
	job2.Items = nil
	_, _, err = runBatch(rc2.Context(), batchConfig{opID: "op-test", job: job2, rc: rc2, store: store, finishOnly: true})
	if err != nil {
		t.Fatalf("finish-only attempt: %v", err)
	}
	if items.Load() != 0 || pagesCalled {
		t.Fatalf("finish-only attempt ran %d items (source read: %v), want none", items.Load(), pagesCalled)
	}
	if finishes.Load() != 1 {
		t.Fatalf("Finish ran %d times, want once", finishes.Load())
	}
	want := make([]string, 0, 2*size)
	for i := range 2 * size {
		want = append(want, itemKey(i))
	}
	if !slices.Equal(got.Marked("applied"), want) {
		t.Fatalf("Finish saw marks %v, want exactly the %d rows of chunks 0 and 1", got.Marked("applied"), len(want))
	}
	if got.Done != 2*size || got.Total != 4*size || got.ChunksDone != 2 || got.ChunksTotal != 4 || got.Canceled {
		t.Fatalf("Summary = %+v, want done 20/40, chunks 2/4, not canceled", got)
	}
	after, _ := store.GetLedgerV3("op-test")
	if !slices.Equal(bitsSet(*after), wantBits) || after.Items != before.Items {
		t.Fatalf("the finish-only attempt changed the stored ledger: %+v -> %+v", before, after)
	}
}

// TestBatchRunner_FinishOnlyWithNoLedgerFinishesEmpty: a run that never
// completed a chunk has no ledger; the finish-only attempt still runs Finish,
// over an empty Summary, and commits nothing.
func TestBatchRunner_FinishOnlyWithNoLedgerFinishesEmpty(t *testing.T) {
	store := newMemLedgerStore(t)
	var items, finishes atomic.Int32
	var got ops.Summary
	job, rc, cancel := openJob(t, seq(20), batchOpts{size: 10, workers: 1,
		finish: func(_ *ops.RC, s ops.Summary) { finishes.Add(1); got = s }},
		func(*ops.ItemRC, int) error { items.Add(1); return nil })
	defer cancel()
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op-test", job: job, rc: rc, store: store, finishOnly: true}); err != nil {
		t.Fatal(err)
	}
	if items.Load() != 0 || finishes.Load() != 1 || got.Done != 0 || len(got.Marks) != 0 {
		t.Fatalf("items %d finishes %d summary %+v, want Finish once over an empty Summary", items.Load(), finishes.Load(), got)
	}
	if l, _ := store.GetLedgerV3("op-test"); l != nil {
		t.Fatalf("a finish-only attempt with no ledger committed one: %+v", l)
	}
}

// TestBatchRunner_FinishOnlyStoppedForResumeSkipsFinish: a shutdown that is
// stopping the finish-only attempt itself leaves Finish to the next decision,
// as for any attempt the registry will resume.
func TestBatchRunner_FinishOnlyStoppedForResumeSkipsFinish(t *testing.T) {
	store := newMemLedgerStore(t)
	var finishes atomic.Int32
	job, rc, cancel := openJob(t, seq(20), batchOpts{size: 10, workers: 1,
		finish: func(*ops.RC, ops.Summary) { finishes.Add(1) }},
		func(*ops.ItemRC, int) error { return nil })
	cancel()
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op-test", job: job, rc: rc, store: store, finishOnly: true,
		resumes: func() bool { return true }})
	if err == nil || finishes.Load() != 0 {
		t.Fatalf("err %v, Finish ran %d times; want the attempt to end stopped with Finish skipped", err, finishes.Load())
	}
}

// finishDecisionHarness is a native Batch with the ResumeAsk policy over four
// chunks of ten items, every item marking "applied". While block is set, items
// of chunk 2 and later park until the run is stopped. Finish counts its calls
// and keeps the marks it saw.
type finishDecisionHarness struct {
	block         atomic.Bool
	blockFinish   atomic.Bool
	items         *counts
	finishes      atomic.Int32
	finishStarted chan struct{}
	startedOnce   sync.Once
	mu            sync.Mutex
	marked        []string
}

func newFinishHarness() *finishDecisionHarness {
	return &finishDecisionHarness{items: newCounts(), finishStarted: make(chan struct{})}
}

const finishDecisionDef = "test.finish-decision"

func (h *finishDecisionHarness) def() ops.Definition {
	ask := ops.ResumeAsk
	return ops.Batch(finishDecisionDef, ops.BatchSpec[ops.NoParams, int, ops.NoResult]{
		Title:      "finish decision",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Resume:     &ask,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[int], error) {
			return ops.Source[int]{Items: seq(40), Key: itemKey}, nil
		},
		Item: func(rc *ops.ItemRC, _ ops.NoParams, i int) error {
			h.items.add(i)
			if h.block.Load() && i/10 >= 2 {
				<-rc.Context().Done()
				return rc.Context().Err()
			}
			rc.Mark("applied")
			return nil
		},
		Finish: func(rc *ops.RC, _ ops.NoParams, s ops.Summary) (ops.NoResult, error) {
			h.finishes.Add(1)
			h.mu.Lock()
			h.marked = s.Marked("applied")
			h.mu.Unlock()
			if h.blockFinish.Load() {
				h.startedOnce.Do(func() { close(h.finishStarted) })
				<-rc.Context().Done()
				return ops.NoResult{}, rc.Context().Err()
			}
			return ops.NoResult{}, nil
		},
		Concurrency: ops.Network(1, "chunks run in order"),
		ChunkSize:   10,
	})
}

func (h *finishDecisionHarness) seenMarks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.marked)
}

func finishRegistry(t *testing.T, store *database.PebbleStore, h *finishDecisionHarness) *Registry {
	t.Helper()
	r := NewWithOptions(store, slog.Default(), 2, Options{WatchdogInterval: time.Hour, SweepInterval: time.Hour})
	if err := Register(r, "test", h.def()); err != nil {
		t.Fatal(err)
	}
	return r
}

func shutdownRegistry(t *testing.T, r *Registry) {
	t.Helper()
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	if err := r.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func awaitRunState(t *testing.T, store *database.PebbleStore, id string, want state.V3) *database.RunRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if rec, err := store.GetRunV3(id); err == nil && rec != nil && rec.State == want {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, _ := store.GetRunV3(id)
	t.Fatalf("run %s never reached %s: %+v", id, want, rec)
	return nil
}

// stopAfterTwoChunks runs the harness def on a fresh registry over store until
// chunks 0 and 1 are in the ledger, then shuts it down: the run is left
// interrupted with its Finish skipped.
func stopAfterTwoChunks(t *testing.T, store *database.PebbleStore, h *finishDecisionHarness) string {
	t.Helper()
	h.block.Store(true)
	r1 := finishRegistry(t, store, h)
	ctx := t.Context()
	r1.Start(ctx)
	opID, err := r1.EnqueueOp(ctx, finishDecisionDef, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := store.GetLedgerV3(opID)
		if err != nil {
			t.Fatal(err)
		}
		if l != nil && slices.Equal(bitsSet(*l), []int{0, 1}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chunks 0 and 1 never reached the ledger: %+v", l)
		}
		time.Sleep(20 * time.Millisecond)
	}
	shutdownRegistry(t, r1)
	if n := h.finishes.Load(); n != 0 {
		t.Fatalf("Finish ran %d times in the stopped attempt, want it skipped", n)
	}
	h.block.Store(false)
	return opID
}

func appliedKeys(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, itemKey(i))
	}
	return out
}

// TestFinishWithoutResuming_RunsFinishOnceOverTheAppliedRows is the decision
// end to end on the real executor: a shutdown leaves a ResumeAsk batch with
// chunks 0 and 1 of 4 in its ledger; the next boot asks; "finish without
// resuming" dispatches an attempt under the same op id with a fresh fence that
// runs no item, runs Finish once over exactly the twenty rows the ledger
// marks, and ends the run succeeded.
func TestFinishWithoutResuming_RunsFinishOnceOverTheAppliedRows(t *testing.T) {
	store := newPebbleV3(t)
	h := newFinishHarness()
	opID := stopAfterTwoChunks(t, store, h)
	itemsBefore := map[int]int{}
	for i := range 40 {
		itemsBefore[i] = h.items.get(i)
	}

	r2 := finishRegistry(t, store, h)
	r2.Start(t.Context())
	t.Cleanup(func() { shutdownRegistry(t, r2) })
	asked := awaitRunState(t, store, opID, state.V3AwaitingDecision)
	if !r2.CanFinishWithoutResuming(finishDecisionDef) {
		t.Fatal("a registered native Batch must be able to take the third decision")
	}

	if err := r2.FinishWithoutResuming(t.Context(), opID, "alice"); err != nil {
		t.Fatalf("FinishWithoutResuming: %v", err)
	}
	row := awaitTerminal(t, store, opID)
	if row.Status != "completed" {
		t.Fatalf("run ended %s (%v), want completed", row.Status, row.ErrorMessage)
	}
	for i := range 40 {
		if h.items.get(i) != itemsBefore[i] {
			t.Fatalf("item %d ran in the finish-only attempt", i)
		}
	}
	if n := h.finishes.Load(); n != 1 {
		t.Fatalf("Finish ran %d times, want exactly once", n)
	}
	if got, want := h.seenMarks(), appliedKeys(0, 20); !slices.Equal(got, want) {
		t.Fatalf("Finish saw marks %v, want exactly the rows of chunks 0 and 1: %v", got, want)
	}
	rec, err := store.GetRunV3(opID)
	if err != nil || rec == nil {
		t.Fatalf("run record: %+v %v", rec, err)
	}
	if rec.State != state.V3Succeeded || rec.Attempt != asked.Attempt+1 || rec.FenceEpoch <= asked.FenceEpoch {
		t.Fatalf("record = state %s attempt %d epoch %d; want succeeded, a new attempt past %d and a fresh fence past %d",
			rec.State, rec.Attempt, rec.FenceEpoch, asked.Attempt, asked.FenceEpoch)
	}
	// The terminal transition deletes the run's ledger with its snapshot; the
	// runner-level test pins that the finish-only attempt itself never rewrites it.
	if l, _ := store.GetLedgerV3(opID); l != nil {
		t.Fatalf("a succeeded run kept its ledger: %+v", l)
	}
}

// TestFinishWithoutResuming_Refusals: the decision applies to a native
// Batch/Fixer run in awaiting_decision, and to nothing else.
func TestFinishWithoutResuming_Refusals(t *testing.T) {
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	if r.CanFinishWithoutResuming("test.unknown") {
		t.Fatal("an unregistered def cannot take the decision")
	}
	if err := r.FinishWithoutResuming(t.Context(), "no-such-op", "alice"); !errors.Is(err, ErrOpNotFound) {
		t.Fatalf("unknown op: %v, want ErrOpNotFound", err)
	}

	h := newFinishHarness()
	if err := Register(r, "test", h.def()); err != nil {
		t.Fatal(err)
	}
	if !r.CanFinishWithoutResuming(finishDecisionDef) {
		t.Fatal("a native Batch can take the decision")
	}
	// A finished run is not awaiting a decision.
	id, err := r.EnqueueOp(t.Context(), finishDecisionDef, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitTerminal(t, store, id)
	if err := r.FinishWithoutResuming(t.Context(), id, "alice"); !errors.Is(err, ErrOpNotFinishable) {
		t.Fatalf("completed run: %v, want ErrOpNotFinishable", err)
	}
	if n := h.finishes.Load(); n != 1 {
		t.Fatalf("a refused decision ran Finish: %d calls, want the run's own one", n)
	}
}

// TestFinishWithoutResuming_V2DefIsRefused: a v2 def waiting in interrupted_ask
// has no ledger and no Finish; "finish" would be a full Run, which is Retry.
func TestFinishWithoutResuming_V2DefIsRefused(t *testing.T) {
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	v2 := OperationDef{ID: "test.v2-ask", Plugin: "test", DisplayName: "v2 ask", ConcurrencyKey: "test.v2-ask",
		ResumePolicy: ResumeAsk, Liveness: LivenessManual,
		Run: func(context.Context, json.RawMessage, Reporter) error { return nil }}
	if err := r.RegisterOp(v2); err != nil {
		t.Fatal(err)
	}
	if r.CanFinishWithoutResuming(v2.ID) {
		t.Fatal("a v2 def cannot take the third decision")
	}
}

// TestFinishWithoutResuming_InterruptedFinishThenRetryResumesInFull: the
// finish-only flag belongs to the decision alone. A finish-only attempt that a
// shutdown interrupts returns the run to awaiting_decision still flagged, and
// a Retry from there resumes in full: it leases chunks 2 and 3 and runs Finish
// once, over all forty rows.
func TestFinishWithoutResuming_InterruptedFinishThenRetryResumesInFull(t *testing.T) {
	store := newPebbleV3(t)
	h := newFinishHarness()
	opID := stopAfterTwoChunks(t, store, h)

	// The finish-only attempt parks inside Finish and a shutdown interrupts it.
	h.blockFinish.Store(true)
	r2 := finishRegistry(t, store, h)
	r2.Start(t.Context())
	awaitRunState(t, store, opID, state.V3AwaitingDecision)
	if err := r2.FinishWithoutResuming(t.Context(), opID, "alice"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.finishStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the finish-only attempt never reached Finish")
	}
	if rec, _ := store.GetRunV3(opID); rec == nil || !rec.FinishOnly {
		t.Fatalf("the dispatched run lost its finish-only flag: %+v", rec)
	}
	shutdownRegistry(t, r2)

	// The next boot asks again; the flag is still on the record.
	h.blockFinish.Store(false)
	r3 := finishRegistry(t, store, h)
	r3.Start(t.Context())
	t.Cleanup(func() { shutdownRegistry(t, r3) })
	rec := awaitRunState(t, store, opID, state.V3AwaitingDecision)
	if !rec.FinishOnly {
		t.Fatalf("an interrupted finish-only attempt must leave the flag set until a decision clears it: %+v", rec)
	}
	finishesBefore := h.finishes.Load()

	// Retry resumes in full: the flag is cleared, chunks 2 and 3 are leased and
	// Finish runs once over every row.
	if err := r3.RetryInterrupted(t.Context(), opID, "alice"); err != nil {
		t.Fatalf("RetryInterrupted: %v", err)
	}
	row := awaitTerminal(t, store, opID)
	if row.Status != "completed" {
		t.Fatalf("retried run ended %s (%v)", row.Status, row.ErrorMessage)
	}
	for i := 20; i < 40; i++ {
		if h.items.get(i) == 0 {
			t.Fatalf("item %d never ran after the Retry: the resume was a finish-only attempt", i)
		}
	}
	if n := h.finishes.Load() - finishesBefore; n != 1 {
		t.Fatalf("Finish ran %d times after the Retry, want once", n)
	}
	if got, want := h.seenMarks(), appliedKeys(0, 40); !slices.Equal(got, want) {
		t.Fatalf("Finish saw %d marks, want all 40 rows", len(got))
	}
	if rec, _ := store.GetRunV3(opID); rec == nil || rec.FinishOnly || rec.State != state.V3Succeeded {
		t.Fatalf("final record = %+v, want succeeded with the flag cleared", rec)
	}
}
