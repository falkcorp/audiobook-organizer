// file: core/internal/executor/batch_test.go
// version: 1.1.1
// guid: ff587c0b-7c81-4a29-8286-03cb21859652
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// archiveRun is one Batch run over ten case files; Item records what it ran.
type archiveRun struct {
	mu     sync.Mutex
	ran    []string
	block  string        // the item that blocks until the run ends
	fail   string        // the item that fails
	reach  chan struct{} // closed when the blocking item starts
	finish chan tasks.Summary
}

func (r *archiveRun) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.ran)
	slices.Sort(out)
	return out
}

var caseFiles = func() []string {
	out := make([]string, 10)
	for i := range out {
		out[i] = fmt.Sprintf("file-%02d", i)
	}
	return out
}()

func archiveDef(r *archiveRun, edit func(*tasks.BatchSpec[noParams, string, tasks.Summary])) tasks.Definition {
	s := tasks.BatchSpec[noParams, string, tasks.Summary]{
		Common: common("gotham.archive"),
		Source: func(*tasks.RC, noParams) (tasks.Source[string], error) {
			return tasks.Source[string]{Items: caseFiles, Key: func(s string) string { return s }}, nil
		},
		Item: func(ic *tasks.ItemRC, _ noParams, f string) error {
			if f == r.block {
				close(r.reach)
				<-ic.Context().Done()
				return ic.Context().Err()
			}
			r.mu.Lock()
			r.ran = append(r.ran, f)
			r.mu.Unlock()
			if f == r.fail {
				return errors.New("the file is water damaged")
			}
			return nil
		},
		Finish: func(_ *tasks.RC, _ noParams, s tasks.Summary) (tasks.Summary, error) {
			if r.finish != nil {
				r.finish <- s
			}
			return s, nil
		},
		Parallelism: tasks.Sequential("one file at a time"),
		ChunkSize:   3,
	}
	if edit != nil {
		edit(&s)
	}
	return tasks.Batch(tasks.Kind("gotham.archive"), s)
}

// TestBatch_ChunkLedgerResume (5T.3.1, 5T.3.6; added for the chunk ledger):
// a resumed Batch skips the chunks its ledger records complete, redoes the
// incomplete one, and its summary counts both attempts; an item failure
// under BatchContinue is counted and the run still succeeds.
func TestBatch_ChunkLedgerResume(t *testing.T) {
	first := &archiveRun{block: "file-07", reach: make(chan struct{})}
	h := newHarness(t, []tasks.Definition{archiveDef(first, nil)}, nil)
	id := h.start("gotham.archive", tasks.StartRequest{})
	<-first.reach
	if got := first.got(); !slices.Equal(got, caseFiles[:7]) {
		t.Fatalf("first attempt ran %v, want files 00..06", got)
	}
	second := &archiveRun{fail: "file-09", finish: make(chan tasks.Summary, 1)}
	next := h.crashBoot([]tasks.Definition{archiveDef(second, nil)}, nil)
	r := next.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded {
		t.Fatalf("resumed batch: %s (%q)", r.State, r.Error)
	}
	if got := second.got(); !slices.Equal(got, caseFiles[6:]) {
		t.Fatalf("resumed attempt ran %v, want files 06..09 (chunks 0 and 1 were complete)", got)
	}
	s := <-second.finish
	if s.Done != 9 || s.Failed != 1 || s.ChunksDone != 4 {
		t.Fatalf("summary %+v, want 9 done, 1 failed, 4 chunks", s)
	}
	var stored tasks.Summary
	if err := json.Unmarshal(r.Result, &stored); err != nil || stored.Failed != 1 {
		t.Fatalf("stored result %s (%v)", r.Result, err)
	}
}

