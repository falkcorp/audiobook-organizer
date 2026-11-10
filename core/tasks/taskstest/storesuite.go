// file: core/tasks/taskstest/storesuite.go
// version: 1.1.0
// guid: 95c1b2be-e2c3-4c7c-86d6-8670bf9fd4cf
// last-edited: 2026-11-10

package taskstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// BatchOpener is what StoresSuite opens its batches with: Stores.Runs
// implements it (NativeStores does). The batch must be indexed (a later read
// of one build sees an earlier write of it) and of the same store as the
// Stores. A Stores whose Runs does not skips every case that writes.
type BatchOpener interface {
	NewBatch() *storage.Batch
}

// ImporterProvider is how StoresSuite finds an Importer over the same
// keyspace as the Stores: Stores.Runs implements it (NativeStores does). A
// Stores whose Runs does not skips the importer cases.
type ImporterProvider interface {
	Importer() tasks.Importer
}

// Crasher simulates a crash: CrashClone returns Stores over the state a
// crash at this instant leaves behind (a crash clone of the store's file
// system, booted again), with Runs implementing the same interfaces. It is a
// test harness's wrapper, never a production type; a Stores whose Runs does
// not implement it skips the crash case of the importer.
type Crasher interface {
	CrashClone(t *testing.T) tasks.Stores
}

// commitAttempts bounds the suite's RetryOnConflict, as WriteRuns bounds its
// own.
const commitAttempts = 64

// StoresSuite is the conformance suite of tasks.Stores (05-tasks 5T.3.13,
// 5T.9; the <pkg>test.<Interface>Suite rule, SIMP5-9). newStores returns a
// fresh, empty Stores for each subtest. It asserts compare-and-set
// transitions over every executor edge, the commit-time write check on every
// write (R6-1), one row per run, per-run sequences, terminal-only Prune,
// that every write is staged into the caller's batch (a discarded batch
// leaves nothing; Prune and ReplaceRows excepted, they commit their own),
// and the concurrency cases TestProgressVsCancelKeepsState,
// TestLedgerCommitAfterTerminalRefused,
// TestConcurrentTransitionsOfDistinctRunsAllCommit,
// TestCancelCommitsUnderProgressTicks, TestPruneUnderConcurrentCompletions,
// TestTransitionVsCancelOneWins and TestFixerMarksLandDuringQuiesce, each in
// a synctest bubble.
//
// The suite reaches past the four interfaces only by type assertion on
// Stores.Runs: BatchOpener (the suite needs batches of the store's own DB;
// without it every case that writes skips with t.Skip), ImporterProvider (the importer cases: an imported run
// keeps its ID, kind, state and Seq; a second import fails with
// ErrRunConflict; RunData lands only on an imported run; RenameKind rewrites
// runs, clock, buckets and stamps and is idempotent; without it they skip
// with t.Skip) and Crasher (the importer's crash case: a crash at every
// point of an import batch leaves it wholly imported or wholly not; without
// it that case skips).
//
// The suite emulates the executor's side of 5T.7 where a case needs it:
// writeRuns re-stages a whole build under storage.RetryOnConflict, a bare
// progress tick is dropped when its fresh read shows a state change, and a
// ledger write is re-staged only while the fresh read shows the run
// non-terminal under the same attempt and fence.
func StoresSuite(t *testing.T, newStores func(*testing.T) tasks.Stores) {
	t.Helper()
	// One probe Stores, built outside any bubble, says whether the cases
	// that write can run; a skip inside a synctest bubble would not reach
	// the case's own result.
	batchless := newFixture(t, newStores).open == nil
	needBatches := func(t *testing.T) {
		t.Helper()
		if batchless {
			t.Skip("Stores.Runs does not implement taskstest.BatchOpener; a case that writes needs batches of the store's own DB")
		}
	}
	cases := []struct {
		name   string
		fn     func(t *testing.T, f *fixture)
		bubble bool // runs in a synctest bubble, the Stores built inside it
	}{
		{"TestProgressVsCancelKeepsState", testProgressVsCancelKeepsState, true},
		{"TestLedgerCommitAfterTerminalRefused", testLedgerCommitAfterTerminalRefused, true},
		{"TestConcurrentTransitionsOfDistinctRunsAllCommit", testConcurrentTransitionsAllCommit, true},
		{"TestCancelCommitsUnderProgressTicks", testCancelCommitsUnderProgressTicks, true},
		{"TestPruneUnderConcurrentCompletions", testPruneUnderConcurrentCompletions, true},
		{"TestTransitionVsCancelOneWins", testTransitionVsCancelOneWins, true},
		{"TestFixerMarksLandDuringQuiesce", testFixerMarksLandDuringQuiesce, true},
		{"CommitTimeWriteCheck", testCommitTimeWriteCheck, false},
		{"DiscardedBatchLeavesNothing", testDiscardedBatchLeavesNothing, false},
		{"OneRowPerRun", testOneRowPerRun, false},
		{"PerRunSeqAdvances", testPerRunSeqAdvances, false},
		{"PruneTerminalOnly", testPruneTerminalOnly, false},
		{"DeleteRemovesEveryRecord", testDeleteRemovesEveryRecord, false},
		{"QueueOrder", testQueueOrder, false},
		{"ListOrderFiltersAndCursor", testList, false},
		{"PagesCursorAndOutcome", testPages, false},
		{"ReplaceRows", testReplaceRows, false},
		{"DebounceMergeAndFlush", testDebounce, false},
		{"ClockPauseStandDownStamps", testClockPauseStandDownStamps, false},
		{"TerminalNeedsFinishTime", testTerminalNeedsFinish, false},
		{"NewIDUnique", testNewID, false},
	}
	t.Run("CAS", func(t *testing.T) {
		for _, e := range executorEdges() {
			t.Run(fmt.Sprintf("%s->%s", e.from, e.to), func(t *testing.T) {
				needBatches(t)
				testEdge(t, newFixture(t, newStores), e)
			})
		}
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name != "NewIDUnique" {
				needBatches(t)
			}
			run := func(t *testing.T) { c.fn(t, newFixture(t, newStores)) }
			if !c.bubble {
				run(t)
				return
			}
			// The store is opened inside the bubble, so the engine's own
			// goroutines belong to it: a goroutine of the bubble may not
			// be woken by one outside it, and t's cleanups (which close
			// the store) run inside the bubble before it ends.
			synctest.Test(t, run)
		})
	}
	t.Run("Importer", func(t *testing.T) {
		for _, c := range []struct {
			name string
			fn   func(t *testing.T, f *fixture)
		}{
			{"KeepsIDKindStateSeq", testImportKeeps},
			{"SecondImportConflicts", testImportTwice},
			{"RunDataOnlyOnImportedRun", testImportRunData},
			{"CrashCloneWhollyOrNot", testImportCrash},
			{"RenameKindRewritesAndIsIdempotent", testRenameKind},
		} {
			t.Run(c.name, func(t *testing.T) {
				needBatches(t)
				f := newFixture(t, newStores)
				if f.imp == nil {
					t.Skip("Stores.Runs does not implement taskstest.ImporterProvider; importer cases skipped")
				}
				c.fn(t, f)
			})
		}
	})
}

// fixture is one fresh Stores and what the suite found on its Runs.
type fixture struct {
	tasks.Stores
	open func() *storage.Batch // nil when Runs is no BatchOpener
	imp  tasks.Importer
}

func newFixture(t *testing.T, newStores func(*testing.T) tasks.Stores) *fixture {
	t.Helper()
	return fixtureOf(t, newStores(t))
}

func fixtureOf(t *testing.T, s tasks.Stores) *fixture {
	t.Helper()
	if s.Runs == nil || s.Clocks == nil || s.Pause == nil || s.StandDown == nil {
		t.Fatalf("Stores has a nil member: %+v", s)
	}
	f := &fixture{Stores: s}
	if bo, ok := s.Runs.(BatchOpener); ok {
		f.open = bo.NewBatch
	}
	if ip, ok := s.Runs.(ImporterProvider); ok {
		f.imp = ip.Importer()
	}
	return f
}

// base is the fixture's fixed instant (Batman fixtures only, for names).
func base() time.Time { return time.Date(2026, time.March, 14, 21, 0, 0, 0, time.UTC) }

const (
	kindPatrol tasks.Kind = "gotham.patrol"
	kindCase   tasks.Kind = "gotham.case-file"
	kindIntake tasks.Kind = "arkham.intake"
)

func villain(id string) entity.Ref { return entity.Ref{Module: "gotham", Kind: "villains", ID: id} }

// rec is a run row in state, attempt 1 and fence 1.
func rec(id string, kind tasks.Kind, state tasks.RunState) tasks.RunRecord {
	r := tasks.RunRecord{
		ID: tasks.RunID(id), Kind: kind, Mode: tasks.ModeLive, State: state,
		Actor: tasks.Actor{UserID: "bruce", AuthMethod: "session"}, Trigger: tasks.TriggerManual,
		Created: base(), Current: "Arkham Asylum",
		Priority: tasks.PriorityNormal, Params: json.RawMessage(`{"district":"east-end"}`),
		Attempt: 1, FenceEpoch: 1,
	}
	if state != tasks.StateQueued && state != tasks.StateWaitingDeps {
		r.Started = base().Add(time.Minute)
	}
	if state.Terminal() {
		r.Finished = base().Add(time.Hour)
	}
	return r
}

// want is the CAS precondition matching r.
func want(r tasks.RunRecord) tasks.Expect {
	return tasks.Expect{State: r.State, Attempt: r.Attempt, FenceEpoch: r.FenceEpoch}
}

// writeRuns is WriteRuns' shape: each attempt opens a fresh batch, stages and
// commits it, under storage.RetryOnConflict.
func (f *fixture) writeRuns(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
	return storage.RetryOnConflict(ctx, commitAttempts, func(ctx context.Context) (err error) {
		b := f.open()
		defer func() { err = errors.Join(err, b.Close()) }()
		if err := stage(ctx, b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// closeBatch discards b and reports a Close error as a test failure.
func closeBatch(t *testing.T, b *storage.Batch) {
	t.Helper()
	if err := b.Close(); err != nil {
		t.Errorf("close batch: %v", err)
	}
}

// once stages into one fresh batch and commits it, without a retry.
func (f *fixture) once(ctx context.Context, stage func(b *storage.Batch) error) (err error) {
	b := f.open()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := stage(b); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
}

func (f *fixture) insert(t *testing.T, rs ...tasks.RunRecord) {
	t.Helper()
	for _, r := range rs {
		if err := f.writeRuns(t.Context(), func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.Insert(ctx, b, r)
		}); err != nil {
			t.Fatalf("Insert %s: %v", r.ID, err)
		}
	}
}

func (f *fixture) get(t *testing.T, id tasks.RunID) tasks.RunRecord {
	t.Helper()
	r, err := f.Runs.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return r
}

func (f *fixture) counts(t *testing.T) map[tasks.RunState]int64 {
	t.Helper()
	c, err := f.Runs.Counts(t.Context())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	return c
}

// transition moves r to state under r's CAS precondition, returning the next
// row as staged.
func (f *fixture) transition(ctx context.Context, r tasks.RunRecord, state tasks.RunState, edit func(*tasks.RunRecord)) (tasks.RunRecord, error) {
	next := r
	next.State = state
	if state.Terminal() && next.Finished.IsZero() {
		next.Finished = base().Add(2 * time.Hour)
	}
	if edit != nil {
		edit(&next)
	}
	err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
		return f.Runs.Transition(ctx, b, r.ID, want(r), next)
	})
	return next, err
}

// tick is a bare progress tick of the run's writer (5T.7): dropped with
// ErrRunConflict when the fresh read no longer shows the state the writer
// holds, otherwise staged.
func (f *fixture) tick(ctx context.Context, id tasks.RunID, held tasks.RunState, done int) error {
	return f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
		cur, err := f.Runs.Get(ctx, id)
		if err != nil {
			return err
		}
		if cur.State != held {
			return fmt.Errorf("%w: tick dropped, the run is %s", tasks.ErrRunConflict, cur.State)
		}
		return f.Runs.Progress(ctx, b, id, done, 100, "Arkham Asylum")
	})
}

