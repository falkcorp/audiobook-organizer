// file: core/tasks/items_test.go
// version: 1.0.0
// guid: acb0e36f-08d6-44a4-beac-44e9516bf795
// last-edited: 2026-11-10

package tasks_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
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
