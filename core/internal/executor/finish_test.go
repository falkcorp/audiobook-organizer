// file: core/internal/executor/finish_test.go
// version: 0.1.0
// guid: c41f8d27-6a0b-4e93-b5d8-0f3a7e92c614
// last-edited: 2026-11-22

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/tasks"
)

// finishOnlyRig is the gotham.archive Batch kind (ten case files, chunks of
// three) as a ResumeAsk kind, with counters for what a boot did: how many
// times its Source was read and its Item ran, and the Summary its Finish
// received. block is the item that blocks until the run ends (a first boot
// the test then shuts down); finishBlocks makes Finish itself block until
// the run ends (a boot shut down in the middle of the finish).
type finishOnlyRig struct {
	sources, items, finishes atomic.Int64
	block                    string
	reach                    chan struct{}
	finishBlocks             bool
	finishStarted            chan struct{}
	finishErr                error
	summary                  chan tasks.Summary
}

func (r *finishOnlyRig) def() tasks.Definition {
	s := tasks.BatchSpec[noParams, string, tasks.Summary]{
		Common: common("gotham.archive", withResume(tasks.ResumeAsk)),
		Source: func(*tasks.RC, noParams) (tasks.Source[string], error) {
			r.sources.Add(1)
			return tasks.Source[string]{Items: caseFiles, Key: func(s string) string { return s }}, nil
		},
		Item: func(ic *tasks.ItemRC, _ noParams, f string) error {
			r.items.Add(1)
			if f == r.block {
				close(r.reach)
				<-ic.Context().Done()
				return ic.Context().Err()
			}
			return nil
		},
		Finish: func(rc *tasks.RC, _ noParams, s tasks.Summary) (tasks.Summary, error) {
			r.finishes.Add(1)
			if r.summary != nil {
				r.summary <- s
			}
			if r.finishBlocks {
				close(r.finishStarted)
				<-rc.Context().Done()
				return tasks.Summary{}, context.Cause(rc.Context())
			}
			return s, r.finishErr
		},
		Parallelism: tasks.Sequential("one file at a time"),
		ChunkSize:   3,
	}
	return tasks.Batch(tasks.Kind("gotham.archive"), s)
}

// interruptedArchive runs gotham.archive until file-07 blocks (chunks 0 and 1,
// six files, are in the ledger), shuts the executor down, and reboots over
// the same store: the run is awaiting_decision. It returns the second boot.
func interruptedArchive(t *testing.T, edit func(*Options), next *finishOnlyRig) (*harness, tasks.RunID) {
	t.Helper()
	first := &finishOnlyRig{block: "file-07", reach: make(chan struct{})}
	h := newHarness(t, []tasks.Definition{first.def()}, edit)
	id := h.start("gotham.archive", tasks.StartRequest{})
	<-first.reach
	if err := h.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h = h.reboot([]tasks.Definition{next.def()}, edit)
	h.waitState(id, tasks.StateAwaitingDecision)
	return h, id
}

func chunkNumbers(t *testing.T, h *harness, id tasks.RunID) []int {
	t.Helper()
	var out []int
	for c, err := range h.stores.Runs.Chunks(context.Background(), id) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c.N)
	}
	return out
}