// ledger stages a ledger-family write under 5T.7's rule: re-staged on a
// fresh read only while the run is non-terminal under the same attempt and
// fence, otherwise refused with ErrRunConflict.
func (f *fixture) ledger(ctx context.Context, id tasks.RunID, attempt int, fence uint64, stage func(ctx context.Context, b *storage.Batch) error) error {
	return f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
		cur, err := f.Runs.Get(ctx, id)
		if err != nil {
			return err
		}
		if cur.State.Terminal() || cur.Attempt != attempt || cur.FenceEpoch != fence {
			return fmt.Errorf("%w: ledger write refused, the run is %s attempt %d fence %d", tasks.ErrRunConflict, cur.State, cur.Attempt, cur.FenceEpoch)
		}
		return stage(ctx, b)
	})
}

func collect[T any](t *testing.T, seq iter.Seq2[T, error]) []T {
	t.Helper()
	var out []T
	for v, err := range seq {
		if err != nil {
			t.Fatalf("iterate: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func ids(rs []tasks.RunRecord) []tasks.RunID {
	out := make([]tasks.RunID, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// edge is one executor transition of 5T.3.10's RunState list.
type edge struct {
	from, to tasks.RunState
	claim    bool // the dispatcher's claim: the fence epoch advances
}

// executorEdges are the transitions the executor makes (5T.3.6, 5T.3.7,
// 5T.3.10, 5T.4): claim, deps wait, completion, cancel, quiesce, shutdown,
// yield, resume policies and decisions.
func executorEdges() []edge {
	return []edge{
		{from: tasks.StateQueued, to: tasks.StateRunning, claim: true},
		{from: tasks.StateQueued, to: tasks.StateWaitingDeps},
		{from: tasks.StateWaitingDeps, to: tasks.StateQueued},
		{from: tasks.StateQueued, to: tasks.StateCanceled},
		{from: tasks.StateWaitingDeps, to: tasks.StateCanceled},
		{from: tasks.StateRunning, to: tasks.StateSucceeded},
		{from: tasks.StateRunning, to: tasks.StateFailed},
		{from: tasks.StateRunning, to: tasks.StateTimedOut},
		{from: tasks.StateRunning, to: tasks.StateStopping},
		{from: tasks.StateRunning, to: tasks.StateInterrupted},
		{from: tasks.StateRunning, to: tasks.StateQueued},
		{from: tasks.StateStopping, to: tasks.StateCanceled},
		{from: tasks.StateStopping, to: tasks.StateInterrupted},
		{from: tasks.StateStopping, to: tasks.StateInterruptedQuiesced},
		{from: tasks.StateInterrupted, to: tasks.StateQueued},
		{from: tasks.StateInterrupted, to: tasks.StateAwaitingDecision},
		{from: tasks.StateInterrupted, to: tasks.StateInterruptedDropped},
		{from: tasks.StateInterruptedQuiesced, to: tasks.StateQueued},
		{from: tasks.StateAwaitingDecision, to: tasks.StateQueued},
		{from: tasks.StateAwaitingDecision, to: tasks.StateInterruptedDropped},
	}
}

// testEdge: the edge commits under a matching precondition, every derived
// view follows it, and a precondition that differs in state, attempt or
// fence is refused at staging with ErrRunConflict.
func testEdge(t *testing.T, f *fixture, e edge) {
	ctx := t.Context()
	r := rec("patrol-edge", kindPatrol, e.from)
	f.insert(t, r)
	for _, stale := range []tasks.Expect{
		{State: tasks.StateSucceeded, Attempt: r.Attempt, FenceEpoch: r.FenceEpoch},
		{State: r.State, Attempt: r.Attempt + 1, FenceEpoch: r.FenceEpoch},
		{State: r.State, Attempt: r.Attempt, FenceEpoch: r.FenceEpoch + 1},
	} {
		err := f.once(ctx, func(b *storage.Batch) error {
			n := r
			n.State = e.to
			return f.Runs.Transition(ctx, b, r.ID, stale, n)
		})
		if !errors.Is(err, tasks.ErrRunConflict) {
			t.Fatalf("Transition under stale %+v: %v, want ErrRunConflict", stale, err)
		}
	}
	next, err := f.transition(ctx, r, e.to, func(n *tasks.RunRecord) {
		if e.claim {
			n.FenceEpoch++
		}
		n.Reason = "bat-signal"
	})
	if err != nil {
		t.Fatalf("Transition %s->%s: %v", e.from, e.to, err)
	}
	got := f.get(t, r.ID)
	if got.State != e.to || got.FenceEpoch != next.FenceEpoch || got.Reason != "bat-signal" {
		t.Fatalf("after %s->%s: state %s fence %d reason %q", e.from, e.to, got.State, got.FenceEpoch, got.Reason)
	}
	c := f.counts(t)
	if c[e.to] != 1 || c[e.from] != 0 {
		t.Fatalf("Counts after %s->%s = %v", e.from, e.to, c)
	}
	queued := ids(collect(t, f.Runs.Queued(ctx)))
	if (len(queued) == 1) != (e.to == tasks.StateQueued) {
		t.Fatalf("Queued after %s->%s = %v", e.from, e.to, queued)
	}
	unfinished := ids(collect(t, f.Runs.Unfinished(ctx)))
	if (len(unfinished) == 1) != !e.to.Terminal() {
		t.Fatalf("Unfinished after %s->%s = %v", e.from, e.to, unfinished)
	}
	// The old precondition no longer holds.
	if _, err := f.transition(ctx, r, e.to, nil); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("second Transition under the old precondition: %v, want ErrRunConflict", err)
	}
	// Retention sees exactly the terminal runs.
	n, err := f.Runs.Prune(ctx, base().Add(24*time.Hour), 10)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if (n == 1) != e.to.Terminal() {
		t.Fatalf("Prune after %s->%s deleted %d", e.from, e.to, n)
	}
}

// testProgressVsCancelKeepsState (R6-1, DATA6-1): a progress tick races an
// HTTP cancel and the run stays stopping whatever the order, because a tick
// staged over a read before the cancel's commit fails at commit.
func testProgressVsCancelKeepsState(t *testing.T, f *fixture) {
	running := rec("patrol-tick-first", kindPatrol, tasks.StateRunning)
	cancelFirst := rec("patrol-cancel-first", kindPatrol, tasks.StateRunning)
	raced := rec("patrol-raced", kindPatrol, tasks.StateRunning)
	f.insert(t, running, cancelFirst, raced)
	cancel := func(ctx context.Context, r tasks.RunRecord) error {
		_, err := f.transition(ctx, r, tasks.StateStopping, func(n *tasks.RunRecord) { n.Reason = "cancel" })
		return err
	}
	ctx := t.Context()

	// The tick reads before the cancel commits.
	bt := f.open()
	defer closeBatch(t, bt)
	if err := f.Runs.Progress(ctx, bt, running.ID, 50, 100, "Ace Chemicals"); err != nil {
		t.Fatalf("Progress: %v", err)
	}
	cancelled := make(chan error, 1)
	go func() { cancelled <- cancel(ctx, running) }()
	synctest.Wait()
	if cancelErr := <-cancelled; cancelErr != nil {
		t.Fatalf("cancel: %v", cancelErr)
	}
	if err := bt.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale tick commit: %v, want storage.ErrConflict", err)
	}
	if err := f.tick(ctx, running.ID, tasks.StateRunning, 51); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("tick retry after the cancel: %v, want ErrRunConflict (dropped)", err)
	}
	if got := f.get(t, running.ID); got.State != tasks.StateStopping || got.Done == 50 || got.Done == 51 {
		t.Fatalf("after a stale tick: state %s done %d, want stopping with the old progress", got.State, got.Done)
	}

	// The cancel commits first: a tick staged afterwards reads stopping,
	// and progress on a stopping run keeps its state.
	if err := cancel(ctx, cancelFirst); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
		return f.Runs.Progress(ctx, b, cancelFirst.ID, 70, 100, "Ace Chemicals")
	}); err != nil {
		t.Fatalf("Progress on a stopping run: %v", err)
	}
	if got := f.get(t, cancelFirst.ID); got.State != tasks.StateStopping || got.Done != 70 {
		t.Fatalf("after the cancel then a tick: state %s done %d", got.State, got.Done)
	}

	// A free race: whatever interleaving, the run ends stopping.
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 20 {
			if err := f.tick(ctx, raced.ID, tasks.StateRunning, i); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	wg.Go(func() {
		time.Sleep(5 * time.Millisecond)
		if err := cancel(ctx, raced); err != nil {
			t.Errorf("raced cancel: %v", err)
		}
	})
	wg.Wait()
	if got := f.get(t, raced.ID); got.State != tasks.StateStopping {
		t.Fatalf("after the race: state %s, want stopping", got.State)
	}
}

// testLedgerCommitAfterTerminalRefused (R6-1, DATA6-1): a ledger write
// racing a terminal transition writes no ledger, checkpoint or mark once the
// run is terminal.
func testLedgerCommitAfterTerminalRefused(t *testing.T, f *fixture) {
	r := rec("case-ledger", kindCase, tasks.StateRunning)
	f.insert(t, r)
	stageLedger := func(ctx context.Context, b *storage.Batch) error {
		if err := f.Runs.SaveChunk(ctx, b, r.ID, tasks.Chunk{N: 3, Keys: []string{"joker", "penguin"}, Complete: true, Done: 2}); err != nil {
			return err
		}
		if err := f.Runs.SaveCheckpoint(ctx, b, r.ID, "watermark", []byte("penguin")); err != nil {
			return err
		}
		return f.Runs.MarkRow(ctx, b, r.ID, "joker")
	}
	ctx := t.Context()
	bl := f.open()
	defer closeBatch(t, bl)
	if err := stageLedger(ctx, bl); err != nil {
		t.Fatalf("stage the ledger: %v", err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := f.transition(ctx, r, tasks.StateSucceeded, nil)
		finished <- err
	}()
	synctest.Wait()
	if doneErr := <-finished; doneErr != nil {
		t.Fatalf("terminal transition: %v", doneErr)
	}
	if err := bl.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("ledger commit after the terminal transition: %v, want storage.ErrConflict", err)
	}
	// The store itself refuses each ledger write on the terminal run.
	for name, stage := range map[string]func(ctx context.Context, b *storage.Batch) error{
		"SaveChunk": func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.SaveChunk(ctx, b, r.ID, tasks.Chunk{N: 3})
		},
		"SaveCheckpoint": func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.SaveCheckpoint(ctx, b, r.ID, "watermark", []byte("penguin"))
		},
		"MarkRow": func(ctx context.Context, b *storage.Batch) error { return f.Runs.MarkRow(ctx, b, r.ID, "joker") },
		"Progress": func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.Progress(ctx, b, r.ID, 9, 9, "Arkham Asylum")
		},
	} {
		if err := f.writeRuns(ctx, stage); !errors.Is(err, tasks.ErrRunConflict) {
			t.Errorf("%s on a terminal run: %v, want ErrRunConflict", name, err)
		}
	}
	if err := f.ledger(ctx, r.ID, r.Attempt, r.FenceEpoch, stageLedger); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("ledger retry: %v, want ErrRunConflict", err)
	}
	if got := collect(t, f.Runs.Chunks(ctx, r.ID)); len(got) != 0 {
		t.Errorf("chunks on the terminal run: %+v", got)
	}
	if _, ok, err := f.Runs.Checkpoint(ctx, r.ID, "watermark"); err != nil || ok {
		t.Errorf("checkpoint on the terminal run: ok=%v err=%v", ok, err)
	}
	if got := collect(t, f.Runs.Marks(ctx, r.ID)); len(got) != 0 {
		t.Errorf("marks on the terminal run: %v", got)
	}
	if got := f.get(t, r.ID); got.State != tasks.StateSucceeded {
		t.Errorf("state %s, want succeeded", got.State)
	}
}

