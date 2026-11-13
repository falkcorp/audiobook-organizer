// file: core/health/budget_test.go
// version: 1.0.2
// guid: 2b8f4d6e-9c13-4a57-8e0d-1f7c3a5b9d28
// last-edited: 2026-11-13

package health

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestStopBudget_ContainerDefault8s: the total is 25 s, 8 s in a container when
// no layer sets the key, and a configured value always wins.
func TestStopBudget_ContainerDefault8s(t *testing.T) {
	cases := []struct {
		container  bool
		configured time.Duration
		want       time.Duration
	}{
		{false, 0, 25 * time.Second},
		{true, 0, 8 * time.Second},
		{true, 25 * time.Second, 25 * time.Second}, // set explicitly to the default: still wins
		{true, 40 * time.Second, 40 * time.Second},
		{false, 12 * time.Second, 12 * time.Second},
	}
	for _, c := range cases {
		if got := DefaultShutdownBudget(c.container, c.configured); got != c.want {
			t.Errorf("DefaultShutdownBudget(%v, %s) = %s, want %s", c.container, c.configured, got, c.want)
		}
	}
	// An unset key reaches NewStopBudget as the container default.
	synctest.Test(t, func(t *testing.T) {
		b := NewStopBudget(DefaultShutdownBudget(true, 0), StopLimits{}, nil)
		if got := b.Remaining(); got != 8*time.Second {
			t.Errorf("a container budget starts with %s, want 8s", got)
		}
	})
}

// runAllPhases runs every phase in stop order, each blocking until its context
// ends, and returns how long each took.
func runAllPhases(t *testing.T, b *StopBudget, early map[StopPhase]bool) map[StopPhase]time.Duration {
	t.Helper()
	took := map[StopPhase]time.Duration{}
	for _, p := range []StopPhase{PhaseHTTPDrain, PhaseTasksDrain, PhaseBusClose, PhaseWorkers, PhaseHooks, PhaseRealtime, PhaseStorage} {
		start := time.Now()
		err := b.Run(context.Background(), p, func(ctx context.Context) error {
			if early[p] || p == PhaseStorage {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: %v", p, err)
		}
		took[p] = time.Since(start)
	}
	return took
}

// TestStopBudget_PhasesNeverExceedTotal: with every phase using all it is given,
// the phases take exactly their caps and the sum never exceeds the total; a
// small total starves the late phases, never the storage close.
func TestStopBudget_PhasesNeverExceedTotal(t *testing.T) {
	t.Run("25s", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			b := NewStopBudget(25*time.Second, StopLimits{}, nil)
			took := runAllPhases(t, b, nil)
			want := map[StopPhase]time.Duration{
				PhaseHTTPDrain: 10 * time.Second, PhaseTasksDrain: 10 * time.Second, PhaseBusClose: 2 * time.Second,
				PhaseWorkers: time.Second, PhaseHooks: time.Second, PhaseRealtime: time.Second, PhaseStorage: 0,
			}
			for p, w := range want {
				if took[p] != w {
					t.Errorf("%s took %s, want %s", p, took[p], w)
				}
			}
			if total := time.Since(start); total > 25*time.Second {
				t.Errorf("phases took %s in all, over the 25s total", total)
			}
		})
	})
	t.Run("8s container total", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			b := NewStopBudget(8*time.Second, StopLimits{}, nil)
			storageRan := false
			took := map[StopPhase]time.Duration{}
			for _, p := range []StopPhase{PhaseHTTPDrain, PhaseTasksDrain, PhaseBusClose, PhaseWorkers, PhaseHooks, PhaseRealtime, PhaseStorage} {
				s0 := time.Now()
				_ = b.Run(context.Background(), p, func(ctx context.Context) error {
					if p == PhaseStorage {
						storageRan = ctx.Err() == nil // never cancelled by the budget
						return nil
					}
					<-ctx.Done()
					return nil
				})
				took[p] = time.Since(s0)
			}
			if took[PhaseHTTPDrain] != 8*time.Second {
				t.Errorf("http drain took %s, want min(10s, remaining) = 8s", took[PhaseHTTPDrain])
			}
			for _, p := range []StopPhase{PhaseTasksDrain, PhaseBusClose, PhaseWorkers, PhaseHooks, PhaseRealtime} {
				if took[p] != 0 {
					t.Errorf("%s took %s with nothing left, want 0", p, took[p])
				}
			}
			if !storageRan {
				t.Error("the storage close did not run with a live context after the cap")
			}
			if total := time.Since(start); total != 8*time.Second {
				t.Errorf("total = %s, want exactly the 8s budget", total)
			}
		})
	})
	t.Run("early finishers donate their time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := NewStopBudget(25*time.Second, StopLimits{}, nil)
			took := runAllPhases(t, b, map[StopPhase]bool{PhaseHTTPDrain: true, PhaseTasksDrain: true, PhaseBusClose: true, PhaseWorkers: true})
			// 25s remain for three remainder phases; workers returns at once, so
			// hooks and realtime split it: 12.5s each (an equal share of what
			// is left, in order).
			if took[PhaseHooks] != 12500*time.Millisecond || took[PhaseRealtime] != 12500*time.Millisecond {
				t.Errorf("hooks %s realtime %s, want 12.5s each", took[PhaseHooks], took[PhaseRealtime])
			}
		})
	})
}