// TestFinishOnly_BatchFinishRunsOverTheStoredLedger (F5-18 F1; HEAD
// registry/finish.go:73, batch_runner.go:282-296): Decide(finish) on a Batch
// run awaiting a decision re-queues it finish-only. The next attempt reads
// no source, runs no item and writes no chunk, runs the definition's Finish
// over the chunks the first boot completed (two chunks, six files, the stored
// total of ten), and ends the run succeeded with the marker cleared.
func TestFinishOnly_BatchFinishRunsOverTheStoredLedger(t *testing.T) {
	second := &finishOnlyRig{summary: make(chan tasks.Summary, 2)}
	h, id := interruptedArchive(t, nil, second)
	before := chunkNumbers(t, h, id)
	if !slices.Equal(before, []int{0, 1}) {
		t.Fatalf("ledger after the first boot: chunks %v, want [0 1]", before)
	}
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon); err != nil {
		t.Fatalf("decide finish: %v", err)
	}
	r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded {
		t.Fatalf("finish-only run: %s (%q)", r.State, r.Error)
	}
	s := <-second.summary
	if s.Done != 6 || s.Failed != 0 || s.ChunksDone != 2 || s.ChunksTotal != 2 || s.Total != 10 {
		t.Fatalf("Finish saw %+v, want 6 done, 2 chunks (of 2), total 10", s)
	}
	if n := second.sources.Load() + second.items.Load(); n != 0 {
		t.Fatalf("the finish-only attempt read the source or ran an item %d times, want none", n)
	}
	if n := second.finishes.Load(); n != 1 {
		t.Fatalf("Finish ran %d times, want once", n)
	}
	if after := chunkNumbers(t, h, id); !slices.Equal(after, before) {
		t.Fatalf("ledger after the finish: chunks %v, want it untouched (%v): the attempt leased work", after, before)
	}
	var stored tasks.Summary
	if err := json.Unmarshal(r.Result, &stored); err != nil || stored.Done != 6 {
		t.Fatalf("stored result %s (%v), want Finish's summary", r.Result, err)
	}
	if r.FinishOnly {
		t.Error("a run that ended still carries the finish-only marker")
	}
	if r.Attempt != 2 {
		t.Errorf("attempt %d, want 2 (the interrupted first boot's, then the finish-only attempt's)", r.Attempt)
	}
	var decide *tasks.RunEvent
	for p, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}) {
		if err != nil {
			t.Fatal(err)
		}
		if p.Event.Event == "decide" {
			decide = &p.Event
		}
	}
	if decide == nil || decide.Reason != "finish" || decide.To != tasks.StateQueued || decide.Actor == nil || *decide.Actor != gordon {
		t.Fatalf("decide event %+v, want reason finish, to queued, by gordon", decide)
	}
}

// TestFinishOnly_FinishErrorEndsTheRunFailed: a Finish that returns an error
// ends the finish-only run failed, with the error on the record and the
// marker cleared.
func TestFinishOnly_FinishErrorEndsTheRunFailed(t *testing.T) {
	second := &finishOnlyRig{finishErr: errors.New("the evidence locker is flooded")}
	h, id := interruptedArchive(t, nil, second)
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon); err != nil {
		t.Fatal(err)
	}
	r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateFailed || !strings.Contains(r.Error, "the evidence locker is flooded") {
		t.Fatalf("run %s (%q), want failed with Finish's error", r.State, r.Error)
	}
	if r.FinishOnly {
		t.Error("a failed run still carries the finish-only marker")
	}
	if n := second.items.Load(); n != 0 {
		t.Errorf("a failed finish ran %d items", n)
	}
}

