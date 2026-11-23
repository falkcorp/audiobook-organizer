// file: internal/plugins/maintenance/batch_sweep_test.go
// version: 0.1.1
// guid: 5a3f0c52-9b1e-4d7a-8c26-0e4d71b6a9f3
// last-edited: 2026-11-23

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// sweepRollup is the fixture result type of the sweep tests: what each batch
// reports and what the parent rolls up.
type sweepRollup struct {
	Books   int `json:"books"`
	Batches int `json:"batches"`
}

// sweepEnv is a fake run for runBatchSweep: children are enqueued, awaited and
// checkpointed in memory, and every call is recorded. A test sets await to decide
// what a child does.
type sweepEnv struct {
	mu sync.Mutex

	ckpt      json.RawMessage // the last checkpoint, shared by every attempt
	enqueued  [][]string      // each child's book ids, in enqueue order
	params    []sweepParams   // the params each child got
	progress  [][2]int        // (done, total) reports
	flushed   []sweepRollup
	flushDone []bool
	nextID    int
	seen      map[string][]string // run id -> book ids

	// await runs for each wait: id is the child's run id. The default succeeds
	// with the child's books counted.
	await func(ctx context.Context, e *sweepEnv, id string) (tasks.Outcome, error)
	// deadline is each attempt's deadline (zero: none).
	deadline time.Time
	// now is the sweep's clock (zero: time.Now).
	now time.Time
	// enqueueErr, when set, fails the enqueue of that batch index.
	enqueueErr map[int]error
}

type sweepParams struct {
	Batch int
	Books []batchBook
}

// batchTestRunaway bounds the children a fake host will accept.
const batchTestRunaway = 500

func newSweepEnv() *sweepEnv { return &sweepEnv{seen: map[string][]string{}} }

func (e *sweepEnv) host(ctx context.Context) batchHost {
	return batchHost{
		Ctx: ctx,
		Enqueue: func(params any) (string, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			sp := params.(sweepParams)
			if err := e.enqueueErr[sp.Batch]; err != nil {
				return "", err
			}
			// A sweep over a few hundred books never needs this many children: a
			// cursor that stopped moving must fail the test, not spin until the
			// package timeout.
			if len(e.enqueued) >= batchTestRunaway {
				return "", fmt.Errorf("runaway sweep: %d children enqueued, the cursor is not moving", len(e.enqueued))
			}
			e.nextID++
			id := fmt.Sprintf("child-%d", e.nextID)
			ids := make([]string, len(sp.Books))
			for i, b := range sp.Books {
				ids[i] = b.ID
			}
			e.enqueued = append(e.enqueued, ids)
			e.params = append(e.params, sp)
			e.seen[id] = ids
			return id, nil
		},
		Await: func(ctx context.Context, id string) (tasks.Outcome, error) {
			if e.await != nil {
				return e.await(ctx, e, id)
			}
			return e.succeed(id), nil
		},
		Checkpoint: func(v any) error {
			b, err := json.Marshal(v)
			e.mu.Lock()
			e.ckpt = b
			e.mu.Unlock()
			return err
		},
		Restore: func(v any) (bool, error) {
			e.mu.Lock()
			b := e.ckpt
			e.mu.Unlock()
			if b == nil {
				return false, nil
			}
			return true, json.Unmarshal(b, v)
		},
		Deadline: func() time.Time { return e.deadline },
		Progress: func(done, total int, _ string) {
			e.mu.Lock()
			e.progress = append(e.progress, [2]int{done, total})
			e.mu.Unlock()
		},
		Log: func(slog.Level, string, ...slog.Attr) {},
		Now: func() time.Time {
			if e.now.IsZero() {
				return time.Now()
			}
			return e.now
		},
	}
}

// succeed is a child that finished its batch: one result per book, one batch.
func (e *sweepEnv) succeed(id string) tasks.Outcome {
	e.mu.Lock()
	n := len(e.seen[id])
	e.mu.Unlock()
	res, _ := json.Marshal(sweepRollup{Books: n, Batches: 1})
	return tasks.Outcome{State: tasks.StateSucceeded, Result: res}
}

func (e *sweepEnv) plan(ids []string, size int) batchPlan[sweepRollup] {
	return batchPlan[sweepRollup]{
		IDs: ids, Size: size,
		Books:  booksOfIDs,
		Params: func(batch int, books []batchBook) any { return sweepParams{Batch: batch, Books: books} },
		Merge: func(acc *sweepRollup, c sweepRollup) {
			acc.Books += c.Books
			acc.Batches += c.Batches
		},
		Flush: func(acc sweepRollup, done bool) {
			e.mu.Lock()
			e.flushed = append(e.flushed, acc)
			e.flushDone = append(e.flushDone, done)
			e.mu.Unlock()
		},
	}
}