// testConcurrentTransitionsAllCommit (R7-3, DATA7-2): N runs complete at
// once; every terminal transition commits and Counts is exact.
func testConcurrentTransitionsAllCommit(t *testing.T, f *fixture) {
	const n = 24
	var runs []tasks.RunRecord
	for i := range n {
		runs = append(runs, rec(fmt.Sprintf("patrol-%02d", i), kindPatrol, tasks.StateRunning))
	}
	f.insert(t, runs...)
	ctx := t.Context()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i, r := range runs {
		wg.Go(func() {
			errs[i] = f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
				next := r
				next.State, next.Finished, next.Seq.Event = tasks.StateSucceeded, base().Add(time.Hour), 1
				if err := f.Runs.Transition(ctx, b, r.ID, want(r), next); err != nil {
					return err
				}
				return f.Runs.AppendEvent(ctx, b, r.ID, 1, tasks.RunEvent{From: tasks.StateRunning, To: tasks.StateSucceeded, Event: "complete", At: base()})
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("transition of run %d: %v", i, err)
		}
	}
	if c := f.counts(t); c[tasks.StateSucceeded] != n || c[tasks.StateRunning] != 0 || len(c) != 1 {
		t.Fatalf("Counts = %v, want exactly %d succeeded", c, n)
	}
	for _, r := range runs {
		if ev := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageEvents})); len(ev) != 1 {
			t.Errorf("run %s has %d events, want 1", r.ID, len(ev))
		}
	}
}

// testCancelCommitsUnderProgressTicks (R7-3, DATA7-2): an HTTP cancel races
// a stream of progress ticks of the same run and commits; no tick reverts it.
func testCancelCommitsUnderProgressTicks(t *testing.T, f *fixture) {
	r := rec("patrol-ticking", kindPatrol, tasks.StateRunning)
	f.insert(t, r)
	ctx := t.Context()
	staged, ticked := make(chan struct{}), make(chan struct{})
	var tickerErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-staged:
				// A tick commits between the cancel's staging and its
				// commit, so the cancel's first commit conflicts.
				tickerErr = f.tick(ctx, r.ID, tasks.StateRunning, i)
				close(ticked)
				if tickerErr != nil {
					return
				}
			default:
			}
			if err := f.tick(ctx, r.ID, tasks.StateRunning, i); err != nil {
				if !errors.Is(err, tasks.ErrRunConflict) {
					t.Errorf("tick: %v", err)
				}
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	attempts := 0
	var cancelErr error
	wg.Go(func() {
		time.Sleep(3 * time.Millisecond)
		cancelErr = f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
			attempts++
			next := r
			next.State, next.Reason = tasks.StateStopping, "cancel"
			// want is the run's state, attempt and fence: progress
			// ticks change none of them, so a re-stage still matches.
			if err := f.Runs.Transition(ctx, b, r.ID, want(r), next); err != nil {
				return err
			}
			if attempts == 1 {
				staged <- struct{}{}
				<-ticked
			}
			return nil
		})
	})
	wg.Wait()
	if tickerErr != nil {
		t.Fatalf("the interleaved tick: %v", tickerErr)
	}
	if cancelErr != nil {
		t.Fatalf("cancel under ticks: %v", cancelErr)
	}
	if attempts < 2 {
		t.Fatalf("cancel committed in %d attempt(s); the interleaved tick should have forced a retry", attempts)
	}
	if got := f.get(t, r.ID); got.State != tasks.StateStopping || got.Reason != "cancel" {
		t.Fatalf("after the cancel: state %s reason %q", got.State, got.Reason)
	}
	if err := f.tick(ctx, r.ID, tasks.StateRunning, 99); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("tick after the cancel: %v, want ErrRunConflict", err)
	}
}

