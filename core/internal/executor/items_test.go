// file: core/internal/executor/items_test.go
// version: 1.0.0
// guid: 89ac7313-f721-481c-86f4-0b5c7234a963
// last-edited: 2026-11-10

package executor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// gauge tracks how many holders are inside at once.
type gauge struct {
	mu        sync.Mutex
	cur, high int
}

func (g *gauge) in() {
	g.mu.Lock()
	g.cur++
	g.high = max(g.high, g.cur)
	g.mu.Unlock()
}

func (g *gauge) out() {
	g.mu.Lock()
	g.cur--
	g.mu.Unlock()
}

func (g *gauge) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.high
}

func count(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// seen records items in order.
type seen struct {
	mu  sync.Mutex
	ids []int
}

func (s *seen) add(i int) {
	s.mu.Lock()
	s.ids = append(s.ids, i)
	s.mu.Unlock()
}

func (s *seen) sorted() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.ids)
	slices.Sort(out)
	return out
}

// TestRunItems_WatermarkResume (5T.3.2): the contiguous-completion
// watermark is checkpointed through the run and restored on resume; the
// resumed attempt starts at the watermark, not at item 0.
func TestRunItems_WatermarkResume(t *testing.T) {
	blocked := make(chan struct{}, 1)
	first, second := &seen{}, &seen{}
	body := func(log *seen, blockAt int) func(rc *tasks.RC, _ noParams) (string, error) {
		return func(rc *tasks.RC, _ noParams) (string, error) {
			res, err := tasks.RunItems(rc, tasks.Sequential("rooftops in order"), count(10), func(ic *tasks.ItemRC, i int) error {
				if i == blockAt {
					blocked <- struct{}{}
					<-ic.Context().Done()
					return context.Cause(ic.Context())
				}
				log.add(i)
				return nil
			}, tasks.ItemsOptions[int]{Watermark: "rooftops", CheckpointEvery: 1})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("watermark %d", res.Watermark), nil
		}
	}
	h := newHarness(t, []tasks.Definition{taskDef("gotham.rooftops", body(first, 5))}, nil)
	id := h.start("gotham.rooftops", tasks.StartRequest{})
	<-blocked
	if got := first.sorted(); !slices.Equal(got, count(5)) {
		t.Fatalf("first attempt ran %v, want 0..4", got)
	}
	next := h.crashBoot([]tasks.Definition{taskDef("gotham.rooftops", body(second, -1))}, nil)
	r := next.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
	if r.State != tasks.StateSucceeded || r.Attempt != 2 {
		t.Fatalf("resumed run: %s attempt %d (%q)", r.State, r.Attempt, r.Error)
	}
	if got := second.sorted(); !slices.Equal(got, []int{5, 6, 7, 8, 9}) {
		t.Fatalf("resumed attempt ran %v, want 5..9 (from the watermark)", got)
	}
	if res := resultOf(t, r); res != "watermark 10" {
		t.Fatalf("result %q, want watermark 10", res)
	}
}

// TestRunItems_CPUBudgetShared (5T.3.2): CPU() workers of every run share
// core.tasks.cpu_slots; two runs never hold more slots than the budget.
func TestRunItems_CPUBudgetShared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &gauge{}
		body := func(rc *tasks.RC, _ noParams) (string, error) {
			_, err := tasks.RunItems(rc, tasks.CPU(), count(6), func(ic *tasks.ItemRC, _ int) error {
				g.in()
				defer g.out()
				time.Sleep(time.Second)
				return nil
			})
			return "", err
		}
		h := newHarness(t, []tasks.Definition{taskDef("gotham.forensics", body), taskDef("arkham.forensics", body)}, func(o *Options) {
			o.Config = Config{CPUSlots: 2, DispatchTick: time.Second, WatchdogInterval: time.Hour}
		})
		a := h.start("gotham.forensics", tasks.StartRequest{})
		b := h.start("arkham.forensics", tasks.StartRequest{})
		h.waitState(a, tasks.StateSucceeded)
		h.waitState(b, tasks.StateSucceeded)
		if got := g.peak(); got != 2 {
			t.Fatalf("peak CPU items across both runs %d, want 2 (cpu_slots)", got)
		}
	})
}

