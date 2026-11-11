// file: core/tasks/taskstest/parity/cases_items.go
// version: 1.0.1
// guid: 92cfb06e-8600-4325-be67-d4878707e637
// last-edited: 2026-11-10

package parity

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// marks logs the watermark checkpoints (and progress writes) a run stores.
type marks struct {
	mu   sync.Mutex
	name string
	ns   []int
	prog [][2]int
}

// watch starts logging checkpoint writes named name and every progress write.
func watch(k *Kit, name string) *marks {
	m := &marks{name: name}
	k.Hooks().ObserveCheckpoints(func(_ tasks.RunID, n string, v []byte) {
		if n != name {
			return
		}
		var env struct {
			V struct {
				N int `json:"n"`
			} `json:"v"`
		}
		if json.Unmarshal(v, &env) != nil {
			return
		}
		m.mu.Lock()
		m.ns = append(m.ns, env.V.N)
		m.mu.Unlock()
	})
	k.Hooks().ObserveProgress(func(_ tasks.RunID, done, total int, _ string) {
		m.mu.Lock()
		m.prog = append(m.prog, [2]int{done, total})
		m.mu.Unlock()
	})
	return m
}

func (m *marks) max() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Max(append([]int{0}, m.ns...))
}

func (m *marks) writes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ns)
}

func (m *marks) progress() [][2]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.prog)
}

// ran collects the items a loop executed.
type ran struct {
	mu  sync.Mutex
	got []int
}

func (r *ran) add(i int) {
	r.mu.Lock()
	r.got = append(r.got, i)
	r.mu.Unlock()
}

func (r *ran) sorted() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.got)
	slices.Sort(out)
	return out
}

func seq(from, to int) []int {
	var out []int
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}

// loopProbe is a Probe whose body runs tasks.RunItems over 0..n-1 with fn.
func (k *Kit) loopProbe(kind string, n int, par tasks.Parallelism, o tasks.ItemsOptions[int], fn func(i int) error) (*Probe, *ran) {
	r := &ran{}
	p := k.Probe(kind).Body(func(_ *Probe, rc *tasks.RC, _ Params) (string, error) {
		_, err := tasks.RunItems(rc, par, seq(0, n), func(_ *tasks.ItemRC, i int) error {
			r.add(i)
			return fn(i)
		}, o)
		return "patrolled", err
	})
	return p, r
}

func ok(int) error { return nil }

// resumeAt plants a running ResumeContinue run whose watermark checkpoint
// says n items are done, then boots (the resume sweep reruns it).
func resumeAt(k *Kit, p *Probe, name string, n int) tasks.RunID {
	id := k.Plant(p.Kind, tasks.StateRunning)
	k.PlantCheckpoint(id, name, `{"n":`+itoa(n)+`}`)
	k.Boot(Defs(p.Def()))
	return id
}

func itoa(n int) string { return strconv.Itoa(n) }