// TestFinishOnly_RestartMidFinishStaysFinishOnly: a restart while the
// finish-only attempt is running resumes the run as finish-only, never as a
// full run. Boot 2 is shut down inside Finish; boot 3's resume sweep
// re-queues the run, which finishes over the same ledger without reading the
// source or running an item.
func TestFinishOnly_RestartMidFinishStaysFinishOnly(t *testing.T) {
	t.Run("a kind that asks", func(t *testing.T) {
		second := &finishOnlyRig{finishBlocks: true, finishStarted: make(chan struct{})}
		h, id := interruptedArchive(t, nil, second)
		if err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon); err != nil {
			t.Fatal(err)
		}
		<-second.finishStarted
		if err := h.x.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := h.waitState(id, tasks.StateInterrupted)
		if !r.FinishOnly {
			t.Fatal("the attempt stopped by the shutdown lost the finish-only marker")
		}
		third := &finishOnlyRig{summary: make(chan tasks.Summary, 2)}
		h = h.reboot([]tasks.Definition{third.def()}, nil)
		r = h.waitState(id, tasks.StateSucceeded, tasks.StateFailed, tasks.StateAwaitingDecision)
		if r.State != tasks.StateSucceeded {
			t.Fatalf("after the restart the run is %s (%q), want it finished", r.State, r.Error)
		}
		if s := <-third.summary; s.Done != 6 || s.ChunksDone != 2 {
			t.Fatalf("the resumed finish saw %+v, want the first boot's 6 files in 2 chunks", s)
		}
		if n := third.sources.Load() + third.items.Load(); n != 0 {
			t.Fatalf("the resumed finish-only run read the source or ran an item %d times, want none", n)
		}
		if r.FinishOnly {
			t.Error("a run that ended still carries the finish-only marker")
		}
	})
	t.Run("a from-zero kind is not restarted from zero", func(t *testing.T) {
		// Only ResumeAsk reaches awaiting_decision, so this puts the marker on
		// a running ResumeFromZero run by hand: whatever the policy, a
		// finish-only run's restart is finish-only, and a from-zero requeue
		// would void the ledger Finish reads.
		first := &finishOnlyRig{block: "file-07", reach: make(chan struct{})}
		fromZero := func(r *finishOnlyRig) tasks.Definition {
			s := tasks.BatchSpec[noParams, string, tasks.Summary]{
				Common: common("gotham.archive", withResume(tasks.ResumeFromZero)),
				Source: func(*tasks.RC, noParams) (tasks.Source[string], error) {
					r.sources.Add(1)
					return tasks.Source[string]{Items: caseFiles, Key: func(s string) string { return s }}, nil
				},
				Item: func(ic *tasks.ItemRC, _ noParams, f string) error {
					r.items.Add(1)
					if f == r.block {
						close(r.reach)
						<-ic.Context().Done()
						return ic.Context().Err()
					}
					return nil
				},
				Finish: func(_ *tasks.RC, _ noParams, s tasks.Summary) (tasks.Summary, error) {
					r.finishes.Add(1)
					if r.summary != nil {
						r.summary <- s
					}
					return s, nil
				},
				Parallelism: tasks.Sequential("one file at a time"),
				ChunkSize:   3,
			}
			return tasks.Batch(tasks.Kind("gotham.archive"), s)
		}
		h := newHarness(t, []tasks.Definition{fromZero(first)}, nil)
		id := h.start("gotham.archive", tasks.StartRequest{})
		<-first.reach
		if _, err := h.e.move(context.Background(), id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			next := cur
			next.FinishOnly = true
			return next, tasks.RunEvent{Event: "test"}, nil
		}, nil); err != nil {
			t.Fatal(err)
		}
		second := &finishOnlyRig{summary: make(chan tasks.Summary, 2)}
		next := h.crashBoot([]tasks.Definition{fromZero(second)}, nil)
		r := next.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
		if r.State != tasks.StateSucceeded {
			t.Fatalf("restarted finish-only run: %s (%q)", r.State, r.Error)
		}
		if s := <-second.summary; s.Done != 6 || s.ChunksDone != 2 {
			t.Fatalf("Finish saw %+v, want the earlier attempt's 6 files in 2 chunks (not voided by a from-zero requeue)", s)
		}
		if n := second.sources.Load() + second.items.Load(); n != 0 {
			t.Fatalf("the restarted finish-only run read the source or ran an item %d times, want none", n)
		}
	})
}

// TestFinishOnly_ResumeClearsTheMarker: Decide(resume) is a full attempt,
// so it clears a finish-only marker left on the run (HEAD: every entry into
// queued but decide_finish and wait_cleared does,
// pebble_store_ops_v3.go:1080-1085).
func TestFinishOnly_ResumeClearsTheMarker(t *testing.T) {
	second := &finishOnlyRig{}
	h, id := interruptedArchive(t, nil, second)
	if _, err := h.e.move(context.Background(), id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		next := cur
		next.FinishOnly = true
		return next, tasks.RunEvent{Event: "test"}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideResume, gordon); err != nil {
		t.Fatal(err)
	}
	r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded {
		t.Fatalf("resumed run: %s (%q)", r.State, r.Error)
	}
	if second.sources.Load() == 0 || second.items.Load() == 0 {
		t.Fatalf("a resumed run read the source %d times and ran %d items: it ran as finish-only", second.sources.Load(), second.items.Load())
	}
}

// TestFinishOnly_RefusesAKindWithoutALedger: a plain task has no ledger and
// no Finish, and HEAD's CanFinishWithoutResuming refuses it (a "finish" would
// be a full run, Retry under another name): Decide(finish) is refused with
// ErrNotFinishable, before any write, and the run stays awaiting_decision.
func TestFinishOnly_RefusesAKindWithoutALedger(t *testing.T) {
	started := make(chan tasks.RunID, 1)
	defs := []tasks.Definition{taskDef("gotham.stakeout", blockUntilCtx(started), withResume(tasks.ResumeAsk))}
	h := newHarness(t, defs, nil)
	id := h.start("gotham.stakeout", tasks.StartRequest{})
	<-started
	if err := h.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h = h.reboot(defs, nil)
	before := h.waitState(id, tasks.StateAwaitingDecision)
	err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon)
	if !errors.Is(err, ErrNotFinishable) {
		t.Fatalf("decide finish on a task: %v, want ErrNotFinishable", err)
	}
	after := h.get(id)
	if after.State != tasks.StateAwaitingDecision || after.FinishOnly || after.Seq != before.Seq {
		t.Fatalf("a refused finish changed the run: %+v -> %+v", before, after)
	}
	// The other two decisions are untouched.
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideDrop, gordon); err != nil {
		t.Fatalf("decide drop: %v", err)
	}
	h.waitState(id, tasks.StateInterruptedDropped)
}