// testPruneUnderConcurrentCompletions (DATA8-8): Prune deletes terminal runs
// while other runs complete; every chunk commits, Counts stays exact, and a
// run re-queued under Prune is either deleted before its re-queue or kept.
func testPruneUnderConcurrentCompletions(t *testing.T, f *fixture) {
	const old, live = 150, 16
	var stale, running []tasks.RunRecord
	for i := range old {
		r := rec(fmt.Sprintf("intake-%03d", i), kindIntake, tasks.StateCanceled)
		stale = append(stale, r)
	}
	for i := range live {
		running = append(running, rec(fmt.Sprintf("patrol-%02d", i), kindPatrol, tasks.StateRunning))
	}
	f.insert(t, stale...)
	f.insert(t, running...)
	for _, r := range stale[:3] {
		if err := f.once(t.Context(), func(b *storage.Batch) error {
			return f.Runs.AppendLog(t.Context(), b, r.ID, 1, tasks.LogLine{At: base(), Level: "info", Message: "Bat-Signal lit"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := base().Add(3 * time.Hour)
	requeued := stale[len(stale)-1]
	ctx := t.Context()
	var wg sync.WaitGroup
	var pruned int
	var pruneErr, requeueErr error
	wg.Go(func() { pruned, pruneErr = f.Runs.Prune(ctx, cutoff, 1000) })
	wg.Go(func() {
		_, requeueErr = f.transition(ctx, requeued, tasks.StateQueued, func(n *tasks.RunRecord) { n.Attempt++ })
	})
	for _, r := range running {
		wg.Go(func() {
			if _, err := f.transition(ctx, r, tasks.StateSucceeded, func(n *tasks.RunRecord) { n.Finished = cutoff.Add(time.Hour) }); err != nil {
				t.Errorf("completion of %s: %v", r.ID, err)
			}
		})
	}
	wg.Wait()
	if pruneErr != nil {
		t.Fatalf("Prune: %v", pruneErr)
	}
	c := f.counts(t)
	if c[tasks.StateSucceeded] != live || c[tasks.StateRunning] != 0 {
		t.Fatalf("Counts = %v, want %d succeeded", c, live)
	}
	_, gerr := f.Runs.Get(ctx, requeued.ID)
	switch {
	case requeueErr == nil:
		// The re-queue won: Prune skipped the run on its fresh read.
		if gerr != nil || pruned != old-1 || c[tasks.StateQueued] != 1 || c[tasks.StateCanceled] != 0 {
			t.Fatalf("re-queue won: get %v, pruned %d, counts %v", gerr, pruned, c)
		}
	case errors.Is(requeueErr, tasks.ErrRunNotFound):
		if !errors.Is(gerr, tasks.ErrRunNotFound) || pruned != old || c[tasks.StateQueued] != 0 || c[tasks.StateCanceled] != 0 {
			t.Fatalf("prune won: get %v, pruned %d, counts %v", gerr, pruned, c)
		}
	default:
		t.Fatalf("re-queue: %v", requeueErr)
	}
	for _, r := range stale[:3] {
		if got := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageLog})); len(got) != 0 {
			t.Errorf("pruned run %s kept %d log lines", r.ID, len(got))
		}
	}
}

// testTransitionVsCancelOneWins (R5-4, DATA5-1): an executor completion and
// an HTTP cancel race on one run; exactly one commits and the other gets
// ErrRunConflict. The same for two ClockStore.Advance calls on one clock.
func testTransitionVsCancelOneWins(t *testing.T, f *fixture) {
	r := rec("case-contested", kindCase, tasks.StateRunning)
	f.insert(t, r)
	c0 := tasks.Clock{LastDue: base(), LastRun: base(), ScheduleHash: "nightly"}
	if err := f.once(t.Context(), func(b *storage.Batch) error {
		return f.Clocks.Advance(t.Context(), b, kindPatrol, tasks.Clock{}, false, c0)
	}); err != nil {
		t.Fatalf("seed clock: %v", err)
	}
	ctx := t.Context()
	// race runs two builds whose first attempts both stage before
	// either commits, and returns their results.
	race := func(a, b func(ctx context.Context, bt *storage.Batch) error) [2]error {
		staged := make(chan struct{}, 2)
		release := make(chan struct{})
		var out [2]error
		var wg sync.WaitGroup
		for i, stage := range []func(ctx context.Context, bt *storage.Batch) error{a, b} {
			wg.Go(func() {
				first := true
				out[i] = f.writeRuns(ctx, func(ctx context.Context, bt *storage.Batch) error {
					err := stage(ctx, bt)
					if first {
						first = false
						staged <- struct{}{}
						<-release
					}
					return err
				})
			})
		}
		<-staged
		<-staged
		close(release)
		wg.Wait()
		return out
	}
	oneWins := func(what string, res [2]error, loser error) int {
		t.Helper()
		won := -1
		for i, err := range res {
			switch {
			case err == nil && won == -1:
				won = i
			case err == nil:
				t.Fatalf("%s: both commit", what)
			case !errors.Is(err, loser):
				t.Fatalf("%s: the loser got %v, want %v", what, err, loser)
			}
		}
		if won == -1 {
			t.Fatalf("%s: neither commits: %v", what, res)
		}
		return won
	}
	finish := func(state tasks.RunState, reason string) func(ctx context.Context, b *storage.Batch) error {
		return func(ctx context.Context, b *storage.Batch) error {
			next := r
			next.State, next.Reason = state, reason
			if state.Terminal() {
				next.Finished = base().Add(time.Hour)
			}
			return f.Runs.Transition(ctx, b, r.ID, want(r), next)
		}
	}
	won := oneWins("completion vs cancel", race(finish(tasks.StateSucceeded, ""), finish(tasks.StateStopping, "cancel")), tasks.ErrRunConflict)
	wantState := []tasks.RunState{tasks.StateSucceeded, tasks.StateStopping}[won]
	if got := f.get(t, r.ID); got.State != wantState {
		t.Fatalf("state %s, want the winner's %s", got.State, wantState)
	}

	advance := func(next tasks.Clock, prev tasks.Clock, had bool, k tasks.Kind) func(ctx context.Context, b *storage.Batch) error {
		return func(ctx context.Context, b *storage.Batch) error {
			return f.Clocks.Advance(ctx, b, k, prev, had, next)
		}
	}
	c1 := c0
	c1.LastFire = base().Add(time.Hour)
	c2 := c0
	c2.LastFire = base().Add(2 * time.Hour)
	won = oneWins("two advances", race(advance(c1, c0, true, kindPatrol), advance(c2, c0, true, kindPatrol)), tasks.ErrClockConflict)
	got, ok, err := f.Clocks.Get(ctx, kindPatrol)
	if err != nil || !ok || !got.LastFire.Equal([]tasks.Clock{c1, c2}[won].LastFire) {
		t.Fatalf("clock after the race: %+v ok=%v err=%v", got, ok, err)
	}
	// Two first advances of a clock that does not exist yet.
	oneWins("two first advances", race(advance(c1, tasks.Clock{}, false, kindCase), advance(c2, tasks.Clock{}, false, kindCase)), tasks.ErrClockConflict)
}

// testFixerMarksLandDuringQuiesce (R7-3, DATA7-3): a Fixer row applies while
// a stand-down quiesces the run to stopping; its mark commits, and on resume
// that row is skipped.
func testFixerMarksLandDuringQuiesce(t *testing.T, f *fixture) {
	r := rec("case-fixer", kindCase, tasks.StateRunning)
	f.insert(t, r)
	rows := []tasks.Row{{ID: "plan:joker", Title: "Joker"}, {ID: "plan:penguin", Title: "Penguin"}, {ID: "plan:riddler", Title: "Riddler"}}
	if err := f.Runs.ReplaceRows(t.Context(), r.ID, "plan:", len(rows), func(i int) (tasks.Row, error) { return rows[i], nil }); err != nil {
		t.Fatalf("ReplaceRows: %v", err)
	}
	ctx := t.Context()
	bm := f.open()
	defer closeBatch(t, bm)
	if err := f.Runs.MarkRow(ctx, bm, r.ID, "plan:joker"); err != nil {
		t.Fatalf("MarkRow: %v", err)
	}
	quiesced := make(chan error, 1)
	go func() {
		_, err := f.transition(ctx, r, tasks.StateStopping, func(n *tasks.RunRecord) { n.Reason = "quiesce" })
		quiesced <- err
	}()
	synctest.Wait()
	if quiesceErr := <-quiesced; quiesceErr != nil {
		t.Fatalf("quiesce: %v", quiesceErr)
	}
	if err := bm.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("mark staged before the quiesce: %v, want storage.ErrConflict", err)
	}
	// The fresh read shows stopping under the same attempt and fence:
	// the mark is re-staged and commits.
	if err := f.ledger(ctx, r.ID, r.Attempt, r.FenceEpoch, func(ctx context.Context, b *storage.Batch) error {
		return f.Runs.MarkRow(ctx, b, r.ID, "plan:joker")
	}); err != nil {
		t.Fatalf("mark during the quiesce: %v", err)
	}
	// The holder parks the run, releases it, and the run resumes.
	cur := f.get(t, r.ID)
	parked, err := f.transition(ctx, cur, tasks.StateInterruptedQuiesced, nil)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := f.transition(ctx, parked, tasks.StateQueued, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(ctx, queued, tasks.StateRunning, func(n *tasks.RunRecord) { n.Attempt++; n.FenceEpoch++ }); err != nil {
		t.Fatal(err)
	}
	marked := map[string]bool{}
	for _, id := range collect(t, f.Runs.Marks(ctx, r.ID)) {
		marked[id] = true
	}
	var applied []string
	for _, row := range collect(t, f.Runs.Rows(ctx, r.ID)) {
		if !marked[row.ID] {
			applied = append(applied, row.ID)
		}
	}
	if !slices.Equal(applied, []string{"plan:penguin", "plan:riddler"}) {
		t.Fatalf("resume applies %v, want the two unmarked rows", applied)
	}
}

// testCommitTimeWriteCheck (R6-1): every write staged over a read of the run
// fails at commit when another actor changes the run in between; Insert
// conflicts with a concurrent Insert, Debounce with a concurrent merge and
// Advance with a concurrent advance.
func testCommitTimeWriteCheck(t *testing.T, f *fixture) {
	ctx := t.Context()
	n := 0
	for name, c := range writeCases(f) {
		n++
		id := tasks.RunID(fmt.Sprintf("case-check-%02d", n))
		state := tasks.StateRunning
		if c.terminal {
			state = tasks.StateSucceeded
		}
		r := rec(string(id), kindCase, state)
		f.insert(t, r)
		b := f.open()
		if err := c.stage(ctx, b, id); err != nil {
			closeBatch(t, b)
			t.Fatalf("%s: stage: %v", name, err)
		}
		// Another actor changes the run (or the guard key) and commits.
		var err error
		if c.interfere != nil {
			err = c.interfere(ctx, id)
		} else {
			_, err = f.transition(ctx, r, state, func(n *tasks.RunRecord) { n.Reason = "interfere" })
		}
		if err != nil {
			closeBatch(t, b)
			t.Fatalf("%s: interfere: %v", name, err)
		}
		if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Errorf("%s: commit after a concurrent change: %v, want storage.ErrConflict", name, err)
		}
		closeBatch(t, b)
	}
}

// writeCase is one staging method of the stores.
type writeCase struct {
	terminal  bool // the method acts on a terminal run (Delete)
	stage     func(ctx context.Context, b *storage.Batch, id tasks.RunID) error
	interfere func(ctx context.Context, id tasks.RunID) error // nil: rewrite the run row
}

// writeCases are every RunStore write that takes b and reads the run, plus
// the guard writes that read their own key.
func writeCases(f *fixture) map[string]writeCase {
	return map[string]writeCase{
		"Transition": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			r, err := f.Runs.Get(ctx, id)
			if err != nil {
				return err
			}
			n := r
			n.State = tasks.StateFailed
			n.Finished = base()
			return f.Runs.Transition(ctx, b, id, want(r), n)
		}},
		"Progress": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.Progress(ctx, b, id, 1, 2, "Wayne Manor")
		}},
		"AppendEvent": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.AppendEvent(ctx, b, id, 1, tasks.RunEvent{Event: "claim", At: base()})
		}},
		"AppendIntent": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.AppendIntent(ctx, b, id, 1, tasks.Write{Name: "gotham.lock_up", Subject: villain("joker")}, []tasks.Change{{Subject: villain("joker"), Field: "cell", Old: "", New: "A-13"}})
		}},
		"PutItem": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.PutItem(ctx, b, id, "joker", tasks.ItemOutcome{Key: "joker", Outcome: "failed", Error: "escaped"})
		}},
		"AppendLog": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.AppendLog(ctx, b, id, 1, tasks.LogLine{At: base(), Level: "warn", Message: "Arkham breach"})
		}},
		"SaveCheckpoint": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.SaveCheckpoint(ctx, b, id, "watermark", []byte("riddler"))
		}},
		"SaveChunk": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.SaveChunk(ctx, b, id, tasks.Chunk{N: 0, Keys: []string{"joker"}})
		}},
		"MarkRow": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.MarkRow(ctx, b, id, "plan:joker")
		}},
		"Strike": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.Strike(ctx, b, id, tasks.Strike{Kind: "stuck", Detail: "no progress since Crime Alley", At: base()})
		}},
		"Delete": {terminal: true, stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.Delete(ctx, b, id)
		}},
		"Insert": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.Insert(ctx, b, rec(string(id)+"-twin", kindCase, tasks.StateQueued))
		}, interfere: func(ctx context.Context, id tasks.RunID) error {
			return f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
				return f.Runs.Insert(ctx, b, rec(string(id)+"-twin", kindCase, tasks.StateQueued))
			})
		}},
		"Debounce": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindPatrol, Bucket: string(id), Subjects: []entity.Ref{villain("joker")}, First: base(), Last: base()})
		}, interfere: func(ctx context.Context, id tasks.RunID) error {
			return f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
				return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindPatrol, Bucket: string(id), Subjects: []entity.Ref{villain("bane")}, First: base(), Last: base()})
			})
		}},
		"ClockAdvance": {stage: func(ctx context.Context, b *storage.Batch, id tasks.RunID) error {
			return f.Clocks.Advance(ctx, b, tasks.Kind("gotham."+string(id)), tasks.Clock{}, false, tasks.Clock{LastRun: base()})
		}, interfere: func(ctx context.Context, id tasks.RunID) error {
			return f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
				return f.Clocks.Advance(ctx, b, tasks.Kind("gotham."+string(id)), tasks.Clock{}, false, tasks.Clock{LastRun: base().Add(time.Hour)})
			})
		}},
	}
}

// snapshot renders everything the stores and the record reader expose about
// the fixture's names, so two snapshots compare equal exactly when nothing
// observable changed.
func (f *fixture) snapshot(t *testing.T, runIDs []tasks.RunID) string {
	t.Helper()
	ctx := t.Context()
	var sb strings.Builder
	sb.WriteString(f.runSnapshot(t, runIDs))
	p := func(format string, args ...any) { fmt.Fprintf(&sb, format+"\n", args...) }
	p("counts %v", f.counts(t))
	p("queued %v", ids(collect(t, f.Runs.Queued(ctx))))
	p("unfinished %v", ids(collect(t, f.Runs.Unfinished(ctx))))
	p("list %v", ids(collect(t, f.Runs.List(ctx, tasks.RunQuery{Limit: 1000}))))
	p("buckets %+v", collect(t, f.Runs.DebounceBuckets(ctx)))
	for _, k := range []tasks.Kind{kindPatrol, kindCase, kindIntake, "gotham.renamed"} {
		c, ok, err := f.Clocks.Get(ctx, k)
		p("clock %s %+v %v %v", k, c, ok, err)
		at, ok, err := f.Runs.Stamped(ctx, villain("joker"), k)
		p("stamp %s %v %v %v", k, at, ok, err)
	}
	pm, ok, err := f.Pause.Get(ctx)
	p("pause %+v %v %v", pm, ok, err)
	p("standdown %+v", collect(t, f.StandDown.List(ctx)))
	return sb.String()
}

// runSnapshot renders everything the stores expose about each run of
// runIDs.
func (f *fixture) runSnapshot(t *testing.T, runIDs []tasks.RunID) string {
	t.Helper()
	ctx := t.Context()
	var sb strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&sb, format+"\n", args...) }
	for _, id := range runIDs {
		r, err := f.Runs.Get(ctx, id)
		p("run %s: %+v err=%v", id, r, err)
		for _, page := range []tasks.PageKind{tasks.PageEvents, tasks.PageIntents, tasks.PageItems, tasks.PageLog} {
			p("  page %d: %+v", page, collect(t, f.Runs.Pages(ctx, id, tasks.PageQuery{Page: page, Limit: 1000})))
		}
		cp, ok, err := f.Runs.Checkpoint(ctx, id, "watermark")
		p("  checkpoint %q %v %v", cp, ok, err)
		p("  chunks %+v", collect(t, f.Runs.Chunks(ctx, id)))
		p("  rows %+v", collect(t, f.Runs.Rows(ctx, id)))
		p("  marks %v", collect(t, f.Runs.Marks(ctx, id)))
		p("  strikes %+v", collect(t, f.Runs.Strikes(ctx, id)))
	}
	return sb.String()
}