// TestStopBudget_UnknownPhaseAndRecording: an unknown phase is an error that
// runs nothing, and a phase's error comes back unchanged.
func TestStopBudget_UnknownPhaseAndRecording(t *testing.T) {
	b := NewStopBudget(time.Second, StopLimits{}, nil)
	ran := false
	if err := b.Run(t.Context(), "nap", func(context.Context) error { ran = true; return nil }); err == nil || ran {
		t.Errorf("unknown phase: err=%v ran=%v", err, ran)
	}
	boom := errors.New("batmobile stuck")
	if err := b.Run(t.Context(), PhaseHooks, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("phase error = %v, want %v", err, boom)
	}
}

// TestStopBudget_PhaseIgnoringContextIsAbandoned: a phase that never looks at its
// context (a drain stuck in a syscall) must not carry the shutdown past the
// total. Six 15 s sleeps on a 25 s budget used to take 1m30s; now every bounded
// phase returns at its deadline, the sum stays inside the budget, and the storage
// close still runs, synchronously, with a live context.
func TestStopBudget_PhaseIgnoringContextIsAbandoned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var logs bytes.Buffer
		b := NewStopBudget(25*time.Second, StopLimits{}, nil).WithLog(slog.New(slog.NewTextHandler(&logs, nil)))
		took := map[StopPhase]time.Duration{}
		abandoned := 0
		storageRan := false
		for _, p := range []StopPhase{PhaseHTTPDrain, PhaseTasksDrain, PhaseBusClose, PhaseWorkers, PhaseHooks, PhaseRealtime, PhaseStorage} {
			s0 := time.Now()
			err := b.Run(context.Background(), p, func(ctx context.Context) error {
				if p == PhaseStorage {
					storageRan = ctx.Err() == nil
					return nil
				}
				time.Sleep(15 * time.Second) // ignores ctx entirely
				return nil
			})
			took[p] = time.Since(s0)
			if p == PhaseStorage {
				if err != nil {
					t.Errorf("storage close: %v", err)
				}
				continue
			}
			if !errors.Is(err, ErrPhaseAbandoned) || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: err = %v, want ErrPhaseAbandoned wrapping DeadlineExceeded", p, err)
			}
			abandoned++
		}
		// Each abandoned phase takes its slice plus the grace; the grace of one
		// phase is time the later remainder phases no longer have, so only the
		// capped phases keep their caps.
		total := time.Since(start)
		if total < 25*time.Second || total > 25*time.Second+6*abandonGrace {
			t.Errorf("phases took %s in all, want the 25s total plus at most 6 x %s", total, abandonGrace)
		}
		for p, w := range map[StopPhase]time.Duration{PhaseHTTPDrain: 10 * time.Second, PhaseTasksDrain: 10 * time.Second, PhaseBusClose: 2 * time.Second} {
			if took[p] != w+abandonGrace {
				t.Errorf("%s took %s, want its %s cap plus the %s grace", p, took[p], w, abandonGrace)
			}
		}
		for _, p := range []StopPhase{PhaseWorkers, PhaseHooks, PhaseRealtime} {
			if took[p] <= abandonGrace || took[p] > time.Second+abandonGrace {
				t.Errorf("%s took %s, want a share of what is left (over the grace, at most 1s) plus the grace", p, took[p])
			}
		}
		if abandoned != 6 {
			t.Errorf("%d phases abandoned, want 6", abandoned)
		}
		if !storageRan {
			t.Error("the storage close did not run with a live context after six abandoned phases")
		}
		if n := strings.Count(logs.String(), "abandoned"); n != 6 {
			t.Errorf("%d abandonment log lines, want 6:\n%s", n, logs.String())
		}
		time.Sleep(time.Minute) // let the abandoned phases end before the bubble does
	})
}