func booksOfIDs(ids []string) []batchBook {
	out := make([]batchBook, len(ids))
	for i, id := range ids {
		out[i] = batchBook{ID: id, Files: []string{"f-" + id}}
	}
	return out
}

// sweepIDs is the ids b<from>..b<to-1>, zero-padded so lexical order is numeric.
func sweepIDs(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("b%04d", i))
	}
	return out
}

func (e *sweepEnv) state(t *testing.T) batchState[sweepRollup] {
	t.Helper()
	var st batchState[sweepRollup]
	if err := json.Unmarshal(e.ckpt, &st); err != nil {
		t.Fatalf("checkpoint %s: %v", e.ckpt, err)
	}
	return st
}

func (e *sweepEnv) sizes() []int {
	var s []int
	for _, b := range e.enqueued {
		s = append(s, len(b))
	}
	return s
}

// 250 books in batches of 100 are three children of 100, 100 and 50, each of the
// next ids in ascending order, numbered 0, 1, 2; the rollup is their sum.
func TestBatchSweep_BatchesOfTheConfiguredSize(t *testing.T) {
	e := newSweepEnv()
	ids := sweepIDs(0, 250)
	// Reversed and with a duplicate: the sweep orders and de-duplicates its input,
	// because its cursor is an id.
	in := slices.Clone(ids)
	slices.Reverse(in)
	in = append(in, ids[7])

	got, err := runBatchSweep(e.host(t.Context()), e.plan(in, batchSweepSize))
	if err != nil {
		t.Fatal(err)
	}
	var flat []string
	for _, b := range e.enqueued {
		flat = append(flat, b...)
	}
	if !slices.Equal(e.sizes(), []int{100, 100, 50}) {
		t.Fatalf("batch sizes %v, want [100 100 50]", e.sizes())
	}
	if !slices.Equal(flat, ids) {
		t.Fatalf("children saw %d ids in a different order than the library's, want every id once, ascending", len(flat))
	}
	for i, p := range e.params {
		if p.Batch != i {
			t.Errorf("child %d was numbered batch %d", i, p.Batch)
		}
	}
	if got != (sweepRollup{Books: 250, Batches: 3}) {
		t.Fatalf("rollup %+v, want 250 books in 3 batches", got)
	}
}

// batchSweepSize is the owner's decision (2026-11-18): 100 books per child. A
// plan that leaves Size at zero gets it, and the scope refuses a larger batch.
func TestBatchSweep_DefaultSizeIsOneHundred(t *testing.T) {
	if batchSweepSize != 100 {
		t.Fatalf("batchSweepSize = %d, the owner decided 100", batchSweepSize)
	}
	e := newSweepEnv()
	if _, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 101), 0)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.sizes(), []int{100, 1}) {
		t.Fatalf("101 books with no size set ran as children of %v, want 100 then 1", e.sizes())
	}
	over := batchScope{ParentRunID: "run-1", Books: booksOfIDs(sweepIDs(0, 101))}
	if err := over.validate(); !errors.Is(err, errBatchScope) {
		t.Fatalf("a 101-book scope validated: %v", err)
	}
	if err := (batchScope{ParentRunID: "run-1", Books: booksOfIDs(sweepIDs(0, 100))}).validate(); err != nil {
		t.Fatalf("a 100-book scope was refused: %v", err)
	}
}