// TestParallelismCap_LowersDeclared (5T.10 core.tasks.kinds[k].parallelism_cap):
// the dial lowers a declared worker count and never raises it. Driven
// through Options here; the config-file and JSON-env cells are F3-01's
// (core config does not reach the executor until then).
func TestParallelismCap_LowersDeclared(t *testing.T) {
	for _, c := range []struct {
		name string
		cap  int
		want int
	}{{"no dial", 0, 6}, {"dial lowers", 2, 2}, {"dial above declared", 10, 6}} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := &gauge{}
				def := taskDef("gotham.sweep", func(rc *tasks.RC, _ noParams) (string, error) {
					_, err := tasks.RunItems(rc, tasks.Fixed(6, "six squads"), count(12), func(ic *tasks.ItemRC, _ int) error {
						g.in()
						defer g.out()
						time.Sleep(time.Second)
						return nil
					})
					return "", err
				})
				h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
					o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
					if c.cap > 0 {
						o.Config.Kinds = map[tasks.Kind]tasks.KindDial{"gotham.sweep": {ParallelismCap: c.cap}}
					}
				})
				h.waitState(h.start("gotham.sweep", tasks.StartRequest{}), tasks.StateSucceeded)
				if got := g.peak(); got != c.want {
					t.Fatalf("peak workers %d, want %d", got, c.want)
				}
			})
		})
	}
}

// TestRunItems_PauseParksBeforeItemClock (5T.3.2, synctest): a paused
// executor parks a loop's next item at the pause gate, before its
// ItemTimeout clock starts; time spent paused never times an item out.
func TestRunItems_PauseParksBeforeItemClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		running := make(chan int, 3)
		var started seen
		def := taskDef("gotham.lineup", func(rc *tasks.RC, _ noParams) (string, error) {
			res, err := tasks.RunItems(rc, tasks.Sequential("one suspect at a time"), count(3), func(ic *tasks.ItemRC, i int) error {
				started.add(i)
				running <- i
				select {
				case <-time.After(5 * time.Second):
					return nil
				case <-ic.Context().Done():
					return context.Cause(ic.Context())
				}
			}, tasks.ItemsOptions[int]{ItemTimeout: 10 * time.Second, OnError: tasks.Collect})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%d done %d failed", res.Done, res.Failed), nil
		}, withTimeout(2*time.Hour))
		h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
			o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
		})
		id := h.start("gotham.lineup", tasks.StartRequest{})
		<-running
		if err := h.x.Pause(context.Background(), "commissioner's briefing"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Minute) // far past ItemTimeout; inside the run's timeout and progress timeout
		synctest.Wait()
		if got := started.sorted(); !slices.Equal(got, []int{0}) {
			t.Fatalf("items started while paused: %v, want only [0]", got)
		}
		if err := h.x.Unpause(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
		if res := resultOf(t, r); res != "3 done 0 failed" {
			t.Fatalf("result %q, want 3 done 0 failed (no item clock ran while paused)", res)
		}
	})
}

// TestForEach_JoinsRunBudgetWhenCtxFromRC (5T.3.2, COR2-16): ForEach with a
// ctx that descends from a run takes that executor's CPU slots; with a plain
// ctx it runs on its own NumCPU workers.
func TestForEach_JoinsRunBudgetWhenCtxFromRC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		joined, plain := &gauge{}, &gauge{}
		work := func(g *gauge) func(context.Context, int) error {
			return func(context.Context, int) error {
				g.in()
				defer g.out()
				time.Sleep(time.Second)
				return nil
			}
		}
		def := taskDef("gotham.casefiles", func(rc *tasks.RC, _ noParams) (string, error) {
			if _, err := tasks.ForEach(context.WithValue(rc.Context(), callerKey{}, "oracle"), tasks.CPU(), count(4), work(joined)); err != nil {
				return "", err
			}
			_, err := tasks.ForEach(context.Background(), tasks.CPU(), count(4), work(plain))
			return "", err
		})
		h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
			o.Config = Config{CPUSlots: 1, DispatchTick: time.Second, WatchdogInterval: time.Hour}
		})
		h.waitState(h.start("gotham.casefiles", tasks.StartRequest{}), tasks.StateSucceeded)
		if got := joined.peak(); got != 1 {
			t.Fatalf("ForEach under the run's ctx peaked at %d, want 1 (cpu_slots)", got)
		}
		if want := min(4, runtime.NumCPU()); plain.peak() != want {
			t.Fatalf("ForEach under a plain ctx peaked at %d, want %d (its own workers)", plain.peak(), want)
		}
	})
}

