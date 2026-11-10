// file: core/tasks/items_test.go
// version: 1.2.0
// guid: acb0e36f-08d6-44a4-beac-44e9516bf795
// last-edited: 2026-11-10

package tasks_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

const caseKind tasks.Kind = "gotham.cases.review"

func asItemsError(t *testing.T, err error) *tasks.ItemsError {
	t.Helper()
	var ie *tasks.ItemsError
	if !errors.As(err, &ie) {
		t.Fatalf("error %v (%T) is not *tasks.ItemsError", err, err)
	}
	return ie
}

func TestRunItems_ZeroParallelismFailsBeforeFirstItem(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	var calls atomic.Int64
	fn := func(*tasks.ItemRC, int) error { calls.Add(1); return nil }
	items := []int{1, 2, 3}
	seq := slices.Values(items)

	for name, call := range map[string]func() (tasks.ItemsResult, error){
		"RunItems": func() (tasks.ItemsResult, error) { return tasks.RunItems(run.RC(), tasks.Parallelism{}, items, fn) },
		"RunSeq": func() (tasks.ItemsResult, error) {
			return tasks.RunSeq(run.RC(), tasks.Parallelism{}, seq, tasks.Known(3), fn)
		},
		"ForEach": func() (tasks.ItemsResult, error) {
			return tasks.ForEach(t.Context(), tasks.Parallelism{}, items, func(context.Context, int) error { calls.Add(1); return nil })
		},
		"FixedNoReason": func() (tasks.ItemsResult, error) { return tasks.RunItems(run.RC(), tasks.Fixed(2, ""), items, fn) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := call()
			ie := asItemsError(t, err)
			if !errors.Is(err, tasks.ErrNoParallelism) {
				t.Fatalf("errors.Is(err, ErrNoParallelism) = false: %v", err)
			}
			if ie.First != tasks.ErrNoParallelism || ie.FirstIndex != -1 || len(ie.Errs) != 0 || ie.Cause != nil {
				t.Fatalf("ItemsError = %+v, want {First: ErrNoParallelism, FirstIndex: -1}", ie)
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("fn ran %d times before the parallelism was refused", n)
	}
}

func TestRunItems_NilRCRefused(t *testing.T) {
	_, err := tasks.RunItems(nil, tasks.CPU(), []int{1}, func(*tasks.ItemRC, int) error { return nil })
	if ie := asItemsError(t, err); ie.First != tasks.ErrNoRunContext || ie.FirstIndex != -1 {
		t.Fatalf("ItemsError = %+v, want First ErrNoRunContext", ie)
	}
	if tasks.NewRC(nil) != nil {
		t.Fatal("NewRC(nil) built an RC")
	}
}

func TestRunItems_ManyOptionsRefused(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	var o tasks.ItemsOptions[int]
	_, err := tasks.RunItems(run.RC(), tasks.CPU(), []int{1}, func(*tasks.ItemRC, int) error { return nil }, o, o)
	if ie := asItemsError(t, err); ie.FirstIndex != -1 || ie.First == nil {
		t.Fatalf("two ItemsOptions accepted: %+v", ie)
	}
}

// TestRunItems_CancelStopsDispatch: cancel while 3 of 8 are in flight; no fn
// starts after the cancel and NotStarted is exact.
func TestRunItems_CancelStopsDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		run := taskstest.NewFakeRun(ctx, caseKind)
		var started atomic.Int64
		var canceled, startedAfter atomic.Bool
		var res tasks.ItemsResult
		var err error
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err = tasks.RunItems(run.RC(), tasks.Fixed(3, "three detectives on shift"), make([]int, 8),
				func(ic *tasks.ItemRC, _ int) error {
					if canceled.Load() {
						startedAfter.Store(true)
					}
					started.Add(1)
					<-ic.Context().Done()
					return context.Cause(ic.Context())
				})
		}()
		synctest.Wait()
		if n := started.Load(); n != 3 {
			t.Fatalf("%d items in flight before the cancel, want 3", n)
		}
		canceled.Store(true)
		cancel(tasks.ErrCanceled)
		<-done

		if startedAfter.Load() || started.Load() != 3 {
			t.Fatalf("an item started after the cancel (started %d)", started.Load())
		}
		if res.NotStarted != 5 || res.Failed != 3 || res.Done != 0 {
			t.Fatalf("result = %+v, want NotStarted 5, Failed 3", res)
		}
		ie := asItemsError(t, err)
		if ie.Cause != tasks.ErrCanceled {
			t.Fatalf("Cause = %v, want ErrCanceled", ie.Cause)
		}
		if !errors.Is(err, context.Canceled) || !errors.Is(err, tasks.ErrCanceled) {
			t.Fatalf("errors.Is(err, context.Canceled / ErrCanceled) = false: %v", err)
		}
	})
}

