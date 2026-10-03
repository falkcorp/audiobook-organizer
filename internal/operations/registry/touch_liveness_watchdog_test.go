// file: internal/operations/registry/touch_liveness_watchdog_test.go
// version: 1.1.0
// guid: 71cc2550-6e84-41cd-b891-7bacda5384db
// last-edited: 2026-10-03

package registry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// stepClock is a liveness clock the test advances by hand. Atomic because the
// op's worker goroutine stamps it while the test goroutine advances it.
type stepClock struct{ ns atomic.Int64 }

func newStepClock() *stepClock {
	c := &stepClock{}
	c.ns.Store(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *stepClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *stepClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// newSteppedRegistry returns a started registry whose watchdog runs only when
// the test calls WatchdogCycleForTest, measuring idle time on clk.
func newSteppedRegistry(store *fakeStore, clk *stepClock) *registry.Registry {
	return registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: time.Hour, // the ticker never fires during a test
		LivenessNow:      clk.Now,
	})
}

// recv waits for one value on ch; the deadline only bounds a broken test.
func recv(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// An op whose ONLY liveness signal is TouchLiveness (no UpdateProgress at all)
// runs well past its ProgressTimeout under the real watchdog without a strike.
//
// Time is stepped, not slept. Until 2026-10-03 the op touched every 20ms of
// real time against a 100ms ProgressTimeout and a 20ms watchdog ticker; under
// -race on a shared CI host one 20ms sleep overran 100ms and the watchdog
// struck a healthy op (Woodpecker pipeline 287). Now each round the op
// touches, the test advances the liveness clock by less than the timeout and
// runs one watchdog pass, so the margin is exact whatever the scheduler does.
func TestWatchdog_TouchLivenessAloneKeepsOpAlive(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	clk := newStepClock()
	r := newSteppedRegistry(store, clk)

	const (
		timeout = 100 * time.Millisecond
		step    = 60 * time.Millisecond // < timeout per round
		rounds  = 10                    // 600ms in all: 6x the timeout
	)
	touched := make(chan struct{})
	proceed := make(chan struct{})
	runDone := make(chan struct{}) // closed when Run returns, struck or not
	def := makeValidDef("test.wdog-touch-alive")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = timeout
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		defer close(runDone)
		for range rounds {
			registry.TouchLiveness(rep)
			select {
			case touched <- struct{}{}:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			select {
			case <-proceed:
			case <-runCtx.Done():
				return runCtx.Err()
			}
		}
		return nil
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-touch-alive", nil)

	for i := range rounds {
		recv(t, touched, fmt.Sprintf("touch %d", i))
		clk.Advance(step)
		r.WatchdogCycleForTest()
		select {
		case proceed <- struct{}{}:
		case <-runDone:
			t.Fatalf("op stopped in round %d: the watchdog struck it %s after its last touch (timeout %s)", i, step, timeout)
		}
	}

	awaitStatus(t, store, opID, "completed", 10*time.Second)
	for _, kind := range []string{"stuck", "never_reported"} {
		if n := len(store.strikesOfKind(opID, kind)); n != 0 {
			t.Errorf("%d %s strikes on an op that touched liveness every %s of a %s timeout", n, kind, step, timeout)
		}
	}
}

// The inverse, on the same stepped clock: one touch, then silence past the
// timeout. The watchdog strikes it as stuck (it did report once) and cancels
// it. This is also the control for the test above: it proves a stepped
// WatchdogCycleForTest pass really evaluates the op, so zero strikes there is
// not a pass that never looked.
func TestWatchdog_SilentAfterTouchIsStruck(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	clk := newStepClock()
	r := newSteppedRegistry(store, clk)

	touched := make(chan struct{})
	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-touch-silent")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		registry.TouchLiveness(rep)
		close(touched)
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-touch-silent", nil)

	recv(t, touched, "the single touch")
	clk.Advance(150 * time.Millisecond)
	r.WatchdogCycleForTest()
	recv(t, canceled, "the watchdog to cancel the silent op")
	awaitStatus(t, store, opID, "canceled", 10*time.Second)
	if n := len(store.strikesOfKind(opID, "stuck")); n == 0 {
		t.Error("expected a stuck strike, got none")
	}
}