// stakeouts maps an item to the refs it names.
var stakeouts = map[string][]entity.Ref{
	"tower":  {{Module: "gotham", Kind: "hero", ID: "batman"}},
	"docks":  {{Module: "gotham", Kind: "hero", ID: "batman"}, {Module: "gotham", Kind: "hero", ID: "robin"}},
	"alley":  {{Module: "gotham", Kind: "hero", ID: "robin"}},
	"museum": {{Module: "gotham", Kind: "villain", ID: "catwoman"}},
}

// refTracker fails when two holders of one ref are inside at once.
type refTracker struct {
	mu      sync.Mutex
	held    map[entity.Ref]int
	overlap []string
	g       gauge
}

func (r *refTracker) enter(name string, refs []entity.Ref) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held == nil {
		r.held = map[entity.Ref]int{}
	}
	for _, ref := range refs {
		if r.held[ref] > 0 {
			r.overlap = append(r.overlap, name+" on "+ref.ID)
		}
		r.held[ref]++
	}
	r.g.in()
}

func (r *refTracker) leave(refs []entity.Ref) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ref := range refs {
		r.held[ref]--
	}
	r.g.out()
}

// TestExclusiveRefs_OverlapNeverConcurrent (5T.3.4): items whose refs
// overlap never run at once, within one run and across runs (the
// executor's ref-lock table); disjoint items still run in parallel.
func TestExclusiveRefs_OverlapNeverConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &refTracker{}
		names := []string{"tower", "docks", "alley", "museum"}
		body := func(rc *tasks.RC, _ noParams) (string, error) {
			_, err := tasks.RunItems(rc, tasks.Fixed(4, "four stakeouts"), names, func(ic *tasks.ItemRC, n string) error {
				tr.enter(n, stakeouts[n])
				defer tr.leave(stakeouts[n])
				time.Sleep(time.Second)
				return nil
			}, tasks.ItemsOptions[string]{ExclusiveRefs: func(n string) []entity.Ref { return stakeouts[n] }})
			return "", err
		}
		h := newHarness(t, []tasks.Definition{taskDef("gotham.stakeout", body), taskDef("arkham.stakeout", body)}, func(o *Options) {
			o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
		})
		a := h.start("gotham.stakeout", tasks.StartRequest{})
		b := h.start("arkham.stakeout", tasks.StartRequest{})
		h.waitState(a, tasks.StateSucceeded)
		h.waitState(b, tasks.StateSucceeded)
		if len(tr.overlap) > 0 {
			t.Fatalf("overlapping items ran at once: %v", tr.overlap)
		}
		if tr.g.peak() < 2 {
			t.Fatalf("peak %d: disjoint items never ran in parallel", tr.g.peak())
		}
	})
}

var batman = entity.Ref{Module: "gotham", Kind: "hero", ID: "batman"}
var robin = entity.Ref{Module: "gotham", Kind: "hero", ID: "robin"}

// TestLockRefs_CrossRunSerialises (5T.3.4): a second run's LockRefs on a
// held ref waits for the release, or for the holder's attempt to end (the
// executor releases what an attempt left locked).
func TestLockRefs_CrossRunSerialises(t *testing.T) {
	for _, c := range []struct {
		name    string
		release bool
	}{{"released by the holder", true}, {"released when the holder ends", false}} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				locked := make(chan struct{})
				var acquiredAt time.Time
				t0 := time.Now()
				holder := taskDef("gotham.signal", func(rc *tasks.RC, _ noParams) (string, error) {
					rel, err := rc.LockRefs(batman)
					if err != nil {
						return "", err
					}
					close(locked)
					time.Sleep(5 * time.Second)
					if c.release {
						rel()
						<-rc.Context().Done() // holds the run open; only the lock was released
					}
					return "", nil
				}, withTimeout(2*time.Hour))
				waiter := taskDef("arkham.signal", func(rc *tasks.RC, _ noParams) (string, error) {
					rel, err := rc.LockRefs(batman)
					if err != nil {
						return "", err
					}
					acquiredAt = time.Now()
					rel()
					return "", nil
				})
				h := newHarness(t, []tasks.Definition{holder, waiter}, func(o *Options) {
					o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
				})
				h.start("gotham.signal", tasks.StartRequest{})
				<-locked
				w := h.start("arkham.signal", tasks.StartRequest{})
				h.waitState(w, tasks.StateSucceeded)
				if waited := acquiredAt.Sub(t0); waited < 5*time.Second || waited > 6*time.Second {
					t.Fatalf("second run took the lock after %s, want just after the holder's 5s", waited)
				}
			})
		})
	}
}