// TestFinishOnly_KeepsTheDecidePermissionGate: Decide(finish) asks for the
// kind's Perms.Approve else Perms.Start, as Decide(resume) and Decide(drop)
// do (the gate is not this slice's to change): a caller without the scope is
// refused and the run stays awaiting; one with it queues the finish.
func TestFinishOnly_KeepsTheDecidePermissionGate(t *testing.T) {
	second := &finishOnlyRig{summary: make(chan tasks.Summary, 2)}
	edit := func(o *Options) {
		o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
		withPrincipal(o)
	}
	h, id := interruptedArchive(t, edit, second)
	if err := h.x.Decide(asUser("joker", "arkham:run"), id, tasks.DecideFinish); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("decide finish without the scope: %v, want ErrNotPermitted", err)
	}
	if r := h.get(id); r.State != tasks.StateAwaitingDecision || r.FinishOnly {
		t.Fatalf("a refused decision left the run %s (finish-only %v)", r.State, r.FinishOnly)
	}
	if err := h.x.Decide(asUser("gordon", "gotham:run"), id, tasks.DecideFinish); err != nil {
		t.Fatalf("decide finish with the scope: %v", err)
	}
	if r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed); r.State != tasks.StateSucceeded {
		t.Fatalf("finish: %s (%q)", r.State, r.Error)
	}
}