// testDiscardedBatchLeavesNothing: every staging method writes only into the
// caller's batch, so a batch closed without a commit leaves the stores as
// they were (Prune and ReplaceRows commit their own batches and are not
// staging methods).
func testDiscardedBatchLeavesNothing(t *testing.T, f *fixture) {
	ctx := t.Context()
	live := rec("patrol-live", kindPatrol, tasks.StateRunning)
	done := rec("patrol-done", kindPatrol, tasks.StateSucceeded)
	f.insert(t, live, done)
	if err := f.once(ctx, func(b *storage.Batch) error {
		return f.Clocks.Advance(ctx, b, kindPatrol, tasks.Clock{}, false, tasks.Clock{LastRun: base()})
	}); err != nil {
		t.Fatal(err)
	}
	all := []tasks.RunID{live.ID, done.ID, "patrol-new", "patrol-imported"}
	before := f.snapshot(t, all)
	stagers := map[string]func(ctx context.Context, b *storage.Batch) error{
		"Insert": func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.Insert(ctx, b, rec("patrol-new", kindPatrol, tasks.StateQueued))
		},
		"Transition": func(ctx context.Context, b *storage.Batch) error {
			n := live
			n.State = tasks.StateStopping
			return f.Runs.Transition(ctx, b, live.ID, want(live), n)
		},
		"Debounce flush": func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindPatrol})
		},
		"Stamp": func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.Stamp(ctx, b, villain("joker"), kindPatrol, base())
		},
		"Pause.Set": func(ctx context.Context, b *storage.Batch) error {
			return f.Pause.Set(ctx, b, &tasks.Pause{Reason: "Bat-Signal maintenance", By: tasks.Actor{UserID: "alfred"}, At: base()})
		},
		"StandDown.Set": func(ctx context.Context, b *storage.Batch) error {
			return f.StandDown.Set(ctx, b, "gotham.blackout", &tasks.StandDown{Holder: live.ID, Quiesced: []tasks.RunID{done.ID}, Expires: base()})
		},
		"Clocks.Advance": func(ctx context.Context, b *storage.Batch) error {
			return f.Clocks.Advance(ctx, b, kindPatrol, tasks.Clock{LastRun: base()}, true, tasks.Clock{LastRun: base().Add(time.Hour)})
		},
	}
	for name, c := range writeCases(f) {
		id := live.ID
		if c.terminal {
			id = done.ID
		}
		stagers[name] = func(ctx context.Context, b *storage.Batch) error { return c.stage(ctx, b, id) }
	}
	if f.imp != nil {
		imported := rec("patrol-imported", kindPatrol, tasks.StateSucceeded)
		stagers["Importer.Run+RunData"] = func(ctx context.Context, b *storage.Batch) error {
			if err := f.imp.Run(ctx, b, imported); err != nil {
				return err
			}
			return f.imp.RunData(ctx, b, imported.ID, tasks.RunData{
				Pages:       []tasks.PageEntry{{Page: tasks.PageLog, Seq: 1, Log: tasks.LogLine{Message: "Batmobile ready"}}},
				Checkpoints: map[string][]byte{"watermark": []byte("joker")},
				Chunks:      []tasks.Chunk{{N: 0}}, Rows: []tasks.Row{{ID: "plan:joker"}}, Marks: []string{"plan:joker"},
			})
		}
		stagers["Importer.Clock"] = func(ctx context.Context, b *storage.Batch) error {
			return f.imp.Clock(ctx, b, kindIntake, tasks.Clock{LastRun: base()})
		}
		stagers["Importer.Pause"] = func(ctx context.Context, b *storage.Batch) error {
			return f.imp.Pause(ctx, b, tasks.Pause{Reason: "cutover"})
		}
		stagers["Importer.StandDown"] = func(ctx context.Context, b *storage.Batch) error {
			return f.imp.StandDown(ctx, b, tasks.StandDown{Key: "gotham.blackout", Holder: live.ID})
		}
		stagers["Importer.Debounce"] = func(ctx context.Context, b *storage.Batch) error {
			return f.imp.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindCase, Subjects: []entity.Ref{villain("joker")}, First: base(), Last: base()})
		}
		stagers["Importer.Stamp"] = func(ctx context.Context, b *storage.Batch) error {
			return f.imp.Stamp(ctx, b, villain("joker"), kindCase, base())
		}
		stagers["Importer.RenameKind"] = func(ctx context.Context, b *storage.Batch) error {
			_, err := f.imp.RenameKind(ctx, b, kindPatrol, "gotham.renamed", "", 100)
			return err
		}
	}
	for name, stage := range stagers {
		b := f.open()
		if err := stage(ctx, b); err != nil {
			closeBatch(t, b)
			t.Fatalf("%s: stage: %v", name, err)
		}
		if err := b.Close(); err != nil {
			t.Fatalf("%s: discard: %v", name, err)
		}
		if after := f.snapshot(t, all); after != before {
			t.Errorf("%s: a discarded batch changed the stores:\nbefore:\n%s\nafter:\n%s", name, before, after)
		}
	}
}

// testOneRowPerRun: a run ID has exactly one row: a second Insert is refused
// at staging, and of two concurrent Inserts exactly one commits.
func testOneRowPerRun(t *testing.T, f *fixture) {
	ctx := t.Context()
	r := rec("patrol-once", kindPatrol, tasks.StateQueued)
	f.insert(t, r)
	if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error { return f.Runs.Insert(ctx, b, r) }); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("second Insert: %v, want ErrRunConflict", err)
	}
	twin := rec("patrol-twin", kindPatrol, tasks.StateQueued)
	b1, b2 := f.open(), f.open()
	defer closeBatch(t, b1)
	defer closeBatch(t, b2)
	for _, b := range []*storage.Batch{b1, b2} {
		if err := f.Runs.Insert(ctx, b, twin); err != nil {
			t.Fatalf("stage Insert: %v", err)
		}
	}
	if err := b1.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("first concurrent Insert: %v", err)
	}
	if err := b2.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second concurrent Insert: %v, want storage.ErrConflict", err)
	}
	listed := ids(collect(t, f.Runs.List(ctx, tasks.RunQuery{})))
	if len(listed) != 2 || f.counts(t)[tasks.StateQueued] != 2 {
		t.Fatalf("List = %v, Counts = %v; want one row per run", listed, f.counts(t))
	}
	if got := collect(t, f.Runs.Queued(ctx)); len(got) != 2 {
		t.Fatalf("Queued = %v", ids(got))
	}
	if _, err := f.Runs.Get(ctx, "patrol-nobody"); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("Get of a missing run: %v, want ErrRunNotFound", err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error {
		return f.Runs.Progress(ctx, b, "patrol-nobody", 1, 1, "")
	}); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("Progress of a missing run: %v, want ErrRunNotFound", err)
	}
}

// testPerRunSeqAdvances: the fenced owner advances the row's Seq in the
// batch that appends the page entry, and the pages read back in seq order.
func testPerRunSeqAdvances(t *testing.T, f *fixture) {
	ctx := t.Context()
	r := rec("case-seq", kindCase, tasks.StateRunning)
	f.insert(t, r)
	cur := r
	for i := 1; i <= 3; i++ {
		next := cur
		next.Seq.Event = cur.Seq.Event + 1
		next.Seq.Log = cur.Seq.Log + 1
		if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
			if err := f.Runs.Transition(ctx, b, r.ID, want(cur), next); err != nil {
				return err
			}
			if err := f.Runs.AppendEvent(ctx, b, r.ID, next.Seq.Event, tasks.RunEvent{Event: fmt.Sprintf("tick-%d", i), At: base()}); err != nil {
				return err
			}
			return f.Runs.AppendLog(ctx, b, r.ID, next.Seq.Log, tasks.LogLine{At: base(), Message: fmt.Sprintf("Batarang %d", i)})
		}); err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		cur = next
	}
	if got := f.get(t, r.ID).Seq; got != (tasks.RunSeqs{Event: 3, Log: 3}) {
		t.Fatalf("Seq = %+v, want {3 0 3}", got)
	}
	ev := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageEvents}))
	if len(ev) != 3 || ev[0].Seq != 1 || ev[2].Seq != 3 || ev[1].Key != "2" || ev[2].Event.Event != "tick-3" {
		t.Fatalf("events = %+v", ev)
	}
	logs := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageLog, After: "1", Limit: 1}))
	if len(logs) != 1 || logs[0].Seq != 2 || logs[0].Log.Message != "Batarang 2" {
		t.Fatalf("log page after 1, limit 1 = %+v", logs)
	}
}

// testPruneTerminalOnly: retention deletes only terminal runs finished
// before the cutoff, honours its limit, and Delete refuses an active run.
func testPruneTerminalOnly(t *testing.T, f *fixture) {
	ctx := t.Context()
	var terminal []tasks.RunRecord
	for i, s := range []tasks.RunState{tasks.StateSucceeded, tasks.StateFailed, tasks.StateTimedOut, tasks.StateCanceled, tasks.StateInterruptedDropped} {
		terminal = append(terminal, rec(fmt.Sprintf("intake-old-%d", i), kindIntake, s))
	}
	var active []tasks.RunRecord
	for _, s := range unfinished() {
		active = append(active, rec("intake-active-"+string(s), kindIntake, s))
	}
	recent := rec("intake-recent", kindIntake, tasks.StateSucceeded)
	recent.Finished = base().Add(48 * time.Hour)
	f.insert(t, terminal...)
	f.insert(t, active...)
	f.insert(t, recent)
	if _, err := f.Runs.Prune(ctx, base(), 0); err == nil {
		t.Fatal("Prune with limit 0 succeeded")
	}
	cutoff := base().Add(24 * time.Hour)
	n, err := f.Runs.Prune(ctx, cutoff, 2)
	if err != nil || n != 2 {
		t.Fatalf("Prune limit 2 = %d, %v", n, err)
	}
	n, err = f.Runs.Prune(ctx, cutoff, 100)
	if err != nil || n != len(terminal)-2 {
		t.Fatalf("second Prune = %d, %v, want %d", n, err, len(terminal)-2)
	}
	for _, r := range terminal {
		if _, err := f.Runs.Get(ctx, r.ID); !errors.Is(err, tasks.ErrRunNotFound) {
			t.Errorf("terminal %s survived Prune: %v", r.ID, err)
		}
	}
	for _, r := range append(active, recent) {
		f.get(t, r.ID)
	}
	for _, r := range active {
		if err := f.once(ctx, func(b *storage.Batch) error { return f.Runs.Delete(ctx, b, r.ID) }); !errors.Is(err, tasks.ErrRunActive) {
			t.Errorf("Delete of %s run: %v, want ErrRunActive", r.State, err)
		}
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Runs.Delete(ctx, b, "intake-nobody") }); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Errorf("Delete of a missing run: %v, want ErrRunNotFound", err)
	}
}

func unfinished() []tasks.RunState {
	return []tasks.RunState{tasks.StateQueued, tasks.StateWaitingDeps, tasks.StateRunning, tasks.StateStopping,
		tasks.StateInterrupted, tasks.StateInterruptedQuiesced, tasks.StateAwaitingDecision}
}