// TestBatch_StopPolicyAndPartitions (5T.3.1): BatchStop fails the run at
// the first failed item without dispatching the rest; PartitionBy runs the
// items of one key in order.
func TestBatch_StopPolicyAndPartitions(t *testing.T) {
	t.Run("BatchStop", func(t *testing.T) {
		r := &archiveRun{fail: "file-01"}
		h := newHarness(t, []tasks.Definition{archiveDef(r, func(s *tasks.BatchSpec[noParams, string, tasks.Summary]) {
			s.OnItemError = tasks.BatchStop
		})}, nil)
		rec := h.waitState(h.start("gotham.archive", tasks.StartRequest{}), tasks.StateFailed, tasks.StateSucceeded)
		if rec.State != tasks.StateFailed {
			t.Fatalf("BatchStop run: %s, want failed", rec.State)
		}
		if got := r.got(); !slices.Equal(got, caseFiles[:2]) {
			t.Fatalf("ran %v, want only files 00 and 01", got)
		}
	})
	t.Run("PartitionBy", func(t *testing.T) {
		r := &archiveRun{}
		h := newHarness(t, []tasks.Definition{archiveDef(r, func(s *tasks.BatchSpec[noParams, string, tasks.Summary]) {
			s.Parallelism = tasks.Fixed(4, "four clerks")
			s.ChunkSize = 10
			s.PartitionBy = func(f string) string {
				if (f[len(f)-1]-'0')%2 == 0 {
					return "even"
				}
				return "odd"
			}
		})}, nil)
		h.waitState(h.start("gotham.archive", tasks.StartRequest{}), tasks.StateSucceeded)
		r.mu.Lock()
		ran := slices.Clone(r.ran)
		r.mu.Unlock()
		var even, odd []string
		for _, f := range ran {
			if (f[len(f)-1]-'0')%2 == 0 {
				even = append(even, f)
			} else {
				odd = append(odd, f)
			}
		}
		if !slices.IsSorted(even) || !slices.IsSorted(odd) || len(even)+len(odd) != 10 {
			t.Fatalf("partitions ran out of order: even %v odd %v", even, odd)
		}
	})
}

// TestBatch_PartitionItemTimeoutPerItem (5T.3.1, 5T.3.2: ItemTimeout is per
// item; synctest): in one partition, item 2 times out on its own while items
// 1 and 3 succeed. Items 1 and 3 together outlast one ItemTimeout, so a clock
// shared by the partition would also fail item 3.
func TestBatch_PartitionItemTimeoutPerItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var lateErr error
		var lateRan atomic.Bool
		var inFlight, peak atomic.Int64
		s := tasks.BatchSpec[noParams, string, tasks.Summary]{
			Common: common("gotham.stakeout"),
			Source: func(*tasks.RC, noParams) (tasks.Source[string], error) {
				return tasks.Source[string]{Items: []string{"post-1", "post-2", "post-3"}, Key: func(s string) string { return s }}, nil
			},
			Item: func(ic *tasks.ItemRC, _ noParams, post string) error {
				cur := inFlight.Add(1)
				defer inFlight.Add(-1)
				for p := peak.Load(); cur > p && !peak.CompareAndSwap(p, cur); p = peak.Load() {
				}
				switch post {
				case "post-2":
					<-ic.Context().Done()
					return ic.Context().Err()
				case "post-3":
					lateErr = ic.Context().Err()
					lateRan.Store(true)
				}
				time.Sleep(40 * time.Second)
				return ic.Context().Err()
			},
			Parallelism: tasks.Fixed(2, "two patrol cars"),
			PartitionBy: func(string) string { return "the narrows" },
			ItemTimeout: time.Minute,
		}
		h := newHarness(t, []tasks.Definition{tasks.Batch(tasks.Kind("gotham.stakeout"), s)}, nil)
		id := h.start("gotham.stakeout", tasks.StartRequest{})
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		r := h.get(id)
		if r.State != tasks.StateSucceeded {
			t.Fatalf("run %s (%q), want succeeded under BatchContinue", r.State, r.Error)
		}
		if peak.Load() != 1 {
			t.Fatalf("peak concurrency %d inside one partition, want 1", peak.Load())
		}
		if !lateRan.Load() || lateErr != nil {
			t.Fatalf("item 3 ran %v with context error %v, want it started on a live clock of its own", lateRan.Load(), lateErr)
		}
		var sum tasks.Summary
		if err := json.Unmarshal(r.Result, &sum); err != nil || sum.Done != 2 || sum.Failed != 1 {
			t.Fatalf("summary %s (%v), want items 1 and 3 done and item 2 failed", r.Result, err)
		}
	})
}