// TestLockRefs_SortedNoDeadlock (5T.3.4): two runs locking the same refs in
// opposite orders always finish: the table sorts and takes all or waits.
func TestLockRefs_SortedNoDeadlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := func(refs ...entity.Ref) func(rc *tasks.RC, _ noParams) (string, error) {
			return func(rc *tasks.RC, _ noParams) (string, error) {
				for range 50 {
					rel, err := rc.LockRefs(refs...)
					if err != nil {
						return "", err
					}
					time.Sleep(time.Millisecond)
					rel()
				}
				return "", nil
			}
		}
		h := newHarness(t, []tasks.Definition{taskDef("gotham.patrol", body(batman, robin)), taskDef("arkham.patrol", body(robin, batman))}, func(o *Options) {
			o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
		})
		a := h.start("gotham.patrol", tasks.StartRequest{})
		b := h.start("arkham.patrol", tasks.StartRequest{})
		h.waitState(a, tasks.StateSucceeded)
		h.waitState(b, tasks.StateSucceeded)
	})
}

// TestLockRefs_CtxFromRCJoinsTable (COR2-16): tasks.LockRefs with a ctx that
// descends from a run takes that executor's table (another run waits on
// it), and a second executor's table is its own (R5: nothing shared).
func TestLockRefs_CtxFromRCJoinsTable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locked := make(chan struct{})
		var waitedFor time.Duration
		cfg := func(o *Options) { o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour} }
		holder := taskDef("gotham.signal", func(rc *tasks.RC, _ noParams) (string, error) {
			rel, err := tasks.LockRefs(context.WithValue(rc.Context(), callerKey{}, "deep"), batman)
			if err != nil {
				return "", err
			}
			close(locked)
			time.Sleep(5 * time.Second)
			rel()
			return "", nil
		})
		waiter := func(rc *tasks.RC, _ noParams) (string, error) {
			t0 := time.Now()
			rel, err := rc.LockRefs(batman)
			if err != nil {
				return "", err
			}
			waitedFor = time.Since(t0)
			rel()
			return "", nil
		}
		h := newHarness(t, []tasks.Definition{holder, taskDef("arkham.signal", waiter)}, cfg)
		other := newHarness(t, []tasks.Definition{taskDef("arkham.signal", waiter)}, cfg)
		h.start("gotham.signal", tasks.StartRequest{})
		<-locked
		other.waitState(other.start("arkham.signal", tasks.StartRequest{}), tasks.StateSucceeded)
		if waitedFor != 0 {
			t.Fatalf("a second executor's run waited %s on the first executor's lock", waitedFor)
		}
		h.waitState(h.start("arkham.signal", tasks.StartRequest{}), tasks.StateSucceeded)
		if waitedFor < 4*time.Second {
			t.Fatalf("a run of the same executor waited %s, want the holder's remaining ~5s", waitedFor)
		}
	})
}

// TestLockRefs_PlainCtxRefused (COR2-16): tasks.LockRefs with a ctx that
// does not descend from a run is refused, never a silent no-op.
func TestLockRefs_PlainCtxRefused(t *testing.T) {
	if rel, err := tasks.LockRefs(context.Background(), batman); !errors.Is(err, tasks.ErrNoRunContext) || rel != nil {
		t.Fatalf("LockRefs(plain ctx): release %v, err %v; want nil, ErrNoRunContext", rel != nil, err)
	}
	got := make(chan error, 1)
	def := taskDef("gotham.signal", func(rc *tasks.RC, _ noParams) (string, error) {
		_, err := tasks.LockRefs(context.WithoutCancel(context.Background()), batman)
		got <- err
		return "", nil
	})
	h := newHarness(t, []tasks.Definition{def}, nil)
	h.waitState(h.start("gotham.signal", tasks.StartRequest{}), tasks.StateSucceeded)
	if err := <-got; !errors.Is(err, tasks.ErrNoRunContext) {
		t.Fatalf("LockRefs(plain ctx) inside a run body: %v, want ErrNoRunContext", err)
	}
}