// A parent that is stopped while batch 1 runs and started again resumes at the
// cursor: it re-attaches to the same child (never enqueues it twice), then starts
// the batch after it, and no book is visited twice or skipped.
func TestBatchSweep_RestartResumesAtTheCursor(t *testing.T) {
	e := newSweepEnv()
	ids := sweepIDs(0, 250)

	// Attempt 1: batch 0 succeeds, batch 1 is running when the attempt is stopped.
	ctx1, stop := context.WithCancelCause(t.Context())
	e.await = func(ctx context.Context, e *sweepEnv, id string) (tasks.Outcome, error) {
		if id == "child-2" {
			stop(errors.New("the node restarted"))
			<-ctx.Done()
			return tasks.Outcome{}, context.Cause(ctx)
		}
		return e.succeed(id), nil
	}
	_, err := runBatchSweep(e.host(ctx1), e.plan(ids, 100))
	if err == nil || err.Error() != "the node restarted" {
		t.Fatalf("attempt 1 ended with %v, want the stop cause", err)
	}
	st := e.state(t)
	if st.After != "b0099" || st.DoneBatches != 1 || st.InFlight == nil || st.InFlight.RunID != "child-2" || st.InFlight.Last != "b0199" {
		t.Fatalf("checkpoint after the stop: %+v (in flight %+v), want cursor b0099, 1 batch done, child-2 in flight to b0199", st, st.InFlight)
	}

	// Attempt 2: child-2 finished meanwhile; the sweep must not enqueue it again.
	e.await = nil
	before := len(e.enqueued)
	got, err := runBatchSweep(e.host(t.Context()), e.plan(ids, 100))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(e.enqueued) - before; n != 1 {
		t.Fatalf("attempt 2 enqueued %d children, want only batch 2 (the in-flight child is re-attached, not re-enqueued)", n)
	}
	if last := e.enqueued[len(e.enqueued)-1]; !slices.Equal(last, ids[200:]) {
		t.Fatalf("attempt 2's new child had %d books from %s, want the 50 after the cursor (b0200..b0249)", len(last), last[0])
	}
	if got != (sweepRollup{Books: 250, Batches: 3}) {
		t.Fatalf("rollup after the restart %+v, want every book once in 3 batches", got)
	}
}

// The cursor is a book id, not a batch index: when the id list a restarted
// attempt plans from has shrunk (only_missing drops what is done), an index would
// skip books, an id never does.
func TestBatchSweep_CursorIsAnIDNotAnIndex(t *testing.T) {
	e := newSweepEnv()
	ids := sweepIDs(0, 250)
	if _, err := runBatchSweep(e.host(t.Context()), e.plan(ids[:100], 100)); err != nil {
		t.Fatal(err)
	}
	if st := e.state(t); st.After != "b0099" {
		t.Fatalf("cursor %q after batch 0", st.After)
	}
	// 40 of the books batch 0 finished are gone from the list; 100..249 remain.
	shrunk := append(slices.Clone(ids[40:100]), ids[100:]...)
	before := len(e.enqueued)
	if _, err := runBatchSweep(e.host(t.Context()), e.plan(shrunk, 100)); err != nil {
		t.Fatal(err)
	}
	var visited []string
	for _, b := range e.enqueued[before:] {
		visited = append(visited, b...)
	}
	if !slices.Equal(visited, ids[100:]) {
		t.Fatalf("resumed over %d books, want the 150 after the cursor (b0100..b0249)", len(visited))
	}
}

// A cancel that arrives while a child runs stops the sweep after that child: the
// child's result is folded in and no further child starts.
func TestBatchSweep_CancelStopsAfterTheCurrentChild(t *testing.T) {
	e := newSweepEnv()
	ctx, cancel := context.WithCancelCause(t.Context())
	e.await = func(_ context.Context, e *sweepEnv, id string) (tasks.Outcome, error) {
		if id == "child-1" {
			cancel(context.Canceled) // the operator cancels while batch 0 runs
		}
		return e.succeed(id), nil // and the child still ends successfully
	}
	got, err := runBatchSweep(e.host(ctx), e.plan(sweepIDs(0, 250), 100))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancel", err)
	}
	if len(e.enqueued) != 1 {
		t.Fatalf("%d children enqueued, want exactly the one that was running at the cancel", len(e.enqueued))
	}
	if got != (sweepRollup{Books: 100, Batches: 1}) {
		t.Fatalf("rollup %+v, want the finished child's 100 books", got)
	}
	if st := e.state(t); st.After != "b0099" || st.InFlight != nil {
		t.Fatalf("checkpoint %+v, want cursor b0099 and nothing in flight", st)
	}
}

// A cancel that arrives while the parent is blocked on a child ends the attempt
// at once; the child stays recorded as in flight (a cancel is not cascaded) and no
// further child starts.
func TestBatchSweep_CancelWhileWaiting(t *testing.T) {
	e := newSweepEnv()
	ctx, cancel := context.WithCancelCause(t.Context())
	e.await = func(ctx context.Context, _ *sweepEnv, _ string) (tasks.Outcome, error) {
		cancel(context.Canceled)
		<-ctx.Done()
		return tasks.Outcome{}, context.Cause(ctx)
	}
	_, err := runBatchSweep(e.host(ctx), e.plan(sweepIDs(0, 250), 100))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancel", err)
	}
	if len(e.enqueued) != 1 {
		t.Fatalf("%d children enqueued after a cancel, want 1", len(e.enqueued))
	}
	if st := e.state(t); st.InFlight == nil || st.After != "" {
		t.Fatalf("checkpoint %+v, want the child in flight and no cursor", st)
	}
}