func itemsCases() []Case {
	const f = "run_items_resume_test.go:"
	par4 := tasks.Fixed(4, "patrol rounds are independent")
	return []Case{
		{
			Name: "ResumeSkipsExactlyThePrefix", Head: f + "TestRunItems_ResumeFrom_SkipsExactlyThePrefix",
			Setup:    "a running run over items 0..9 whose watermark checkpoint records 4 done",
			Action:   "boot; the resumed attempt runs the loop",
			Expected: "the body is called for exactly items 4..9 and the run succeeds",
			Run: func(t *testing.T, k *Kit) {
				p, r := k.loopProbe("gotham.rounds", 10, par4, tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1}, ok)
				id := resumeAt(k, p, "rounds", 4)
				k.WaitState(id, tasks.StateSucceeded)
				if got := r.sorted(); !slices.Equal(got, seq(4, 10)) {
					t.Errorf("items run = %v, want 4..9", got)
				}
			},
		},
		{
			Name: "ResumeKeepsAbsoluteProgress", Head: f + "TestRunItems_ResumeFrom_KeepsAbsoluteProgress",
			Setup:    "100 items, watermark 60",
			Action:   "boot and let the resumed run finish",
			Expected: "progress ends at 100 of 100 and the first progress this attempt reports is above 60",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				p, _ := k.loopProbe("gotham.rounds", 100, par4, tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1}, ok)
				id := resumeAt(k, p, "rounds", 60)
				r := k.WaitState(id, tasks.StateSucceeded)
				prog := m.progress()
				if len(prog) == 0 {
					t.Fatal("no progress written")
				}
				if first := prog[0]; first[0] <= 60 {
					t.Errorf("first progress done = %d, want > 60", first[0])
				}
				if r.Done != 100 || r.Total != 100 {
					t.Errorf("final progress = %d of %d, want 100 of 100", r.Done, r.Total)
				}
			},
		},
		{
			Name: "ResumeBeyondTheEndRunsNothing", Head: f + "TestRunItems_ResumeFrom_BeyondEndRunsNothing",
			Setup:    "3 items, watermark 3",
			Action:   "boot",
			Expected: "no item runs and the run succeeds",
			Run: func(t *testing.T, k *Kit) {
				p, r := k.loopProbe("gotham.rounds", 3, par4, tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1}, ok)
				id := resumeAt(k, p, "rounds", 3)
				k.WaitState(id, tasks.StateSucceeded)
				if got := r.sorted(); len(got) != 0 {
					t.Errorf("items run = %v, want none", got)
				}
			},
		},
		{
			Name: "WatermarkStopsAtAGap", Head: f + "TestRunItems_Watermark_StopsAtAGap",
			Setup:    "8 items, 8 workers; item 2 blocks until items 3..7 finished",
			Action:   "run; sample the highest watermark while item 2 is still in flight",
			Expected: "no watermark above 2 during the gap; the final watermark is 8",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				var others atomic.Int32
				gate := make(chan struct{})
				var gapMax atomic.Int32
				p, _ := k.loopProbe("gotham.rounds", 8, tasks.Fixed(8, "independent rounds"), tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1}, func(i int) error {
					if i != 2 {
						if i > 2 && others.Add(1) == 5 {
							close(gate)
						}
						return nil
					}
					<-gate
					time.Sleep(100 * time.Millisecond)
					gapMax.Store(int32(m.max()))
					return nil
				})
				k.Boot(Defs(p.Def()))
				id := k.Start(p.Kind, nil)
				k.WaitState(id, tasks.StateSucceeded)
				if g := gapMax.Load(); g > 2 {
					t.Errorf("watermark %d claimed past the gap at item 2", g)
				}
				if got := m.max(); got != 8 {
					t.Errorf("final watermark = %d, want 8", got)
				}
			},
		},
		{
			Name: "FailedItemDoesNotAdvanceTheWatermark", Head: f + "TestRunItems_Watermark_FailedItemDoesNotAdvance",
			Setup:    "6 items; item 3 fails; errors are collected",
			Action:   "run",
			Expected: "the run fails and the watermark never passes 3",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				p, _ := k.loopProbe("gotham.rounds", 6, tasks.Fixed(3, "independent rounds"), tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1, OnError: tasks.Collect}, func(i int) error {
					if i == 3 {
						return errors.New("the Joker got away")
					}
					return nil
				})
				k.Boot(Defs(p.Def()))
				id := k.Start(p.Kind, nil)
				k.WaitState(id, tasks.StateFailed)
				if got := m.max(); got != 3 {
					t.Errorf("watermark max = %d, want exactly 3", got)
				}
			},
		},
		{
			Name: "WatermarkReachesTheTotal", Head: f + "TestRunItems_Watermark_AllCompleteReachesTotal",
			Setup:    "64 items, 8 workers, all succeed",
			Action:   "run",
			Expected: "the watermark reaches 64",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				p, _ := k.loopProbe("gotham.rounds", 64, tasks.Fixed(8, "independent rounds"), tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1}, ok)
				k.Boot(Defs(p.Def()))
				k.WaitState(k.Start(p.Kind, nil), tasks.StateSucceeded)
				if got := m.max(); got != 64 {
					t.Errorf("watermark max = %d, want 64", got)
				}
			},
		},
		{
			Name: "WatermarkIsAbsoluteAcrossResume", Head: f + "TestRunItems_Watermark_IsAbsoluteAcrossResume",
			Setup:    "50 items, watermark 20",
			Action:   "boot",
			Expected: "the watermark written ends at 50 (absolute, reusable as the next resume point)",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				p, _ := k.loopProbe("gotham.rounds", 50, par4, tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 1}, ok)
				id := resumeAt(k, p, "rounds", 20)
				k.WaitState(id, tasks.StateSucceeded)
				if got := m.max(); got != 50 {
					t.Errorf("watermark max = %d, want 50", got)
				}
			},
		},
		{
			Name: "CheckpointEveryThrottles", Head: f + "TestRunItems_CheckpointEvery_Throttles",
			Setup:    "100 items, CheckpointEvery 25",
			Action:   "run",
			Expected: "at most 10 checkpoint writes; the last is 100",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				p, _ := k.loopProbe("gotham.rounds", 100, par4, tasks.ItemsOptions[int]{Watermark: "rounds", CheckpointEvery: 25}, ok)
				k.Boot(Defs(p.Def()))
				k.WaitState(k.Start(p.Kind, nil), tasks.StateSucceeded)
				if w := m.writes(); w > 10 || w == 0 {
					t.Errorf("%d checkpoint writes, want 1..10", w)
				}
				if got := m.max(); got != 100 {
					t.Errorf("final watermark = %d, want 100", got)
				}
			},
		},
		{
			Name: "NoWatermarkLeavesTheLoopUnchanged", Head: f + "TestRunItems_NoCheckpointFn_Unchanged",
			Setup:    "4 items and no Watermark option",
			Action:   "run",
			Expected: "all 4 items run, progress ends 4 of 4, and no watermark is written",
			Run: func(t *testing.T, k *Kit) {
				m := watch(k, "rounds")
				p, r := k.loopProbe("gotham.rounds", 4, tasks.Fixed(2, "independent rounds"), tasks.ItemsOptions[int]{}, ok)
				k.Boot(Defs(p.Def()))
				rec := k.WaitState(k.Start(p.Kind, nil), tasks.StateSucceeded)
				if got := r.sorted(); !slices.Equal(got, seq(0, 4)) {
					t.Errorf("items run = %v, want 0..3", got)
				}
				if rec.Done != 4 || rec.Total != 4 {
					t.Errorf("final progress = %d of %d, want 4 of 4", rec.Done, rec.Total)
				}
				if m.writes() != 0 {
					t.Error("a watermark was written without the option")
				}
			},
		},
		fixerCase(),
	}
}