// foldRigFinishOnly boots an executor that is never Run (so nothing is
// claimed) over a Dedupe-folding Batch kind and returns a run of it in state
// with the finish-only marker set.
func foldRigFinishOnly(t *testing.T, state tasks.RunState) (*harness, tasks.RunID) {
	t.Helper()
	s := tasks.BatchSpec[noParams, string, tasks.Summary]{
		Common: common("gotham.archive", withResume(tasks.ResumeAsk), func(c *tasks.Common) {
			c.Dedupe = tasks.DedupePolicy{AnyParams: true}
		}),
		Source:      func(*tasks.RC, noParams) (tasks.Source[string], error) { return tasks.Source[string]{}, nil },
		Item:        func(*tasks.ItemRC, noParams, string) error { return nil },
		Parallelism: tasks.Sequential("one file at a time"),
	}
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	h := onStores(t, fs, stores, ks, []tasks.Definition{tasks.Batch(tasks.Kind("gotham.archive"), s)}, nil, false)
	id := h.start("gotham.archive", tasks.StartRequest{})
	if _, err := h.e.move(context.Background(), id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		next := cur
		next.State, next.FinishOnly = state, true
		if state != tasks.StateQueued {
			next.Started = h.e.now()
		}
		return next, tasks.RunEvent{Event: "test"}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	return h, id
}

// TestFinishOnly_IsNotAFoldTarget (HEAD registry/finish.go:63-64: no new
// enqueue is folded into the re-queued row): a start of the same kind, mode
// and params must not fold into a finish-only run, queued or running, even
// when the kind folds into any queued or running run. It would be handed
// back as Existing and run nothing.
func TestFinishOnly_IsNotAFoldTarget(t *testing.T) {
	for _, state := range []tasks.RunState{tasks.StateQueued, tasks.StateRunning} {
		t.Run(string(state), func(t *testing.T) {
			h, id := foldRigFinishOnly(t, state)
			ref, err := h.x.Start(context.Background(), "gotham.archive", tasks.StartRequest{Mode: tasks.ModeRequestLive})
			if err != nil {
				t.Fatal(err)
			}
			if ref.Existing || ref.ID == id {
				t.Fatalf("a start folded into the %s finish-only run %s: %+v", state, id, ref)
			}
		})
	}
	t.Run("a plain queued run still folds", func(t *testing.T) {
		h, id := foldRigFinishOnly(t, tasks.StateQueued)
		if _, err := h.e.move(context.Background(), id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			next := cur
			next.FinishOnly = false
			return next, tasks.RunEvent{Event: "test"}, nil
		}, nil); err != nil {
			t.Fatal(err)
		}
		ref, err := h.x.Start(context.Background(), "gotham.archive", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil || !ref.Existing || ref.ID != id {
			t.Fatalf("start beside a plain queued run: %+v, %v, want it folded into %s", ref, err, id)
		}
	})
}

// --- Fixer ---

// finishFixer is the arkham.relocate Fixer as a ResumeAsk kind whose Apply
// blocks on blockRow until the run ends and whose AfterApply records what it
// was given.
type finishFixer struct {
	log        applyLog
	blockRow   string
	reach      chan struct{}
	blockPlan  bool // Evaluate blocks until the run ends
	planReach  chan struct{}
	afterCalls atomic.Int64
	afterRows  chan []string
	afterErr   error
}

func (f *finishFixer) def() tasks.Definition {
	return fixerDef(&f.log, func(s *tasks.FixerSpec[noParams, string]) {
		s.Common.Resume = tasks.ResumeAsk
		inner := s.Evaluate
		s.Evaluate = func(ic *tasks.ItemRC, p noParams, c string) (tasks.Row, error) {
			if f.blockPlan {
				close(f.planReach)
				<-ic.Context().Done()
				return tasks.Row{}, ic.Context().Err()
			}
			return inner(ic, p, c)
		}
		apply := s.Apply
		s.Apply = func(ic *tasks.ItemRC, w tasks.Writer, p noParams, row tasks.Row) error {
			if row.ID == f.blockRow {
				close(f.reach)
				<-ic.Context().Done()
				return ic.Context().Err()
			}
			return apply(ic, w, p, row)
		}
		s.AfterApply = func(_ *tasks.RC, applied []string) error {
			f.afterCalls.Add(1)
			if f.afterRows != nil {
				f.afterRows <- slices.Clone(applied)
			}
			return f.afterErr
		}
	})
}

// interruptedApply plans arkham.relocate, approves every inmate, lets the
// apply run reach blockRow (the rows before it, in ID order, are applied and
// marked), shuts the executor down and reboots over the same store with next:
// the apply run is awaiting_decision.
func interruptedApply(t *testing.T, blockRow string, next *finishFixer) (*harness, tasks.RunID) {
	t.Helper()
	first := &finishFixer{blockRow: blockRow, reach: make(chan struct{})}
	h := newHarness(t, []tasks.Definition{first.def()}, withPrincipal)
	plan := h.plan()
	if err := h.x.Approve(asUser("gordon", scopeApprov, scopeRun), plan, tasks.ApproveRequest{Rows: inmates}); err != nil {
		t.Fatal(err)
	}
	<-first.reach
	apply := h.onlyApplyRun(plan)
	if err := h.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h = h.reboot([]tasks.Definition{next.def()}, withPrincipal)
	h.waitState(apply.ID, tasks.StateAwaitingDecision)
	return h, apply.ID
}

// TestFinishOnly_FixerAfterApplySeesTheAppliedRows (HEAD
// pkg/ops/fixer.go:246-256; registry/finish.go:48-57): finish on an interrupted
// apply run runs AfterApply once over the rows the first boot applied and
// marked (joker; penguin was running), applies nothing more, and ends the run
// succeeded with the applied count.
func TestFinishOnly_FixerAfterApplySeesTheAppliedRows(t *testing.T) {
	second := &finishFixer{afterRows: make(chan []string, 2)}
	h, id := interruptedApply(t, "penguin", second)
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon); err != nil {
		t.Fatal(err)
	}
	r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded {
		t.Fatalf("finish-only apply run: %s (%q)", r.State, r.Error)
	}
	if got := <-second.afterRows; !slices.Equal(got, []string{"joker"}) {
		t.Fatalf("AfterApply saw %v, want [joker]", got)
	}
	if n := second.afterCalls.Load(); n != 1 {
		t.Fatalf("AfterApply ran %d times, want once", n)
	}
	if got := second.log.got(); len(got) != 0 {
		t.Fatalf("the finish-only attempt applied %v, want no row applied", got)
	}
	if res := result(t, r); res.Applied != 1 {
		t.Fatalf("result %+v, want 1 applied", res)
	}
	if r.FinishOnly {
		t.Error("a run that ended still carries the finish-only marker")
	}
}

