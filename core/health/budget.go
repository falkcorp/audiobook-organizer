// file: core/health/budget.go
// version: 1.0.1
// guid: 91c4e7a2-5d38-4f60-8b1e-2a9d6c3f7e84
// last-edited: 2026-11-13

package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"
)

// The stop budget of 05-health §5H.4: one total, core.shutdown.budget, from
// which every shutdown phase carves its deadline, so the phases never add up to
// more than the total (HEAD's phases are unbounded as a sum, up to 45 s).

// Budget defaults (05-health §5H.7, 05-tasks, 05-events).
const (
	// DefaultStopBudget is core.shutdown.budget unless a layer sets it.
	DefaultStopBudget = 25 * time.Second
	// ContainerStopBudget is the default when the process runs in a container
	// and no layer sets the key: Docker's default grace is 10 s (OPS2-23).
	ContainerStopBudget = 8 * time.Second

	defaultHTTPDrain  = 10 * time.Second // core.shutdown.http_drain
	defaultTasksDrain = 10 * time.Second // core.tasks.shutdown_drain
	defaultBusClose   = 2 * time.Second  // core.events.close_drain
)

// DefaultShutdownBudget is core.shutdown.budget: configured when a layer set it
// (non-zero), else 8 s in a container and 25 s elsewhere. container is
// sysinfo.Info.Container; it is a parameter, not read here, because package
// health sits below sysinfo.
func DefaultShutdownBudget(container bool, configured time.Duration) time.Duration {
	switch {
	case configured > 0:
		return configured
	case container:
		return ContainerStopBudget
	}
	return DefaultStopBudget
}

// StopPhase names one phase of the stop order (06 §6.5.2). Its string is the
// "phase" label of shutdown_phase_seconds.
type StopPhase string

// The phases, in stop order.
const (
	PhaseHTTPDrain  StopPhase = "http_drain"
	PhaseTasksDrain StopPhase = "tasks_drain"
	PhaseBusClose   StopPhase = "bus_close"
	PhaseWorkers    StopPhase = "workers"
	PhaseHooks      StopPhase = "hooks"
	PhaseRealtime   StopPhase = "realtime"
	PhaseStorage    StopPhase = "storage_close"
)

// StopLimits are the per-phase caps; a zero field takes the key's default.
type StopLimits struct {
	HTTPDrain  time.Duration // core.shutdown.http_drain, 10s
	TasksDrain time.Duration // core.tasks.shutdown_drain, 10s
	BusClose   time.Duration // core.events.close_drain, 2s
}

// ErrPhaseAbandoned is wrapped by the error StopBudget.Run returns when a
// bounded phase did not return by its deadline and was left running. The
// error also wraps the context's own error (context.DeadlineExceeded for a
// budget deadline), so a caller that only asks errors.Is(err,
// context.DeadlineExceeded) keeps working.
var ErrPhaseAbandoned = errors.New("health: stop phase abandoned at its deadline")

// StopBudget carves phase deadlines from one total. It is used by one
// goroutine, the shutdown path, one phase at a time.
type StopBudget struct {
	now      func() time.Time
	deadline time.Time
	limits   StopLimits
	m        *Metrics
	log      *slog.Logger
	// remainderLeft counts the remainder phases (workers, hooks, realtime) not
	// yet run; each gets an equal share of what is left.
	remainderLeft int
}

// NewStopBudget starts a budget of total now. total <= 0 means
// DefaultStopBudget. m may be nil.
func NewStopBudget(total time.Duration, l StopLimits, m *Metrics) *StopBudget {
	return newStopBudget(time.Now, total, l, m)
}