// Every batch's result is folded into the parent's summary, once, and the
// persistence hook sees the running rollup after each batch and the final one.
func TestBatchSweep_RollsUpEveryBatch(t *testing.T) {
	e := newSweepEnv()
	e.await = func(_ context.Context, _ *sweepEnv, id string) (tasks.Outcome, error) {
		// Child n reports n books, so a dropped or doubled batch changes the sum.
		var n int
		fmt.Sscanf(id, "child-%d", &n)
		res, _ := json.Marshal(sweepRollup{Books: n, Batches: 1})
		return tasks.Outcome{State: tasks.StateSucceeded, Result: res}, nil
	}
	got, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100))
	if err != nil {
		t.Fatal(err)
	}
	if got != (sweepRollup{Books: 1 + 2 + 3, Batches: 3}) {
		t.Fatalf("rollup %+v, want 1+2+3 books over 3 batches", got)
	}
	want := []sweepRollup{{1, 1}, {3, 2}, {6, 3}, {6, 3}}
	if !slices.Equal(e.flushed, want) || !slices.Equal(e.flushDone, []bool{false, false, false, true}) {
		t.Fatalf("flushed %v (done %v), want %v with only the last done", e.flushed, e.flushDone, want)
	}
	if st := e.state(t); st.Rollup != got {
		t.Fatalf("checkpointed rollup %+v differs from the result %+v", st.Rollup, got)
	}
}

// A child that does not succeed ends the sweep with its error: the cursor stays
// before that batch and no later child starts.
func TestBatchSweep_FailedChildStopsTheSweep(t *testing.T) {
	for _, state := range []tasks.RunState{tasks.StateFailed, tasks.StateCanceled} {
		t.Run(string(state), func(t *testing.T) {
			e := newSweepEnv()
			e.await = func(_ context.Context, e *sweepEnv, id string) (tasks.Outcome, error) {
				if id == "child-2" {
					return tasks.Outcome{State: state, Err: "the Joker interfered"}, nil
				}
				return e.succeed(id), nil
			}
			got, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100))
			if !errors.Is(err, errBatchChild) {
				t.Fatalf("err = %v, want errBatchChild", err)
			}
			if len(e.enqueued) != 2 {
				t.Fatalf("%d children, want the sweep to stop after the failing one", len(e.enqueued))
			}
			if got.Books != 100 {
				t.Fatalf("rollup %+v folded the failed batch", got)
			}
			if st := e.state(t); st.After != "b0099" {
				t.Fatalf("cursor %q, want b0099 (the failed batch is not done)", st.After)
			}
		})
	}
}

// An enqueue that fails ends the sweep with nothing in flight and the cursor at
// the last finished batch.
func TestBatchSweep_EnqueueErrorEndsTheSweep(t *testing.T) {
	e := newSweepEnv()
	e.enqueueErr = map[int]error{1: errors.New("queue full")}
	_, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100))
	if err == nil || !errors.Is(err, e.enqueueErr[1]) {
		t.Fatalf("err = %v, want the enqueue error", err)
	}
	if st := e.state(t); st.InFlight != nil || st.After != "b0099" {
		t.Fatalf("checkpoint %+v", st)
	}
}

// No books is a successful sweep with no child.
func TestBatchSweep_NothingToDo(t *testing.T) {
	e := newSweepEnv()
	got, err := runBatchSweep(e.host(t.Context()), e.plan(nil, 100))
	if err != nil || len(e.enqueued) != 0 || got != (sweepRollup{}) {
		t.Fatalf("got %+v, %v with %d children", got, err, len(e.enqueued))
	}
	if len(e.flushDone) != 1 || !e.flushDone[0] {
		t.Fatalf("flushes %v, want one final", e.flushDone)
	}
}