// TestStopBudget_StoragePhaseIsNeverAbandoned: the storage close waits for its
// function however long it takes; abandoning it would skip the WAL sync.
func TestStopBudget_StoragePhaseIsNeverAbandoned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := NewStopBudget(time.Second, StopLimits{}, nil)
		_ = b.Run(context.Background(), PhaseHTTPDrain, func(ctx context.Context) error { <-ctx.Done(); return nil })
		finished := false
		err := b.Run(context.Background(), PhaseStorage, func(context.Context) error {
			time.Sleep(time.Minute)
			finished = true
			return nil
		})
		if err != nil || !finished {
			t.Errorf("storage close: err=%v finished=%v, want it waited for", err, finished)
		}
	})
}

// TestStopBudget_PhaseThatReturnsInTimeIsNotAbandoned: a phase that returns
// before its deadline is a normal return with its own result, and one that
// returns what its context says at the deadline reports the deadline, never an
// abandonment. The grace makes the boundary exact: a phase that is up to
// abandonGrace late still returns its own result, one later than that is
// abandoned.
func TestStopBudget_PhaseThatReturnsInTimeIsNotAbandoned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := NewStopBudget(10*time.Second, StopLimits{HTTPDrain: time.Second}, nil)
		boom := errors.New("batmobile stuck")
		err := b.Run(context.Background(), PhaseHTTPDrain, func(context.Context) error {
			time.Sleep(500 * time.Millisecond)
			return boom
		})
		if !errors.Is(err, boom) || errors.Is(err, ErrPhaseAbandoned) {
			t.Errorf("a phase that returned in time: err = %v, want its own error and no abandonment", err)
		}
		err = b.Run(context.Background(), PhaseTasksDrain, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrPhaseAbandoned) {
			t.Errorf("a phase that honoured its deadline: err = %v, want DeadlineExceeded and no abandonment", err)
		}

		late := func(phase StopPhase, after time.Duration) error {
			b := NewStopBudget(10*time.Second, StopLimits{BusClose: time.Second}, nil)
			return b.Run(context.Background(), phase, func(context.Context) error {
				time.Sleep(time.Second + after) // ignores ctx, returns after the deadline
				return boom
			})
		}
		if err := late(PhaseBusClose, abandonGrace/2); !errors.Is(err, boom) || errors.Is(err, ErrPhaseAbandoned) {
			t.Errorf("a phase %s past its deadline: err = %v, want its own error inside the grace", abandonGrace/2, err)
		}
		if err := late(PhaseBusClose, 2*abandonGrace); !errors.Is(err, ErrPhaseAbandoned) {
			t.Errorf("a phase %s past its deadline: err = %v, want abandoned after the grace", 2*abandonGrace, err)
		}
		time.Sleep(time.Minute) // let the abandoned phase end before the bubble does
	})
}

// TestStopBudget_PanickingPhaseIsAnError: a phase runs on its own goroutine now,
// so a panic must come back as the phase's error, not kill the shutdown path.
func TestStopBudget_PanickingPhaseIsAnError(t *testing.T) {
	b := NewStopBudget(time.Second, StopLimits{}, nil)
	err := b.Run(t.Context(), PhaseHooks, func(context.Context) error { panic("joker laughed") })
	if err == nil || !strings.Contains(err.Error(), "joker laughed") {
		t.Errorf("err = %v, want the panic value in the phase error", err)
	}
}