// testDeleteRemovesEveryRecord: Delete removes a terminal run with its
// pages, checkpoints, chunks, rows, marks and strikes, and nothing of
// another run.
func testDeleteRemovesEveryRecord(t *testing.T, f *fixture) {
	ctx := t.Context()
	gone := rec("case-gone", kindCase, tasks.StateRunning)
	kept := rec("case-kept", kindCase, tasks.StateRunning)
	f.insert(t, gone, kept)
	for _, r := range []tasks.RunRecord{gone, kept} {
		if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
			for name, c := range writeCases(f) {
				if c.interfere != nil || name == "Transition" || c.terminal {
					continue
				}
				if err := c.stage(ctx, b, r.ID); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
		if err := f.Runs.ReplaceRows(ctx, r.ID, "plan:", 1, func(int) (tasks.Row, error) { return tasks.Row{ID: "plan:joker"}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	keptBefore := f.runSnapshot(t, []tasks.RunID{kept.ID})
	cur := f.get(t, gone.ID)
	if _, err := f.transition(ctx, cur, tasks.StateFailed, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error { return f.Runs.Delete(ctx, b, gone.ID) }); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for page := tasks.PageEvents; page <= tasks.PageLog; page++ {
		if got := collect(t, f.Runs.Pages(ctx, gone.ID, tasks.PageQuery{Page: page})); len(got) != 0 {
			t.Errorf("page %d of the deleted run: %+v", page, got)
		}
	}
	if _, ok, err := f.Runs.Checkpoint(ctx, gone.ID, "watermark"); err != nil || ok {
		t.Errorf("checkpoint of the deleted run (found=%v, err=%v)", ok, err)
	}
	if len(collect(t, f.Runs.Chunks(ctx, gone.ID))) != 0 || len(collect(t, f.Runs.Rows(ctx, gone.ID))) != 0 ||
		len(collect(t, f.Runs.Marks(ctx, gone.ID))) != 0 || len(collect(t, f.Runs.Strikes(ctx, gone.ID))) != 0 {
		t.Error("resume state or strikes of the deleted run remain")
	}
	if c := f.counts(t); c[tasks.StateFailed] != 0 || c[tasks.StateRunning] != 1 {
		t.Errorf("Counts after Delete = %v", c)
	}
	if after := f.runSnapshot(t, []tasks.RunID{kept.ID}); after != keptBefore {
		t.Errorf("Delete changed another run:\n%s\n%s", keptBefore, after)
	}
}

// testQueueOrder: Queued is dispatch order, PrioritySystem first, then
// enqueue time.
func testQueueOrder(t *testing.T, f *fixture) {
	mk := func(id string, p tasks.Priority, minutes int) tasks.RunRecord {
		r := rec(id, kindPatrol, tasks.StateQueued)
		r.Priority = p
		r.Created = base().Add(time.Duration(minutes) * time.Minute)
		return r
	}
	f.insert(t,
		mk("low-early", tasks.PriorityLow, 0),
		mk("normal-late", tasks.PriorityNormal, 9),
		mk("normal-early", tasks.PriorityNormal, 1),
		mk("system-late", tasks.PrioritySystem, 8),
		mk("high", tasks.PriorityHigh, 5),
	)
	f.insert(t, rec("running", kindPatrol, tasks.StateRunning))
	got := ids(collect(t, f.Runs.Queued(t.Context())))
	wantOrder := []tasks.RunID{"system-late", "high", "normal-early", "normal-late", "low-early"}
	if !slices.Equal(got, wantOrder) {
		t.Fatalf("Queued = %v, want %v", got, wantOrder)
	}
}

// testList: RunFilter order (Created descending, then RunID), every filter,
// the keyset cursor and the limit clamp.
func testList(t *testing.T, f *fixture) {
	ctx := t.Context()
	mk := func(id string, k tasks.Kind, s tasks.RunState, minutes int, parent tasks.RunID) tasks.RunRecord {
		r := rec(id, k, s)
		r.Created = base().Add(time.Duration(minutes) * time.Minute)
		r.Parent = parent
		return r
	}
	f.insert(t,
		mk("a", kindPatrol, tasks.StateSucceeded, 1, ""),
		mk("b", kindPatrol, tasks.StateRunning, 3, ""),
		mk("c", kindCase, tasks.StateQueued, 3, "b"),
		mk("d", kindCase, tasks.StateFailed, 2, "b"),
		mk("e", kindIntake, tasks.StateQueued, 0, ""),
	)
	list := func(q tasks.RunQuery) []tasks.RunID { return ids(collect(t, f.Runs.List(ctx, q))) }
	for name, c := range map[string]struct {
		q    tasks.RunQuery
		want []tasks.RunID
	}{
		"all":          {tasks.RunQuery{}, []tasks.RunID{"c", "b", "d", "a", "e"}},
		"kind":         {tasks.RunQuery{Kinds: []tasks.Kind{kindCase}}, []tasks.RunID{"c", "d"}},
		"two kinds":    {tasks.RunQuery{Kinds: []tasks.Kind{kindIntake, kindPatrol}}, []tasks.RunID{"b", "a", "e"}},
		"state":        {tasks.RunQuery{States: []tasks.RunState{tasks.StateQueued}}, []tasks.RunID{"c", "e"}},
		"kind+state":   {tasks.RunQuery{Kinds: []tasks.Kind{kindCase}, States: []tasks.RunState{tasks.StateQueued}}, []tasks.RunID{"c"}},
		"parent":       {tasks.RunQuery{Parent: "b"}, []tasks.RunID{"c", "d"}},
		"since":        {tasks.RunQuery{Since: base().Add(2 * time.Minute)}, []tasks.RunID{"c", "b", "d"}},
		"limit":        {tasks.RunQuery{Limit: 2}, []tasks.RunID{"c", "b"}},
		"after":        {tasks.RunQuery{After: "b", Limit: 2}, []tasks.RunID{"d", "a"}},
		"after+filter": {tasks.RunQuery{Kinds: []tasks.Kind{kindCase}, After: "c"}, []tasks.RunID{"d"}},
	} {
		if got := list(c.q); !slices.Equal(got, c.want) {
			t.Errorf("List %s = %v, want %v", name, got, c.want)
		}
	}
	for _, err := range f.Runs.List(ctx, tasks.RunQuery{After: "nobody"}) {
		if !errors.Is(err, tasks.ErrRunNotFound) {
			t.Errorf("List after a missing cursor: %v, want ErrRunNotFound", err)
		}
		break
	}
	var many []tasks.RunRecord
	for i := range 1005 {
		many = append(many, rec(fmt.Sprintf("bulk-%04d", i), kindIntake, tasks.StateSucceeded))
	}
	if err := f.once(ctx, func(b *storage.Batch) error {
		for _, r := range many {
			if err := f.Runs.Insert(ctx, b, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(list(tasks.RunQuery{})); n != 100 {
		t.Errorf("List with limit 0 = %d rows, want 100", n)
	}
	if n := len(list(tasks.RunQuery{Limit: 5000})); n != 1000 {
		t.Errorf("List with limit 5000 = %d rows, want 1000", n)
	}
}

// testPages: the four pages, the keyset cursor, the limit and the item
// outcome filter.
func testPages(t *testing.T, f *fixture) {
	ctx := t.Context()
	r := rec("case-pages", kindCase, tasks.StateRunning)
	f.insert(t, r)
	if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
		for i := uint64(1); i <= 5; i++ {
			if err := f.Runs.AppendIntent(ctx, b, r.ID, i, tasks.Write{Name: "gotham.lock_up", Subject: villain(fmt.Sprint(i))}, []tasks.Change{{Field: "cell", New: fmt.Sprint(i)}}); err != nil {
				return err
			}
		}
		for key, o := range map[string]string{"joker": "failed", "penguin": "skipped:held", "riddler": "skipped:missing", "bane": "failed"} {
			if err := f.Runs.PutItem(ctx, b, r.ID, key, tasks.ItemOutcome{Key: key, Outcome: o, Chunk: 1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	in := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageIntents, After: "2", Limit: 2}))
	if len(in) != 2 || in[0].Seq != 3 || in[1].Seq != 4 || in[0].Write.Subject.ID != "3" || in[1].Changes[0].New != "4" {
		t.Fatalf("intents after 2, limit 2 = %+v", in)
	}
	skipped := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageItems, Outcome: "skipped"}))
	if len(skipped) != 2 || skipped[0].Key != "penguin" || skipped[1].Item.Outcome != "skipped:missing" {
		t.Fatalf("skipped items = %+v", skipped)
	}
	after := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageItems, After: "joker"}))
	if len(after) != 2 || after[0].Key != "penguin" {
		t.Fatalf("items after joker = %+v", after)
	}
	for _, err := range f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: 0}) {
		if err == nil {
			t.Error("Pages of an unknown page: no error")
		}
	}
	for _, err := range f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageLog, After: "not-a-seq"}) {
		if err == nil {
			t.Error("Pages with a malformed seq cursor: no error")
		}
	}
}

// testReplaceRows: HEAD's ReplaceOpRowsPrefix contract (DATA9-7).
func testReplaceRows(t *testing.T, f *fixture) {
	ctx := t.Context()
	r := rec("case-rows", kindCase, tasks.StateRunning)
	f.insert(t, r)
	row := func(id string) tasks.Row { return tasks.Row{ID: id, Title: id, Subjects: []entity.Ref{villain(id)}} }
	plan := func(n int) func(int) (tasks.Row, error) {
		return func(i int) (tasks.Row, error) { return row(fmt.Sprintf("plan:%04d", i)), nil }
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "class:", 2, func(i int) (tasks.Row, error) {
		return row([]string{"class:a", "class:b"}[i]), nil
	}); err != nil {
		t.Fatal(err)
	}
	const big = 600 // more than one bounded batch
	if err := f.Runs.ReplaceRows(ctx, r.ID, "plan:", big, plan(big)); err != nil {
		t.Fatalf("ReplaceRows %d: %v", big, err)
	}
	if got := collect(t, f.Runs.Rows(ctx, r.ID)); len(got) != big+2 {
		t.Fatalf("rows = %d, want %d", len(got), big+2)
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "plan:", 3, plan(3)); err != nil {
		t.Fatal(err)
	}
	got := collect(t, f.Runs.Rows(ctx, r.ID))
	var gotIDs []string
	for _, x := range got {
		gotIDs = append(gotIDs, x.ID)
	}
	if !slices.Equal(gotIDs, []string{"class:a", "class:b", "plan:0000", "plan:0001", "plan:0002"}) {
		t.Fatalf("rows after replacing plan: = %v", gotIDs)
	}
	if got[0].Subjects[0] != villain("class:a") {
		t.Fatalf("row round trip: %+v", got[0])
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "class:", 0, nil); err != nil {
		t.Fatalf("clear class: %v", err)
	}
	if n := len(collect(t, f.Runs.Rows(ctx, r.ID))); n != 3 {
		t.Fatalf("rows after clearing class: = %d, want 3", n)
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "", 0, nil); err == nil {
		t.Error("ReplaceRows with an empty prefix succeeded")
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "plan:", 1, func(int) (tasks.Row, error) { return row("class:x"), nil }); err == nil {
		t.Error("ReplaceRows of a row outside its prefix succeeded")
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "plan:", 1, func(int) (tasks.Row, error) { return tasks.Row{}, errors.New("source failed") }); err == nil {
		t.Error("ReplaceRows with a failing source succeeded")
	}
	if err := f.Runs.ReplaceRows(ctx, "case-nobody", "plan:", 0, nil); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Errorf("ReplaceRows of a missing run: %v, want ErrRunNotFound", err)
	}
	if _, err := f.transition(ctx, r, tasks.StateSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Runs.ReplaceRows(ctx, r.ID, "plan:", 1, plan(1)); !errors.Is(err, tasks.ErrRunConflict) {
		t.Errorf("ReplaceRows on a terminal run: %v, want ErrRunConflict", err)
	}
}