// TestRunItems_ErrorShapeSameSeqAndPar: Sequential and CPU return identical
// *ItemsError fields in both error modes.
func TestRunItems_ErrorShapeSameSeqAndPar(t *testing.T) {
	errJoker := errors.New("joker escaped")
	errRiddler := errors.New("riddler left a clue")
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	cases := []struct {
		name string
		mode tasks.ErrorMode
		fail map[int]error
		want tasks.ItemsError
	}{
		{"fail_fast", tasks.FailFast, map[int]error{2: errJoker},
			tasks.ItemsError{First: errJoker, FirstIndex: 2, Errs: []error{errJoker}}},
		{"collect", tasks.Collect, map[int]error{2: errJoker, 5: errRiddler},
			tasks.ItemsError{First: errJoker, FirstIndex: 2, Errs: []error{errJoker, errRiddler}}},
	}
	for _, c := range cases {
		for _, par := range []tasks.Parallelism{tasks.Sequential("one interrogation room"), tasks.CPU()} {
			t.Run(c.name+"/"+par.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					run := taskstest.NewFakeRun(t.Context(), caseKind)
					_, err := tasks.RunItems(run.RC(), par, items, func(_ *tasks.ItemRC, i int) error {
						if i == 5 { // completes after item 2 in both shapes
							time.Sleep(time.Second)
						}
						return c.fail[i]
					}, tasks.ItemsOptions[int]{OnError: c.mode})
					got := asItemsError(t, err)
					if got.First != c.want.First || got.FirstIndex != c.want.FirstIndex ||
						!slices.Equal(got.Errs, c.want.Errs) || got.Cause != nil {
						t.Fatalf("ItemsError = %+v, want %+v", got, c.want)
					}
				})
			})
		}
	}
}