// TestFixer_ApplyPartitionBy (5T.3.1): a Fixer's PartitionBy holds at apply:
// the approved rows of one key run one at a time, in row order, even with
// parallel workers.
func TestFixer_ApplyPartitionBy(t *testing.T) {
	log := &applyLog{}
	var inFlight, peak atomic.Int64
	h := newHarness(t, []tasks.Definition{fixerDef(log, func(s *tasks.FixerSpec[noParams, string]) {
		s.Parallelism = tasks.Fixed(3, "three cell blocks")
		s.PartitionBy = func(tasks.Row) string { return "block-a" }
		s.Apply = func(_ *tasks.ItemRC, _ tasks.Writer, _ noParams, row tasks.Row) error {
			cur := inFlight.Add(1)
			for p := peak.Load(); cur > p && !peak.CompareAndSwap(p, cur); p = peak.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			log.add(row.ID)
			inFlight.Add(-1)
			return nil
		}
	})}, withPrincipal)
	plan := h.plan()
	if err := h.x.Approve(asUser("gordon", scopeApprov, scopeRun), plan, tasks.ApproveRequest{Rows: inmates}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	h.waitState(h.onlyApplyRun(plan).ID, tasks.StateSucceeded)
	log.mu.Lock()
	order := slices.Clone(log.ids)
	log.mu.Unlock()
	if !slices.Equal(order, inmates) || peak.Load() != 1 {
		t.Fatalf("applied %v with peak concurrency %d, want %v one at a time", order, peak.Load(), inmates)
	}
}

// standsSink is a ChangeSink whose Stands says no for the rows in undone.
type standsSink struct{ undone map[string]bool }

func (s *standsSink) Record(context.Context, tasks.RunID, []tasks.Change) error { return nil }
func (s *standsSink) Stands(_ context.Context, _ tasks.RunID, row string) (bool, error) {
	return !s.undone[row], nil
}

// TestFixer_ResumedApplyReportsStandingRows (5T.3.7; HEAD's
// appliedByEarlierAttempt): after a crash mid-apply, the resumed attempt
// reports the rows the first attempt applied (their marks) in AfterApply and
// Applied without writing them again, unless the module's ChangeSink says a
// row's changes no longer stand: that row is skipped, not re-applied.
func TestFixer_ResumedApplyReportsStandingRows(t *testing.T) {
	for _, undone := range []bool{false, true} {
		t.Run(map[bool]string{false: "stands", true: "undone"}[undone], func(t *testing.T) {
			log := &applyLog{}
			reached := make(chan struct{})
			var once sync.Once
			var amu sync.Mutex
			var after []string
			edit := func(blocking bool) fixerEdit {
				return func(s *tasks.FixerSpec[noParams, string]) {
					s.Apply = func(ic *tasks.ItemRC, _ tasks.Writer, _ noParams, row tasks.Row) error {
						if blocking && row.ID == "penguin" {
							once.Do(func() { close(reached) })
							<-ic.Context().Done()
							return ic.Context().Err()
						}
						log.add(row.ID)
						return nil
					}
					s.AfterApply = func(_ *tasks.RC, applied []string) error {
						amu.Lock()
						after = slices.Clone(applied)
						amu.Unlock()
						return nil
					}
				}
			}
			sink := &standsSink{undone: map[string]bool{"joker": undone}}
			withSink := func(o *Options) {
				withPrincipal(o)
				o.ChangeSinks = map[string]tasks.ChangeSink{"arkham": sink}
			}
			h := newHarness(t, []tasks.Definition{fixerDef(log, edit(true))}, withSink)
			plan := h.plan()
			if err := h.x.Approve(asUser("gordon", scopeApprov, scopeRun), plan, tasks.ApproveRequest{Rows: inmates}); err != nil {
				t.Fatalf("approve: %v", err)
			}
			apply := h.onlyApplyRun(plan).ID
			<-reached
			deadline := time.Now().Add(10 * time.Second)
			for !h.marked(apply, "joker") {
				if time.Now().After(deadline) {
					t.Fatal("the first attempt never marked joker applied")
				}
				time.Sleep(2 * time.Millisecond)
			}
			next := h.crashBoot([]tasks.Definition{fixerDef(log, edit(false))}, withSink)
			r := next.waitState(apply, tasks.StateSucceeded)
			want := []string{"joker", "penguin", "riddler"}
			if undone {
				want = want[1:]
			}
			amu.Lock()
			got := slices.Clone(after)
			amu.Unlock()
			if !slices.Equal(got, want) || result(t, r).Applied != len(want) {
				t.Fatalf("AfterApply %v, result %+v, want %v", got, result(t, r), want)
			}
			log.mu.Lock()
			jokers := 0
			for _, id := range log.ids {
				if id == "joker" {
					jokers++
				}
			}
			log.mu.Unlock()
			if jokers != 1 {
				t.Fatalf("joker applied %d times, want once (the resume never writes a marked row again)", jokers)
			}
		})
	}
}

// marked reports whether the run carries row's applied mark.
func (h *harness) marked(id tasks.RunID, row string) bool {
	h.t.Helper()
	for m, err := range h.stores.Runs.Marks(context.Background(), id) {
		if err != nil {
			h.t.Fatalf("marks: %v", err)
		}
		if m == row {
			return true
		}
	}
	return false
}