// testDebounce: Debounce merges arrivals into the bucket under its guard
// precondition and a zero First flushes it.
func testDebounce(t *testing.T, f *fixture) {
	ctx := t.Context()
	arrive := func(bucket string, at time.Time, params string, who ...string) error {
		var subj []entity.Ref
		for _, w := range who {
			subj = append(subj, villain(w))
		}
		return f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
			return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindPatrol, Bucket: bucket, Subjects: subj, Params: json.RawMessage(params), First: at, Last: at})
		})
	}
	for _, err := range []error{
		arrive("east-end", base().Add(time.Minute), `{"n":1}`, "joker", "penguin"),
		arrive("east-end", base(), ``, "penguin", "riddler"),
		arrive("east-end", base().Add(2*time.Minute), `{"n":3}`, "bane"),
		arrive("", base(), ``, "catwoman"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := collect(t, f.Runs.DebounceBuckets(ctx))
	if len(got) != 2 {
		t.Fatalf("buckets = %+v", got)
	}
	east := got[1]
	if got[0].Bucket != "" {
		east = got[0]
	}
	var who []string
	for _, s := range east.Subjects {
		who = append(who, s.ID)
	}
	if !slices.Equal(who, []string{"joker", "penguin", "riddler", "bane"}) || !east.First.Equal(base()) ||
		!east.Last.Equal(base().Add(2*time.Minute)) || string(east.Params) != `{"n":3}` {
		t.Fatalf("merged bucket = %+v (subjects %v)", east, who)
	}
	if err := f.writeRuns(ctx, func(ctx context.Context, b *storage.Batch) error {
		return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindPatrol, Bucket: "east-end"})
	}); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := collect(t, f.Runs.DebounceBuckets(ctx)); len(got) != 1 || got[0].Subjects[0] != villain("catwoman") {
		t.Fatalf("buckets after the flush = %+v", got)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{}) }); err == nil {
		t.Error("Debounce without a kind succeeded")
	}
}

// testClockPauseStandDownStamps: the clock, pause, stand-down and stamp
// stores round-trip, and nil clears.
func testClockPauseStandDownStamps(t *testing.T, f *fixture) {
	ctx := t.Context()
	if _, ok, err := f.Clocks.Get(ctx, kindPatrol); ok || err != nil {
		t.Fatalf("clock before any advance: ok=%v err=%v", ok, err)
	}
	c := tasks.Clock{LastDue: base(), LastFire: base(), LastRun: base(), Missed: 2, GateClosedAt: base(), ScheduleHash: "h1"}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Clocks.Advance(ctx, b, kindPatrol, tasks.Clock{}, false, c) }); err != nil {
		t.Fatal(err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Clocks.Advance(ctx, b, kindPatrol, tasks.Clock{}, false, c) }); !errors.Is(err, tasks.ErrClockConflict) {
		t.Fatalf("first advance of an existing clock: %v, want ErrClockConflict", err)
	}
	stale := c
	stale.Missed = 1
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Clocks.Advance(ctx, b, kindPatrol, stale, true, c) }); !errors.Is(err, tasks.ErrClockConflict) {
		t.Fatalf("advance from a stale clock: %v, want ErrClockConflict", err)
	}
	got, ok, err := f.Clocks.Get(ctx, kindPatrol)
	if err != nil || !ok || got.Missed != 2 || !got.LastDue.Equal(base()) || got.ScheduleHash != "h1" {
		t.Fatalf("clock = %+v ok=%v err=%v", got, ok, err)
	}
	next := got
	next.Missed = 0
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Clocks.Advance(ctx, b, kindPatrol, got, true, next) }); err != nil {
		t.Fatalf("advance from the read clock: %v", err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Clocks.Advance(ctx, b, kindCase, next, true, next) }); !errors.Is(err, tasks.ErrClockConflict) {
		t.Fatalf("advance of a missing clock that claims one: %v, want ErrClockConflict", err)
	}

	p := tasks.Pause{Reason: "Bat-Signal maintenance", By: tasks.Actor{UserID: "alfred", AuthMethod: "session"}, At: base()}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Pause.Set(ctx, b, &p) }); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := f.Pause.Get(ctx); err != nil || !ok || got.Reason != p.Reason || got.By != p.By || !got.At.Equal(base()) {
		t.Fatalf("pause = %+v ok=%v err=%v", got, ok, err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Pause.Set(ctx, b, nil) }); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.Pause.Get(ctx); ok || err != nil {
		t.Fatalf("pause after clear: ok=%v err=%v", ok, err)
	}

	sd := tasks.StandDown{Key: "ignored", Holder: "case-holder", Quiesced: []tasks.RunID{"patrol-1", "patrol-2"}, Expires: base()}
	if err := f.once(ctx, func(b *storage.Batch) error {
		if err := f.StandDown.Set(ctx, b, "gotham.blackout", &sd); err != nil {
			return err
		}
		return f.StandDown.Set(ctx, b, "arkham.lockdown", &tasks.StandDown{Holder: "case-warden"})
	}); err != nil {
		t.Fatal(err)
	}
	list := collect(t, f.StandDown.List(ctx))
	if len(list) != 2 || list[0].Key != "arkham.lockdown" || list[1].Key != "gotham.blackout" || !slices.Equal(list[1].Quiesced, sd.Quiesced) {
		t.Fatalf("stand-downs = %+v", list)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.StandDown.Set(ctx, b, "gotham.blackout", nil) }); err != nil {
		t.Fatal(err)
	}
	if list := collect(t, f.StandDown.List(ctx)); len(list) != 1 {
		t.Fatalf("stand-downs after clear = %+v", list)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.StandDown.Set(ctx, b, "", &sd) }); err == nil {
		t.Error("stand-down without a key succeeded")
	}

	if err := f.once(ctx, func(b *storage.Batch) error {
		return f.Runs.Stamp(ctx, b, villain("joker"), kindPatrol, base().Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := f.Runs.Stamped(ctx, villain("joker"), kindPatrol); err != nil || !ok || !at.Equal(base().Add(time.Hour)) {
		t.Fatalf("stamp = %v ok=%v err=%v", at, ok, err)
	}
	if _, ok, err := f.Runs.Stamped(ctx, villain("penguin"), kindPatrol); err != nil || ok {
		t.Fatalf("stamp of an unstamped subject (found=%v, err=%v)", ok, err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Runs.Stamp(ctx, b, entity.Ref{}, kindPatrol, base()) }); err == nil {
		t.Error("stamp without a subject succeeded")
	}
}

// testNewID: NewID returns distinct, well-formed IDs.
// testTerminalNeedsFinish: a terminal record without its finish time is
// refused, by Insert and by Transition, so the done index never holds a run
// before every retention cutoff.
func testTerminalNeedsFinish(t *testing.T, f *fixture) {
	ctx := t.Context()
	done := rec("patrol-unfinished", kindPatrol, tasks.StateSucceeded)
	done.Finished = time.Time{}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.Runs.Insert(ctx, b, done) }); err == nil {
		t.Error("Insert of a terminal run without Finished: no error")
	}
	r := rec("patrol-running", kindPatrol, tasks.StateRunning)
	f.insert(t, r)
	if _, err := f.transition(ctx, r, tasks.StateFailed, func(n *tasks.RunRecord) { n.Finished = time.Time{} }); err == nil {
		t.Error("Transition to a terminal state without Finished: no error")
	}
	if got := f.get(t, r.ID); got.State != tasks.StateRunning {
		t.Errorf("run after the refused Transition is %s", got.State)
	}
}