// TestRunItems_LabelGetsItemOnly_RaceFree: 64 workers; Label sees only the
// item, runs in the workers, and every completed item's label reaches the
// progress delivery. Run under -race.
func TestRunItems_LabelGetsItemOnly_RaceFree(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	const n = 2000
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	var inFlight, peak atomic.Int64
	res, err := tasks.RunItems(run.RC(), tasks.Fixed(64, "sixty-four Robins"), items,
		func(ic *tasks.ItemRC, _ int) error {
			cur := inFlight.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			ic.Count("solved", 1)
			inFlight.Add(-1)
			return nil
		},
		tasks.ItemsOptions[int]{Label: func(i int) string { return "case-" + strconv.Itoa(i) }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != n || res.Counters["solved"] != n {
		t.Fatalf("result = %+v", res)
	}
	if p := peak.Load(); p > 64 {
		t.Fatalf("%d items ran at once, want at most 64", p)
	}
	prog := run.Progress()
	if len(prog) != n {
		t.Fatalf("%d progress deliveries, want %d", len(prog), n)
	}
	labels := make(map[string]bool, n)
	for i, p := range prog {
		if p.Done != int64(i+1) || p.Total != n {
			t.Fatalf("delivery %d = %+v, want Done %d of %d in completion order", i, p, i+1, n)
		}
		labels[p.Current] = true
	}
	for _, i := range []int{0, n / 2, n - 1} {
		if !labels["case-"+strconv.Itoa(i)] {
			t.Fatalf("label of item %d never delivered", i)
		}
	}
}

func TestRunItems_CountersAtomic(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	const n = 1000
	items := make([]int, n)
	var wantClues int64
	for i := range items {
		items[i] = i
		wantClues += int64(i)
	}
	res, err := tasks.RunItems(run.RC(), tasks.CPU(), items, func(ic *tasks.ItemRC, i int) error {
		ic.Count("cases", 1)
		ic.Count("clues", int64(i))
		if i%10 == 0 {
			ic.Skip("cold case")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters["cases"] != n || res.Counters["clues"] != wantClues {
		t.Fatalf("counters = %v, want cases %d clues %d", res.Counters, n, wantClues)
	}
	if res.Skipped != n/10 || res.Done != n-n/10 || res.Counters["skipped:cold case"] != n/10 {
		t.Fatalf("result = %+v", res)
	}
}

func TestForEach_WatermarkRefused(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	var calls atomic.Int64
	for name, ctx := range map[string]context.Context{"plain": t.Context(), "from_rc": run.RC().Context()} {
		t.Run(name, func(t *testing.T) {
			_, err := tasks.ForEach(ctx, tasks.CPU(), []int{1, 2}, func(context.Context, int) error {
				calls.Add(1)
				return nil
			}, tasks.ItemsOptions[int]{Watermark: "batcave"})
			ie := asItemsError(t, err)
			if ie.First != tasks.ErrNoRunContext || ie.FirstIndex != -1 || !errors.Is(err, tasks.ErrNoRunContext) {
				t.Fatalf("ItemsError = %+v, want {First: ErrNoRunContext, FirstIndex: -1}", ie)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("fn ran although the watermark was refused")
	}
}

// TestRunItems_WatermarkPrefixAndResume is the loop half of the watermark row
// over a FakeRun (the executor-backed TestRunItems_WatermarkResume is F2-03's):
// a failed item stops the prefix, the checkpoint holds it, and a second
// attempt runs only the items after it.
func TestRunItems_WatermarkPrefixAndResume(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	opts := tasks.ItemsOptions[int]{Watermark: "arkham", CheckpointEvery: 2, OnError: tasks.Collect}
	res, err := tasks.RunItems(run.RC(), tasks.Sequential("ordered ledger"), items, func(_ *tasks.ItemRC, i int) error {
		if i == 6 {
			return errors.New("bane broke the ledger")
		}
		return nil
	}, opts)
	if err == nil || res.Watermark != 6 {
		t.Fatalf("first attempt: watermark %d err %v, want 6 and an error", res.Watermark, err)
	}
	if raw, _ := run.CheckpointJSON("arkham"); string(raw) != `{"n":6}` {
		t.Fatalf("checkpoint = %s, want {\"n\":6}", raw)
	}
	var ran []int
	var mu sync.Mutex
	res, err = tasks.RunItems(run.RC(), tasks.Sequential("ordered ledger"), items, func(_ *tasks.ItemRC, i int) error {
		mu.Lock()
		ran = append(ran, i)
		mu.Unlock()
		return nil
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []int{6, 7, 8, 9}) || res.Watermark != 10 || res.Done != 10 || res.NotStarted != 0 {
		t.Fatalf("resume ran %v, result %+v; want items 6..9, watermark 10, Done 10", ran, res)
	}
	if raw, _ := run.CheckpointJSON("arkham"); string(raw) != `{"n":10}` {
		t.Fatalf("checkpoint = %s, want {\"n\":10}", raw)
	}
}

func TestRunItems_WatermarkPersistErrorStops(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	errVault := errors.New("vault offline")
	run.FailCheckpoints(errVault)
	res, err := tasks.RunItems(run.RC(), tasks.Sequential("ordered ledger"), make([]int, 10),
		func(*tasks.ItemRC, int) error { return nil },
		tasks.ItemsOptions[int]{Watermark: "arkham", CheckpointEvery: 3})
	ie := asItemsError(t, err)
	if !errors.Is(err, errVault) || ie.FirstIndex != -1 {
		t.Fatalf("ItemsError = %+v, want the checkpoint error with FirstIndex -1", ie)
	}
	if res.Done != 3 || res.NotStarted != 7 {
		t.Fatalf("result = %+v, want the loop stopped after the failed checkpoint", res)
	}
}

func TestRunSeq_KeyedWatermark(t *testing.T) {
	villains := []string{"joker", "penguin", "riddler", "two-face", "scarecrow"}
	key := func(s string) string { return s }
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	if err := run.SetCheckpoint("rogues", map[string]any{"n": 2, "key": "penguin"}); err != nil {
		t.Fatal(err)
	}
	var ran []string
	res, err := tasks.RunSeq(run.RC(), tasks.Sequential("one cell at a time"), slices.Values(villains), tasks.Known(5),
		func(_ *tasks.ItemRC, v string) error { ran = append(ran, v); return nil },
		tasks.ItemsOptions[string]{Watermark: "rogues", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, villains[2:]) || res.Watermark != 5 || res.Done != 5 {
		t.Fatalf("ran %v, result %+v", ran, res)
	}

	moved := taskstest.NewFakeRun(t.Context(), caseKind)
	if err := moved.SetCheckpoint("rogues", map[string]any{"n": 2, "key": "harley"}); err != nil {
		t.Fatal(err)
	}
	_, err = tasks.RunSeq(moved.RC(), tasks.Sequential("one cell at a time"), slices.Values(villains), tasks.Unknown(),
		func(*tasks.ItemRC, string) error { t.Error("an item ran after an order mismatch"); return nil },
		tasks.ItemsOptions[string]{Watermark: "rogues", Key: key})
	if ie := asItemsError(t, err); ie.FirstIndex != -1 {
		t.Fatalf("order mismatch not refused: %+v", ie)
	}

	_, err = tasks.RunSeq(run.RC(), tasks.CPU(), slices.Values(villains), tasks.Unknown(),
		func(*tasks.ItemRC, string) error { return nil }, tasks.ItemsOptions[string]{Watermark: "rogues"})
	if ie := asItemsError(t, err); ie.FirstIndex != -1 || ie.First == nil {
		t.Fatalf("RunSeq Watermark without Key accepted: %+v", ie)
	}
}

func TestRunSeq_StreamsWithBoundedWorkers(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	var sum atomic.Int64
	seq := func(yield func(int) bool) {
		for i := range 300 {
			if !yield(i) {
				return
			}
		}
	}
	res, err := tasks.RunSeq(run.RC(), tasks.Fixed(4, "four patrol cars"), seq, tasks.Unknown(),
		func(_ *tasks.ItemRC, i int) error { sum.Add(int64(i)); return nil })
	if err != nil || res.Done != 300 || sum.Load() != 299*300/2 {
		t.Fatalf("result %+v, sum %d, err %v", res, sum.Load(), err)
	}
}

func TestForEach_PlainPoolExclusiveRefs(t *testing.T) {
	// A season pack (the whole kind) and single episodes: overlapping refs
	// never run at once (COR-7).
	pack := tasks.Res("gotham", "episodes")
	type job struct {
		refs []entity.Ref
	}
	var jobs []job
	for i := range 40 {
		if i%5 == 0 {
			jobs = append(jobs, job{refs: []entity.Ref{pack}})
		} else {
			jobs = append(jobs, job{refs: []entity.Ref{tasks.ResID("gotham", "episodes", strconv.Itoa(i%3))}})
		}
	}
	var mu sync.Mutex
	holding := map[string]int{}
	packHeld := 0
	res, err := tasks.ForEach(t.Context(), tasks.Fixed(8, "eight bat-signals"), jobs, func(_ context.Context, j job) error {
		mu.Lock()
		for _, r := range j.refs {
			if r.ID == "" {
				if packHeld > 0 || len(holding) > 0 {
					mu.Unlock()
					return errors.New("pack overlapped a held episode")
				}
				packHeld++
			} else {
				if packHeld > 0 || holding[r.ID] > 0 {
					mu.Unlock()
					return errors.New("episode overlapped a held ref")
				}
				holding[r.ID]++
			}
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		for _, r := range j.refs {
			if r.ID == "" {
				packHeld--
			} else if holding[r.ID]--; holding[r.ID] == 0 {
				delete(holding, r.ID)
			}
		}
		mu.Unlock()
		return nil
	}, tasks.ItemsOptions[job]{ExclusiveRefs: func(j job) []entity.Ref { return j.refs }, OnError: tasks.Collect})
	if err != nil || res.Done != 40 {
		t.Fatalf("result %+v err %v", res, err)
	}
}

// TestForEach_RCContextUsesRunHooks: a ctx from an RC makes ForEach take the
// run's CPU slots and park on its pause gate; a plain ctx takes neither.
func TestForEach_RCContextUsesRunHooks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := taskstest.NewFakeRun(t.Context(), caseKind)
		run.SetCPUSlots(2)
		run.Pause()
		var started atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := tasks.ForEach(run.RC().Context(), tasks.CPU(), make([]int, 12), func(ctx context.Context, _ int) error {
				started.Add(1)
				time.Sleep(time.Second)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if started.Load() != 0 {
			t.Fatal("an item started while the run was paused")
		}
		run.Unpause()
		<-done
		acquires, peak := run.CPUStats()
		if acquires != 12 || peak > 2 {
			t.Fatalf("CPU slots: %d acquires, peak %d; want 12 acquires and at most 2 at once", acquires, peak)
		}

		plain := taskstest.NewFakeRun(t.Context(), caseKind)
		if _, err := tasks.ForEach(t.Context(), tasks.CPU(), make([]int, 3), func(context.Context, int) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if acquires, _ := plain.CPUStats(); acquires != 0 {
			t.Fatal("a plain ctx reached a run's CPU budget")
		}
	})
}

func TestRunItems_CapLowersWorkersAndFixedTakesNoCPUSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := taskstest.NewFakeRun(t.Context(), caseKind)
		run.SetCap(2)
		var inFlight, peak atomic.Int64
		_, err := tasks.RunItems(run.RC(), tasks.Fixed(8, "eight informants"), make([]int, 16), func(*tasks.ItemRC, int) error {
			cur := inFlight.Add(1)
			for p := peak.Load(); cur > p && !peak.CompareAndSwap(p, cur); p = peak.Load() {
			}
			time.Sleep(time.Second)
			inFlight.Add(-1)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if peak.Load() != 2 {
			t.Fatalf("peak concurrency %d, want the cap 2", peak.Load())
		}
		if acquires, _ := run.CPUStats(); acquires != 0 {
			t.Fatalf("Fixed workers took %d CPU slots, want 0", acquires)
		}
	})
}

func TestRunItems_ItemTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := taskstest.NewFakeRun(t.Context(), caseKind)
		res, err := tasks.RunItems(run.RC(), tasks.Sequential("one stakeout"), []int{1}, func(ic *tasks.ItemRC, _ int) error {
			<-ic.Context().Done()
			return ic.Context().Err()
		}, tasks.ItemsOptions[int]{ItemTimeout: time.Minute})
		if !errors.Is(err, context.DeadlineExceeded) || res.Failed != 1 {
			t.Fatalf("result %+v err %v, want the item to time out", res, err)
		}
		if ie := asItemsError(t, err); ie.Cause != nil {
			t.Fatalf("an item timeout set the run Cause %v", ie.Cause)
		}
	})
}

func TestRunItems_ProgressBaseAndTotal(t *testing.T) {
	run := taskstest.NewFakeRun(t.Context(), caseKind)
	_, err := tasks.RunItems(run.RC(), tasks.Sequential("ordered"), []int{1, 2, 3}, func(*tasks.ItemRC, int) error { return nil },
		tasks.ItemsOptions[int]{ProgressBase: 100, ProgressTotal: 500})
	if err != nil {
		t.Fatal(err)
	}
	want := []taskstest.ProgressEvent{{Done: 101, Total: 500}, {Done: 102, Total: 500}, {Done: 103, Total: 500}}
	if got := run.Progress(); !slices.Equal(got, want) {
		t.Fatalf("progress %+v, want %+v", got, want)
	}
}

func TestItemsError_UnwrapAndError(t *testing.T) {
	errA := errors.New("alfred is out")
	e := &tasks.ItemsError{First: errA, FirstIndex: 4, Errs: []error{errA, errors.New("b")}, Cause: tasks.ErrShutdown}
	if !errors.Is(e, errA) || !errors.Is(e, context.Canceled) || !errors.Is(e, tasks.ErrShutdown) {
		t.Fatal("Unwrap does not yield First, Errs and Cause")
	}
	want := "tasks: items: alfred is out (item 4); 2 item errors; run stopped: " + tasks.ErrShutdown.Error()
	if e.Error() != want {
		t.Fatalf("Error() = %q, want %q", e.Error(), want)
	}
}

func TestItemRC_HostlessForEachHasNoRunServices(t *testing.T) {
	// ForEach with a plain ctx: fn receives a ctx with no run behind it, so
	// LockRefs refuses.
	_, err := tasks.ForEach(t.Context(), tasks.Sequential("one"), []int{1}, func(ctx context.Context, _ int) error {
		if _, err := tasks.LockRefs(ctx, tasks.Res("gotham", "cases")); !errors.Is(err, tasks.ErrNoRunContext) {
			return errors.New("LockRefs on a plain ctx did not refuse")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// gateHost fails one loop gate with a plain error while the run is live.
type gateHost struct {
	*taskstest.FakeRun
	locks, cpu error
}

func (h gateHost) LockRefs(ctx context.Context, refs ...entity.Ref) (func(), error) {
	if h.locks != nil {
		return nil, h.locks
	}
	return h.FakeRun.LockRefs(ctx, refs...)
}

func (h gateHost) AcquireCPU(ctx context.Context) (func(), error) {
	if h.cpu != nil {
		return nil, h.cpu
	}
	return h.FakeRun.AcquireCPU(ctx)
}

// TestRunItems_GateErrorFailsTheItem: a ref-lock or CPU-slot error on a live
// run is an item failure that stops the loop in both error modes, never a
// silent "not started" with a nil error.
func TestRunItems_GateErrorFailsTheItem(t *testing.T) {
	errTable := errors.New("ref-lock table offline")
	refs := func(int) []entity.Ref { return []entity.Ref{tasks.ResID("gotham", "cases", "1")} }
	for name, c := range map[string]struct {
		host gateHost
		par  tasks.Parallelism
	}{
		"LockRefs":   {gateHost{locks: errTable}, tasks.Sequential("one cell")},
		"AcquireCPU": {gateHost{cpu: errTable}, tasks.CPU()},
	} {
		for _, mode := range []tasks.ErrorMode{tasks.FailFast, tasks.Collect} {
			t.Run(name+"/"+strconv.Itoa(int(mode)), func(t *testing.T) {
				c.host.FakeRun = taskstest.NewFakeRun(t.Context(), caseKind)
				c.host.SetCap(1)
				var ran atomic.Int64
				res, err := tasks.RunItems(tasks.NewRC(c.host), c.par, []int{1, 2, 3},
					func(*tasks.ItemRC, int) error { ran.Add(1); return nil },
					tasks.ItemsOptions[int]{OnError: mode, ExclusiveRefs: refs})
				ie := asItemsError(t, err)
				if !errors.Is(err, errTable) || ie.FirstIndex != 0 || ie.Cause != nil {
					t.Fatalf("ItemsError = %+v, want the gate error at item 0 and no Cause", ie)
				}
				if ran.Load() != 0 || res.Failed != 1 || res.NotStarted != 2 || res.Done != 0 {
					t.Fatalf("ran %d, result %+v; want Failed 1, NotStarted 2", ran.Load(), res)
				}
			})
		}
	}
}

// cancelOnPause cancels the run while parked on the pause gate and still
// returns nil, so only the second context check can stop the item.
type cancelOnPause struct {
	*taskstest.FakeRun
	cancel context.CancelCauseFunc
}

func (h cancelOnPause) AwaitUnpaused(context.Context) error {
	h.cancel(tasks.ErrCanceled)
	return nil
}

func TestRunItems_SecondContextCheckBeforeFn(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	h := cancelOnPause{FakeRun: taskstest.NewFakeRun(ctx, caseKind), cancel: cancel}
	var ran atomic.Int64
	res, err := tasks.RunItems(tasks.NewRC(h), tasks.Fixed(4, "four detectives"), make([]int, 6),
		func(*tasks.ItemRC, int) error { ran.Add(1); return nil })
	if ran.Load() != 0 {
		t.Fatalf("fn ran %d times after the run was canceled", ran.Load())
	}
	if res.NotStarted != 6 || res.Failed != 0 || res.Done != 0 {
		t.Fatalf("result = %+v, want NotStarted 6", res)
	}
	if ie := asItemsError(t, err); ie.Cause != tasks.ErrCanceled || len(ie.Errs) != 0 {
		t.Fatalf("ItemsError = %+v, want Cause ErrCanceled and no item error", ie)
	}
}

// TestRunItems_CauseOnlyWhenStoppedEarly: a run canceled by its last item,
// after every item started, did not stop the loop early: no error.
func TestRunItems_CauseOnlyWhenStoppedEarly(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	run := taskstest.NewFakeRun(ctx, caseKind)
	res, err := tasks.RunItems(run.RC(), tasks.Sequential("in order"), []int{0, 1, 2}, func(_ *tasks.ItemRC, i int) error {
		if i == 2 {
			cancel(tasks.ErrShutdown)
		}
		return nil
	})
	if err != nil || res.Done != 3 {
		t.Fatalf("result %+v err %v, want Done 3 and no error", res, err)
	}
}

// TestWatermark_ResumeWithProgressBase: the checkpoint is absolute
// (ProgressBase + prefix) and a resumed RunItems or RunSeq skips exactly the
// items of this slice it covers.
func TestWatermark_ResumeWithProgressBase(t *testing.T) {
	rogues := []string{"joker", "penguin", "riddler", "two-face", "scarecrow"}
	failRiddler := func(_ *tasks.ItemRC, s string) error {
		if s == "riddler" {
			return errors.New("riddle unsolved")
		}
		return nil
	}
	for name, run := range map[string]func(*tasks.RC, func(*tasks.ItemRC, string) error) (tasks.ItemsResult, error){
		"RunItems": func(rc *tasks.RC, fn func(*tasks.ItemRC, string) error) (tasks.ItemsResult, error) {
			return tasks.RunItems(rc, tasks.Sequential("in order"), rogues, fn,
				tasks.ItemsOptions[string]{Watermark: "w", CheckpointEvery: 1, ProgressBase: 100})
		},
		"RunSeq": func(rc *tasks.RC, fn func(*tasks.ItemRC, string) error) (tasks.ItemsResult, error) {
			return tasks.RunSeq(rc, tasks.Sequential("in order"), slices.Values(rogues), tasks.Unknown(), fn,
				tasks.ItemsOptions[string]{Watermark: "w", CheckpointEvery: 1, ProgressBase: 100, Key: func(s string) string { return s }})
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := taskstest.NewFakeRun(t.Context(), caseKind)
			if res, err := run(fake.RC(), failRiddler); err == nil || res.Watermark != 102 {
				t.Fatalf("first attempt: watermark %d err %v, want 102 and an error", res.Watermark, err)
			}
			raw, _ := fake.CheckpointJSON("w")
			if want := map[string]string{"RunItems": `{"n":102}`, "RunSeq": `{"n":102,"key":"penguin"}`}[name]; string(raw) != want {
				t.Fatalf("checkpoint = %s, want %s", raw, want)
			}
			var seen []string
			res, err := run(fake.RC(), func(_ *tasks.ItemRC, s string) error { seen = append(seen, s); return nil })
			if err != nil || !slices.Equal(seen, rogues[2:]) || res.Watermark != 105 || res.Done != 5 {
				t.Fatalf("resume saw %v, result %+v, err %v; want the last three, watermark 105, Done 5", seen, res, err)
			}
		})
	}
}

// TestForEach_NestedCPUInheritsSlot: a CPU() ForEach inside a CPU() item runs
// on the item's slot (one worker, no AcquireCPU) instead of deadlocking the
// budget waiting for a second slot.
func TestForEach_NestedCPUInheritsSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		run := taskstest.NewFakeRun(ctx, caseKind)
		run.SetCPUSlots(2)
		var peakInner atomic.Int64
		res, err := tasks.RunItems(run.RC(), tasks.CPU(), make([]int, 4), func(ic *tasks.ItemRC, _ int) error {
			var inFlight atomic.Int64
			_, err := tasks.ForEach(ic.Context(), tasks.CPU(), make([]int, 3), func(context.Context, int) error {
				n := inFlight.Add(1)
				for p := peakInner.Load(); n > p && !peakInner.CompareAndSwap(p, n); p = peakInner.Load() {
				}
				time.Sleep(time.Millisecond)
				inFlight.Add(-1)
				return nil
			})
			return err
		})
		if err != nil || res.Done != 4 {
			t.Fatalf("result %+v err %v, want Done 4 (a nested CPU loop deadlocked?)", res, err)
		}
		if acquires, peak := run.CPUStats(); acquires != 4 || peak > 2 {
			t.Fatalf("CPU slots: %d acquires, peak %d; want 4 (outer items only), at most 2 at once", acquires, peak)
		}
		if peakInner.Load() != 1 {
			t.Fatalf("a nested loop ran %d items at once on one inherited slot, want 1", peakInner.Load())
		}
	})
}

// TestRunItems_PanicBecomesItemError: a panic in fn, Label, Key or
// ExclusiveRefs fails that item with a one-line *PanicError that carries the
// stack, which is logged once through the run's logger; the loop and its
// other items carry on.
func TestRunItems_PanicBecomesItemError(t *testing.T) {
	errGas := errors.New("laughing gas")
	boom := func(i int) bool { return i == 1 }
	for name, c := range map[string]struct {
		fn   func(*tasks.ItemRC, int) error
		opts tasks.ItemsOptions[int]
	}{
		"fn": {fn: func(_ *tasks.ItemRC, i int) error {
			if boom(i) {
				panic(errGas)
			}
			return nil
		}},
		"Label": {opts: tasks.ItemsOptions[int]{Label: func(i int) string {
			if boom(i) {
				panic(errGas)
			}
			return ""
		}}},
		"Key": {opts: tasks.ItemsOptions[int]{Watermark: "w", Key: func(i int) string {
			if boom(i) {
				panic(errGas)
			}
			return strconv.Itoa(i)
		}}},
		"ExclusiveRefs": {opts: tasks.ItemsOptions[int]{ExclusiveRefs: func(i int) []entity.Ref {
			if boom(i) {
				panic(errGas)
			}
			return nil
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			fn := c.fn
			if fn == nil {
				fn = func(*tasks.ItemRC, int) error { return nil }
			}
			c.opts.OnError = tasks.Collect
			logs := &syncBuffer{}
			h := logHost{FakeRun: taskstest.NewFakeRun(t.Context(), caseKind), log: slog.New(slog.NewTextHandler(logs, nil))}
			res, err := tasks.RunItems(tasks.NewRC(h), tasks.Sequential("in order"), []int{0, 1, 2}, fn, c.opts)
			ie := asItemsError(t, err)
			var pe *tasks.PanicError
			if !errors.Is(err, errGas) || ie.FirstIndex != 1 || !errors.As(ie.First, &pe) || len(pe.Stack) == 0 {
				t.Fatalf("ItemsError = %v (index %d), want the %s panic as a *PanicError with a stack at item 1", ie.First, ie.FirstIndex, name)
			}
			if msg := ie.First.Error(); msg != "tasks: "+name+": panic: laughing gas" || strings.Contains(err.Error(), "\n") {
				t.Fatalf("error text %q is not one line naming the panic", msg)
			}
			if out := logs.String(); strings.Count(out, "level=ERROR") != 1 || !strings.Contains(out, "goroutine ") {
				t.Fatalf("the stack was not logged once at Error level: %q", out)
			}
			if res.Failed != 1 || res.Done != 2 {
				t.Fatalf("result = %+v, want Failed 1, Done 2", res)
			}
		})
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent log writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// logHost is a FakeRun whose run logger writes where the test can read it.
type logHost struct {
	*taskstest.FakeRun
	log *slog.Logger
}

func (h logHost) Log() *slog.Logger { return h.log }

func TestPanicError_OneLineAndUnwrap(t *testing.T) {
	errGas := errors.New("laughing gas")
	e := &tasks.PanicError{Value: "first line\nsecond line", Stack: []byte("goroutine 1")}
	if e.Error() != "panic: first line second line" || e.Unwrap() != nil {
		t.Fatalf("Error() = %q, Unwrap() = %v", e.Error(), e.Unwrap())
	}
	if w := (&tasks.PanicError{Value: errGas}); !errors.Is(w, errGas) {
		t.Fatal("a panic(err) does not unwrap to err")
	}
}

// TestRunItems_CauseWhenCanceledAllInFlight: a shutdown while every item is
// in flight (none left to start) still reports the run's cause, because the
// items failed after the run's context ended.
func TestRunItems_CauseWhenCanceledAllInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		run := taskstest.NewFakeRun(ctx, caseKind)
		var res tasks.ItemsResult
		var err error
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err = tasks.RunItems(run.RC(), tasks.Fixed(3, "three detectives"), make([]int, 3), func(ic *tasks.ItemRC, _ int) error {
				<-ic.Context().Done()
				return ic.Context().Err()
			})
		}()
		synctest.Wait()
		cancel(tasks.ErrShutdown)
		<-done
		ie := asItemsError(t, err)
		if ie.Cause != tasks.ErrShutdown || !errors.Is(err, tasks.ErrShutdown) || res.Failed != 3 || res.NotStarted != 0 {
			t.Fatalf("result %+v, ItemsError %+v; want Failed 3 and Cause ErrShutdown", res, ie)
		}
	})
}

// TestRunItems_OwnFailureBeforeCancelHasNoCause: an item that failed on its
// own before the run was canceled does not pick up the run's cause.
func TestRunItems_OwnFailureBeforeCancelHasNoCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	run := taskstest.NewFakeRun(ctx, caseKind)
	errClue := errors.New("clue missing")
	res, err := tasks.RunItems(run.RC(), tasks.Sequential("in order"), []int{0, 1}, func(_ *tasks.ItemRC, i int) error {
		if i == 0 {
			return errClue
		}
		cancel(tasks.ErrShutdown)
		return nil
	}, tasks.ItemsOptions[int]{OnError: tasks.Collect})
	if ie := asItemsError(t, err); ie.Cause != nil || ie.First != errClue || res.Done != 1 || res.Failed != 1 {
		t.Fatalf("result %+v, ItemsError %+v; want the item's own error and no Cause", res, ie)
	}
}

// TestNestedRefLocks_SkipHeldRefs: an item holding a ref can run a nested
// loop and call ic.LockRefs on that ref without waiting on itself; the
// nested loop's items still exclude each other on it; an overlapping ref the
// item does not cover is refused, not waited for.
func TestNestedRefLocks_SkipHeldRefs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		run := taskstest.NewFakeRun(ctx, caseKind)
		cave := tasks.ResID("gotham", "caves", "batcave")
		lab := tasks.ResID("gotham", "labs", "lab-1")
		var inFlight, peak atomic.Int64
		res, err := tasks.RunItems(run.RC(), tasks.Sequential("one case"), []int{0}, func(ic *tasks.ItemRC, _ int) error {
			release, err := ic.LockRefs(cave)
			if err != nil {
				return err
			}
			release()
			if _, err := ic.LockRefs(tasks.Res("gotham", "caves")); !errors.Is(err, tasks.ErrHeldOverlap) {
				return fmt.Errorf("a whole-kind lock over a held ID = %v, want ErrHeldOverlap", err)
			}
			_, err = tasks.ForEach(ic.Context(), tasks.Fixed(4, "four detectives"), make([]int, 4), func(context.Context, int) error {
				n := inFlight.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(time.Millisecond)
				inFlight.Add(-1)
				return nil
			}, tasks.ItemsOptions[int]{ExclusiveRefs: func(int) []entity.Ref { return []entity.Ref{cave, lab} }})
			return err
		}, tasks.ItemsOptions[int]{ExclusiveRefs: func(int) []entity.Ref { return []entity.Ref{cave} }})
		if err != nil || res.Done != 1 {
			t.Fatalf("result %+v err %v (a nested lock waited on its own item?)", res, err)
		}
		if peak.Load() != 1 {
			t.Fatalf("nested items sharing a held ref ran %d at once, want 1", peak.Load())
		}
		if _, err := tasks.LockRefs(run.RC().Context(), cave, lab); err != nil {
			t.Fatalf("a ref stayed locked after the run: %v", err)
		}
	})
}