// TestFinishOnly_FixerWithNothingAppliedSkipsAfterApply (HEAD
// pkg/ops/fixer.go:252: AfterApply runs only when a row was applied): the
// first boot blocked on the first row, so finish ends the run succeeded
// without calling AfterApply.
func TestFinishOnly_FixerWithNothingAppliedSkipsAfterApply(t *testing.T) {
	second := &finishFixer{}
	h, id := interruptedApply(t, "joker", second)
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon); err != nil {
		t.Fatal(err)
	}
	if r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed); r.State != tasks.StateSucceeded {
		t.Fatalf("finish-only apply run: %s (%q)", r.State, r.Error)
	}
	if n := second.afterCalls.Load(); n != 0 {
		t.Fatalf("AfterApply ran %d times over no applied row, want none", n)
	}
}

// TestFinishOnly_FixerAfterApplyErrorEndsTheRunFailed: an AfterApply error
// ends the finish-only run failed.
func TestFinishOnly_FixerAfterApplyErrorEndsTheRunFailed(t *testing.T) {
	second := &finishFixer{afterErr: errors.New("the cell door will not close")}
	h, id := interruptedApply(t, "penguin", second)
	if err := h.e.DecideAs(context.Background(), id, tasks.DecideFinish, gordon); err != nil {
		t.Fatal(err)
	}
	r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateFailed || !strings.Contains(r.Error, "the cell door will not close") {
		t.Fatalf("run %s (%q), want failed with AfterApply's error", r.State, r.Error)
	}
}

// TestFinishOnly_FixerPlanRunFinishesWithItsStoredRows: a plan (Preview) run
// applied nothing and core's plan has no AfterApply, so finish ends it
// succeeded with the plan rows that are stored, calling neither Evaluate nor
// AfterApply.
func TestFinishOnly_FixerPlanRunFinishesWithItsStoredRows(t *testing.T) {
	first := &finishFixer{blockPlan: true, planReach: make(chan struct{})}
	h := newHarness(t, []tasks.Definition{first.def()}, withPrincipal)
	ref, err := h.x.Start(planner(), relocate, tasks.StartRequest{Mode: tasks.ModeRequestPreview})
	if err != nil {
		t.Fatal(err)
	}
	<-first.planReach
	if err := h.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := &finishFixer{}
	h = h.reboot([]tasks.Definition{second.def()}, withPrincipal)
	h.waitState(ref.ID, tasks.StateAwaitingDecision)
	// The interrupted plan wrote no rows; store two, as a plan that got that
	// far would have, so the result shows the rows read back.
	if err := h.stores.Runs.ReplaceRows(context.Background(), ref.ID, planPrefix, 2, func(i int) (tasks.Row, error) {
		return tasks.Row{ID: planPrefix + inmates[i]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.e.DecideAs(context.Background(), ref.ID, tasks.DecideFinish, gordon); err != nil {
		t.Fatal(err)
	}
	r := h.waitState(ref.ID, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded {
		t.Fatalf("finish-only plan run: %s (%q)", r.State, r.Error)
	}
	if res := result(t, r); res.Rows != 2 || res.Applied != 0 {
		t.Fatalf("result %+v, want the 2 stored plan rows and nothing applied", res)
	}
	if n := second.afterCalls.Load(); n != 0 {
		t.Fatalf("AfterApply ran %d times on a plan run, want none", n)
	}
}

// TestFinishOnly_MarkedTaskRunFails: a run marked finish-only whose kind is a
// plain task (Decide refuses to make one; an import or a hand-edit could)
// fails with ErrNotFinishable rather than running the task, as HEAD's
// adapter does (registry/v3_adapter.go:261-263).
func TestFinishOnly_MarkedTaskRunFails(t *testing.T) {
	ran := make(chan struct{}, 1)
	defs := []tasks.Definition{taskDef("gotham.stakeout", func(*tasks.RC, noParams) (string, error) {
		ran <- struct{}{}
		return "", nil
	})}
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	h := onStores(t, fs, stores, ks, defs, nil, false)
	id := h.start("gotham.stakeout", tasks.StartRequest{})
	if _, err := h.e.move(context.Background(), id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		next := cur
		next.FinishOnly = true
		return next, tasks.RunEvent{Event: "test"}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.x.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateFailed || !strings.Contains(r.Error, ErrNotFinishable.Error()) {
		t.Fatalf("run %s (%q), want failed with ErrNotFinishable", r.State, r.Error)
	}
	select {
	case <-ran:
		t.Fatal("the task body ran on a finish-only record")
	default:
	}
}