func testNewID(t *testing.T, f *fixture) {
	seen := map[tasks.RunID]bool{}
	for range 200 {
		id, err := f.Runs.NewID(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 26 || seen[id] {
			t.Fatalf("NewID = %q (len %d, seen %v)", id, len(id), seen[id])
		}
		seen[id] = true
	}
}

// testImportKeeps: an imported run keeps its ID, kind, state and Seq; a run
// recorded running or stopping is stored interrupted.
func testImportKeeps(t *testing.T, f *fixture) {
	ctx := t.Context()
	done := rec("01HEADOPDONE", kindCase, tasks.StateSucceeded)
	done.Seq = tasks.RunSeqs{Event: 7, Intent: 2, Log: 31}
	waiting := rec("01HEADOPWAIT", kindCase, tasks.StateAwaitingDecision)
	running := rec("01HEADOPRUN", kindPatrol, tasks.StateRunning)
	stopping := rec("01HEADOPSTOP", kindPatrol, tasks.StateStopping)
	stopping.Reason = "shutdown"
	if err := f.once(ctx, func(b *storage.Batch) error {
		for _, r := range []tasks.RunRecord{done, waiting, running, stopping} {
			if err := f.imp.Run(ctx, b, r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	got := f.get(t, done.ID)
	if got.ID != done.ID || got.Kind != done.Kind || got.State != tasks.StateSucceeded || got.Seq != done.Seq || got.Current != done.Current {
		t.Fatalf("imported run = %+v", got)
	}
	if s := f.get(t, waiting.ID).State; s != tasks.StateAwaitingDecision {
		t.Fatalf("imported awaiting run is %s", s)
	}
	for _, r := range []tasks.RunRecord{running, stopping} {
		if s := f.get(t, r.ID).State; s != tasks.StateInterrupted {
			t.Fatalf("imported %s run is %s, want interrupted", r.State, s)
		}
	}
	if c := f.counts(t); c[tasks.StateInterrupted] != 2 || c[tasks.StateSucceeded] != 1 || c[tasks.StateAwaitingDecision] != 1 {
		t.Fatalf("Counts after the import = %v", c)
	}
	if u := collect(t, f.Runs.Unfinished(ctx)); len(u) != 3 {
		t.Fatalf("Unfinished after the import = %v", ids(u))
	}
}

// testImportTwice: a second import of one ID fails with ErrRunConflict.
func testImportTwice(t *testing.T, f *fixture) {
	ctx := t.Context()
	r := rec("01HEADOPTWICE", kindCase, tasks.StateFailed)
	if err := f.once(ctx, func(b *storage.Batch) error { return f.imp.Run(ctx, b, r) }); err != nil {
		t.Fatal(err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error { return f.imp.Run(ctx, b, r) }); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("second import: %v, want ErrRunConflict", err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error {
		if err := f.imp.Run(ctx, b, rec("01HEADOPDUP", kindCase, tasks.StateFailed)); err != nil {
			return err
		}
		return f.imp.Run(ctx, b, rec("01HEADOPDUP", kindCase, tasks.StateFailed))
	}); !errors.Is(err, tasks.ErrRunConflict) {
		t.Fatalf("one ID twice in one batch: %v, want ErrRunConflict", err)
	}
}

// testImportRunData: RunData lands only on a run imported in this batch or
// an earlier one, and every record reads back.
func testImportRunData(t *testing.T, f *fixture) {
	ctx := t.Context()
	r := rec("01HEADOPDATA", kindCase, tasks.StateInterrupted)
	data := tasks.RunData{
		Pages: []tasks.PageEntry{
			{Page: tasks.PageEvents, Seq: 1, Event: tasks.RunEvent{Event: "claim", At: base()}},
			{Page: tasks.PageIntents, Seq: 1, Write: tasks.Write{Name: "gotham.lock_up"}},
			{Page: tasks.PageItems, Item: tasks.ItemOutcome{Key: "joker", Outcome: "failed"}},
			{Page: tasks.PageLog, Seq: 4, Log: tasks.LogLine{Message: "Batmobile ready"}},
		},
		Checkpoints: map[string][]byte{"watermark": []byte("penguin"), "v1": []byte("legacy")},
		Chunks:      []tasks.Chunk{{N: 0, Complete: true}, {N: 1}},
		Rows:        []tasks.Row{{ID: "plan:joker"}, {ID: "plan:penguin"}},
		Marks:       []string{"plan:joker"},
	}
	if err := f.once(ctx, func(b *storage.Batch) error {
		return f.imp.RunData(ctx, b, "01HEADOPGHOST", data)
	}); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("RunData of a run never imported: %v, want ErrRunNotFound", err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error {
		if err := f.imp.Run(ctx, b, r); err != nil {
			return err
		}
		return f.imp.RunData(ctx, b, r.ID, tasks.RunData{Pages: data.Pages[:2], Checkpoints: data.Checkpoints})
	}); err != nil {
		t.Fatalf("Run and RunData in one batch: %v", err)
	}
	if err := f.once(ctx, func(b *storage.Batch) error {
		return f.imp.RunData(ctx, b, r.ID, tasks.RunData{Pages: data.Pages[2:], Chunks: data.Chunks, Rows: data.Rows, Marks: data.Marks})
	}); err != nil {
		t.Fatalf("RunData in a later batch: %v", err)
	}
	for page, n := range map[tasks.PageKind]int{tasks.PageEvents: 1, tasks.PageIntents: 1, tasks.PageItems: 1, tasks.PageLog: 1} {
		if got := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: page})); len(got) != n {
			t.Errorf("imported page %d = %+v", page, got)
		}
	}
	if got := collect(t, f.Runs.Pages(ctx, r.ID, tasks.PageQuery{Page: tasks.PageLog})); got[0].Seq != 4 {
		t.Errorf("imported log seq = %d, want 4", got[0].Seq)
	}
	if v, ok, err := f.Runs.Checkpoint(ctx, r.ID, "v1"); err != nil || !ok || string(v) != "legacy" {
		t.Errorf("imported checkpoint = %q %v %v", v, ok, err)
	}
	if n := len(collect(t, f.Runs.Chunks(ctx, r.ID))); n != 2 {
		t.Errorf("imported chunks = %d", n)
	}
	if n := len(collect(t, f.Runs.Rows(ctx, r.ID))); n != 2 {
		t.Errorf("imported rows = %d", n)
	}
	if m := collect(t, f.Runs.Marks(ctx, r.ID)); !slices.Equal(m, []string{"plan:joker"}) {
		t.Errorf("imported marks = %v", m)
	}
}

// importAll stages one import chunk: two runs with data, a clock, a stamp,
// a debounce bucket, the pause and a stand-down marker.
func importAll(ctx context.Context, imp tasks.Importer, b *storage.Batch, step func()) error {
	for _, r := range []tasks.RunRecord{rec("01HEADCRASH1", kindCase, tasks.StateSucceeded), rec("01HEADCRASH2", kindPatrol, tasks.StateRunning)} {
		if err := imp.Run(ctx, b, r); err != nil {
			return err
		}
		step()
		if err := imp.RunData(ctx, b, r.ID, tasks.RunData{
			Pages: []tasks.PageEntry{{Page: tasks.PageLog, Seq: 1, Log: tasks.LogLine{Message: "Bat-Signal lit"}}},
			Rows:  []tasks.Row{{ID: "plan:joker"}}, Marks: []string{"plan:joker"},
		}); err != nil {
			return err
		}
		step()
	}
	steps := []func() error{
		func() error { return imp.Clock(ctx, b, kindCase, tasks.Clock{LastRun: base(), ScheduleHash: "head"}) },
		func() error { return imp.Stamp(ctx, b, villain("joker"), kindCase, base()) },
		func() error {
			return imp.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindCase, Subjects: []entity.Ref{villain("joker")}, First: base(), Last: base()})
		},
		func() error { return imp.Pause(ctx, b, tasks.Pause{Reason: "cutover", At: base()}) },
		func() error {
			return imp.StandDown(ctx, b, tasks.StandDown{Key: "gotham.blackout", Holder: "01HEADCRASH2"})
		},
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
		step()
	}
	return nil
}

// imported counts how many of importAll's records a Stores holds.
func imported(t *testing.T, s tasks.Stores) (have, total int) {
	t.Helper()
	ctx := t.Context()
	f := fixtureOf(t, s)
	check := func(ok bool) {
		total++
		if ok {
			have++
		}
	}
	for _, id := range []tasks.RunID{"01HEADCRASH1", "01HEADCRASH2"} {
		_, err := s.Runs.Get(ctx, id)
		check(err == nil)
		check(len(collect(t, s.Runs.Pages(ctx, id, tasks.PageQuery{Page: tasks.PageLog}))) == 1)
		check(len(collect(t, s.Runs.Rows(ctx, id))) == 1)
		check(len(collect(t, f.Runs.Marks(ctx, id))) == 1)
	}
	c, ok, err := s.Clocks.Get(ctx, kindCase)
	check(err == nil && ok && c.ScheduleHash == "")
	_, ok, err = s.Runs.Stamped(ctx, villain("joker"), kindCase)
	check(err == nil && ok)
	check(len(collect(t, s.Runs.DebounceBuckets(ctx))) == 1)
	_, ok, err = s.Pause.Get(ctx)
	check(err == nil && ok)
	check(len(collect(t, s.StandDown.List(ctx))) == 1)
	cn, err := s.Runs.Counts(ctx)
	check(err == nil && cn[tasks.StateInterrupted] == 1 && cn[tasks.StateSucceeded] == 1)
	return have, total
}

// testImportCrash: a crash at every point of an import batch leaves the
// chunk wholly imported or wholly not.
func testImportCrash(t *testing.T, f *fixture) {
	cr, ok := f.Runs.(Crasher)
	if !ok {
		t.Skip("Stores.Runs does not implement taskstest.Crasher; crash case skipped")
	}
	ctx := t.Context()
	b := f.open()
	defer closeBatch(t, b)
	points := 0
	none := func() {
		points++
		if have, total := imported(t, cr.CrashClone(t)); have != 0 {
			t.Fatalf("crash at point %d of the import batch: %d of %d records present, want none", points, have, total)
		}
	}
	none()
	if err := importAll(ctx, f.imp, b, none); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if have, total := imported(t, cr.CrashClone(t)); have != total {
		t.Fatalf("crash after the import commit: %d of %d records present, want all", have, total)
	}
	if points < 10 {
		t.Fatalf("only %d crash points were taken", points)
	}
}

// testRenameKind: RenameKind rewrites runs and their kind index, the clock,
// debounce buckets and stamps, chunk by chunk through its cursor, and a
// second pass changes nothing.
func testRenameKind(t *testing.T, f *fixture) {
	ctx := t.Context()
	const to tasks.Kind = "gotham.renamed"
	var runs []tasks.RunRecord
	for i := range 7 {
		runs = append(runs, rec(fmt.Sprintf("patrol-rename-%d", i), kindPatrol, []tasks.RunState{tasks.StateQueued, tasks.StateSucceeded}[i%2]))
	}
	other := rec("case-untouched", kindCase, tasks.StateQueued)
	f.insert(t, append(runs, other)...)
	if err := f.once(ctx, func(b *storage.Batch) error {
		if err := f.Clocks.Advance(ctx, b, kindPatrol, tasks.Clock{}, false, tasks.Clock{LastRun: base(), ScheduleHash: "p"}); err != nil {
			return err
		}
		for _, v := range []string{"joker", "penguin"} {
			if err := f.Runs.Stamp(ctx, b, villain(v), kindPatrol, base()); err != nil {
				return err
			}
		}
		if err := f.Runs.Stamp(ctx, b, villain("joker"), kindCase, base()); err != nil {
			return err
		}
		return f.Runs.Debounce(ctx, b, tasks.DebounceBucket{Kind: kindPatrol, Bucket: "east-end", Subjects: []entity.Ref{villain("joker")}, First: base(), Last: base()})
	}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		from, to tasks.Kind
		limit    int
		cursor   string
	}{{kindPatrol, kindPatrol, 1, ""}, {"", to, 1, ""}, {kindPatrol, to, 0, ""}, {kindPatrol, to, 1, "garbage"}} {
		if err := f.once(ctx, func(b *storage.Batch) error {
			_, err := f.imp.RenameKind(ctx, b, bad.from, bad.to, bad.cursor, bad.limit)
			return err
		}); err == nil {
			t.Errorf("RenameKind %+v succeeded", bad)
		}
	}
	// Chunk by chunk, one batch per chunk, as a data migration does. The
	// first chunk is staged twice into one batch through its cursor.
	cursor, chunks := "", 0
	for {
		var next string
		if err := f.once(ctx, func(b *storage.Batch) error {
			var err error
			next, err = f.imp.RenameKind(ctx, b, kindPatrol, to, cursor, 3)
			if err == nil && chunks == 0 && next != "" {
				next, err = f.imp.RenameKind(ctx, b, kindPatrol, to, next, 3)
			}
			return err
		}); err != nil {
			t.Fatalf("RenameKind chunk %d: %v", chunks, err)
		}
		chunks++
		if cursor = next; cursor == "" {
			break
		}
		if chunks > 20 {
			t.Fatal("RenameKind does not finish")
		}
	}
	if chunks < 2 {
		t.Fatalf("RenameKind finished in %d chunk(s) at limit 3", chunks)
	}
	for _, r := range runs {
		if got := f.get(t, r.ID); got.Kind != to || got.State != r.State {
			t.Fatalf("renamed run %s = kind %s state %s", r.ID, got.Kind, got.State)
		}
	}
	if got := f.get(t, other.ID); got.Kind != kindCase {
		t.Fatalf("a run of another kind was renamed: %s", got.Kind)
	}
	if got := ids(collect(t, f.Runs.List(ctx, tasks.RunQuery{Kinds: []tasks.Kind{to}, Limit: 100}))); len(got) != len(runs) {
		t.Fatalf("kind index of the new kind = %v", got)
	}
	if got := collect(t, f.Runs.List(ctx, tasks.RunQuery{Kinds: []tasks.Kind{kindPatrol}})); len(got) != 0 {
		t.Fatalf("kind index of the old kind = %v", ids(got))
	}
	if _, ok, err := f.Clocks.Get(ctx, kindPatrol); err != nil || ok {
		t.Fatalf("the old kind's clock remains (found=%v, err=%v)", ok, err)
	}
	if c, ok, err := f.Clocks.Get(ctx, to); err != nil || !ok || c.ScheduleHash != "p" {
		t.Fatalf("the new kind's clock = %+v %v %v", c, ok, err)
	}
	for _, v := range []string{"joker", "penguin"} {
		if _, ok, err := f.Runs.Stamped(ctx, villain(v), kindPatrol); err != nil || ok {
			t.Fatalf("old stamp of %s remains (found=%v, err=%v)", v, ok, err)
		}
		if _, ok, err := f.Runs.Stamped(ctx, villain(v), to); err != nil || !ok {
			t.Fatalf("new stamp of %s missing (found=%v, err=%v)", v, ok, err)
		}
	}
	if _, ok, err := f.Runs.Stamped(ctx, villain("joker"), kindCase); err != nil || !ok {
		t.Fatalf("another kind's stamp was moved (found=%v, err=%v)", ok, err)
	}
	if b := collect(t, f.Runs.DebounceBuckets(ctx)); len(b) != 1 || b[0].Kind != to || b[0].Bucket != "east-end" {
		t.Fatalf("buckets after the rename = %+v", b)
	}
	before := f.snapshot(t, ids(append(runs, other)))
	if err := f.once(ctx, func(b *storage.Batch) error {
		next, err := f.imp.RenameKind(ctx, b, kindPatrol, to, "", 100)
		if err == nil && next != "" {
			err = fmt.Errorf("second pass returned cursor %q", next)
		}
		return err
	}); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if after := f.snapshot(t, ids(append(runs, other))); after != before {
		t.Fatalf("a second RenameKind pass changed the stores:\n%s\n%s", before, after)
	}
}