func newStopBudget(now func() time.Time, total time.Duration, l StopLimits, m *Metrics) *StopBudget {
	if total <= 0 {
		total = DefaultStopBudget
	}
	if l.HTTPDrain <= 0 {
		l.HTTPDrain = defaultHTTPDrain
	}
	if l.TasksDrain <= 0 {
		l.TasksDrain = defaultTasksDrain
	}
	if l.BusClose <= 0 {
		l.BusClose = defaultBusClose
	}
	return &StopBudget{now: now, deadline: now().Add(total), limits: l, m: m, remainderLeft: 3}
}

// WithLog sets the logger that records an abandoned phase and returns b.
func (b *StopBudget) WithLog(l *slog.Logger) *StopBudget {
	b.log = l
	return b
}

// Remaining is the time left of the total, never negative.
func (b *StopBudget) Remaining() time.Duration {
	if d := b.deadline.Sub(b.now()); d > 0 {
		return d
	}
	return 0
}

// slice is the time phase may take now, and whether it has a deadline at all.
func (b *StopBudget) slice(phase StopPhase) (d time.Duration, bounded bool, err error) {
	rem := b.Remaining()
	switch phase {
	case PhaseHTTPDrain:
		return min(b.limits.HTTPDrain, rem), true, nil
	case PhaseTasksDrain:
		return min(b.limits.TasksDrain, rem), true, nil
	case PhaseBusClose:
		return min(b.limits.BusClose, rem), true, nil
	case PhaseWorkers, PhaseHooks, PhaseRealtime:
		left := max(b.remainderLeft, 1)
		b.remainderLeft = left - 1
		return rem / time.Duration(left), true, nil
	case PhaseStorage:
		return 0, false, nil
	}
	return 0, false, fmt.Errorf("health: unknown stop phase %q", phase)
}

// Run runs fn as phase under its slice of the budget: a context that ends at
// min(the phase's cap, what remains), or for the remainder phases an equal
// share of what remains. A phase reached with nothing left runs with an
// expired context, so it can still do work that needs none.
//
// The deadline is a bound on how long Run takes, not only a signal to fn: a
// bounded phase that has not returned when its context ends is abandoned (the
// goroutine is left to finish or die with the process, as a health check that
// overruns its timeout is, 05-health 5H.9) and Run returns an error wrapping
// ErrPhaseAbandoned and the context's error. Without that a phase that ignores
// its context, a drain stuck in a syscall, would carry the whole shutdown past
// core.shutdown.budget and the supervisor's SIGKILL would land before the
// storage close. An abandoned phase's context is cancelled for good when Run
// returns.
//
// PhaseStorage is the exception: it runs synchronously, after the cap if need
// be, with a context the budget never cancels, and is never abandoned (Pebble's
// WAL keeps committed batches; a skipped close would not). The phase's
// duration, abandoned or not, is recorded as shutdown_phase_seconds.
func (b *StopBudget) Run(ctx context.Context, phase StopPhase, fn func(context.Context) error) error {
	d, bounded, err := b.slice(phase)
	if err != nil {
		return err
	}
	start := b.now()
	if !bounded {
		ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		err = fn(ctx)
		b.m.recordPhase(string(phase), b.now().Sub(start))
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("health: stop phase %q panicked: %v", phase, p)
			}
		}()
		done <- fn(ctx)
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		// A phase that honours its context returns at the same instant the
		// deadline fires; give it one scheduling turn so that is usually not
		// reported as an abandonment. Best effort: one that has not been
		// scheduled by then is abandoned, which costs a log line and nothing
		// else, since its error would have been the context's anyway.
		runtime.Gosched()
		select {
		case err = <-done:
		default:
			took := b.now().Sub(start)
			b.m.recordPhase(string(phase), took)
			if b.log != nil {
				b.log.Warn("health: stop phase did not return by its deadline and was abandoned",
					"phase", string(phase), "budget", d.String(), "took", took.String())
			}
			return fmt.Errorf("%w: %s after %s: %w", ErrPhaseAbandoned, phase, took, ctx.Err())
		}
	}
	b.m.recordPhase(string(phase), b.now().Sub(start))
	return err
}