// The attempt's deadline: a wait is bounded margin before it. With the child still
// running at the bound the attempt checkpoints it in flight and yields
// (ErrContinueInPlace); the next attempt re-attaches to the same child. The
// progress the kernel's yield proof reads (done) grew across the yield.
func TestBatchSweep_YieldsWithTheChildInFlightAndReattaches(t *testing.T) {
	e := newSweepEnv()
	e.deadline = time.Now().Add(batchYieldMargin + 40*time.Millisecond)
	e.await = func(ctx context.Context, _ *sweepEnv, _ string) (tasks.Outcome, error) {
		<-ctx.Done() // the child is still running when the wait's bound passes
		return tasks.Outcome{}, context.Cause(ctx)
	}
	_, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100))
	if !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("err = %v, want ErrContinueInPlace", err)
	}
	st := e.state(t)
	if st.InFlight == nil || len(e.enqueued) != 1 {
		t.Fatalf("checkpoint %+v after %d children, want the one child in flight", st, len(e.enqueued))
	}
	doneAfterFirst := e.progress[len(e.progress)-1][0]

	// Attempt 2, a fresh deadline: the child ended while the node was between
	// attempts. It is re-attached, and the progress moves past attempt 1's.
	e.deadline = time.Time{}
	e.await = nil
	before := len(e.enqueued)
	got, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100))
	if err != nil {
		t.Fatal(err)
	}
	if end := e.progress[len(e.progress)-1][0]; end <= doneAfterFirst {
		t.Fatalf("progress did not move past the yield: %d then %d", doneAfterFirst, end)
	}
	if n := len(e.enqueued) - before; n != 2 {
		t.Fatalf("attempt 2 enqueued %d children, want the 2 batches after the re-attached one", n)
	}
	if got.Books != 250 {
		t.Fatalf("rollup %+v, want 250 books", got)
	}
}

// The re-attach counts as a step, so an attempt that does nothing but wait on the
// same running child at consecutive deadlines still moves the progress (the kernel
// refuses a yield that moved nothing, and would fail the run).
func TestBatchSweep_ReattachAdvancesProgress(t *testing.T) {
	e := newSweepEnv()
	e.await = func(ctx context.Context, _ *sweepEnv, _ string) (tasks.Outcome, error) {
		<-ctx.Done()
		return tasks.Outcome{}, context.Cause(ctx)
	}
	var done []int
	for range 3 {
		e.deadline = time.Now().Add(batchYieldMargin + 20*time.Millisecond)
		if _, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100)); !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("err = %v, want a yield", err)
		}
		done = append(done, e.progress[len(e.progress)-1][0])
	}
	if !(done[0] < done[1] && done[1] < done[2]) {
		t.Fatalf("progress across three yields on the same child: %v, want strictly growing", done)
	}
	if len(e.enqueued) != 1 {
		t.Fatalf("%d children enqueued, want the same one re-attached each time", len(e.enqueued))
	}
}

// Before a new batch an attempt near its deadline yields instead of starting a
// child it could not wait for, but only once it has made progress: an attempt that
// would yield having done nothing starts the batch (else it would never end).
func TestBatchSweep_YieldsBeforeANewBatchOnlyAfterProgress(t *testing.T) {
	e := newSweepEnv()
	base := time.Now()
	e.now = base
	// The deadline is inside the margin from the start: attempt 1 has made no
	// progress, so it still starts and finishes batch 0, then yields before batch 1.
	e.deadline = base.Add(batchYieldMargin - time.Minute)
	e.await = func(_ context.Context, e *sweepEnv, id string) (tasks.Outcome, error) {
		return e.succeed(id), nil
	}
	_, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 250), 100))
	if !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("err = %v, want a yield", err)
	}
	if len(e.enqueued) != 1 {
		t.Fatalf("%d children, want exactly one before the yield (a no-progress attempt must start a batch)", len(e.enqueued))
	}
	if st := e.state(t); st.After != "b0099" || st.InFlight != nil {
		t.Fatalf("checkpoint %+v, want batch 0 done and nothing in flight", st)
	}
}

// The wait is bounded by deadline - margin, so a child still running at that point
// is not waited for until the attempt's own deadline.
func TestBatchSweep_WaitIsBoundedByTheDeadlineMinusTheMargin(t *testing.T) {
	e := newSweepEnv()
	deadline := time.Now().Add(2 * time.Hour)
	e.deadline = deadline
	var bound time.Time
	e.await = func(ctx context.Context, e *sweepEnv, id string) (tasks.Outcome, error) {
		bound, _ = ctx.Deadline()
		return e.succeed(id), nil
	}
	if _, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 10), 100)); err != nil {
		t.Fatal(err)
	}
	if want := deadline.Add(-batchYieldMargin); !bound.Equal(want) {
		t.Fatalf("a wait is bounded at %v, want %v (deadline minus %v)", bound, want, batchYieldMargin)
	}
}

// A result the parent cannot read is a failed child, not a silent zero.
func TestBatchSweep_UndecodableResultFails(t *testing.T) {
	e := newSweepEnv()
	e.await = func(context.Context, *sweepEnv, string) (tasks.Outcome, error) {
		return tasks.Outcome{State: tasks.StateSucceeded, Result: json.RawMessage(`[1,2]`)}, nil
	}
	_, err := runBatchSweep(e.host(t.Context()), e.plan(sweepIDs(0, 10), 100))
	if !errors.Is(err, errBatchChild) {
		t.Fatalf("err = %v, want errBatchChild", err)
	}
}
