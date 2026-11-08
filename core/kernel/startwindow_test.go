// file: core/kernel/startwindow_test.go
// version: 1.1.0
// guid: f332f38c-075c-4652-8581-6d592721a1fd
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/internal/sdnotify"
	"github.com/falkcorp/mediacore/module"
)

// stepRecorder is Config.ExtendStart: the service-manager half of
// Deps.ExtendStart, recorded instead of sent.
type stepRecorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *stepRecorder) extend(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
}

func (r *stepRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.steps...)
}

// depsOf collects each module's Deps at Register, so a hook's Start can call
// its own module's ExtendStart, or another module's.
type depsOf struct {
	mu sync.Mutex
	m  map[module.ID]module.Deps
}

func (d *depsOf) put(deps module.Deps) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.m == nil {
		d.m = map[module.ID]module.Deps{}
	}
	d.m[deps.Module] = deps
}

func (d *depsOf) get(id module.ID) module.Deps {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m[id]
}

// startingModule is a module with one hook per start func, named
// <id>.<index>, whose Deps land in deps.
func startingModule(id string, deps *depsOf, starts ...func(ctx context.Context) error) testModule {
	return mod(id, func(r *module.Registrar) error {
		deps.put(r.Deps())
		for i, start := range starts {
			r.Hook(module.Hook{Name: id + "." + string(rune('a'+i)), Start: start})
		}
		return nil
	})
}

// napping waits d on the bubble's clock, or returns ctx's error when the
// window closes first.
func napping(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runStart runs the kernel on the bubble's clock until its start succeeds
// (the notifier saw ready) or fails, and returns how long that took and Run's
// error (nil after a clean stop). It advances the clock a second at a time, so
// every duration the tests assert is a whole number of seconds.
func runStart(t *testing.T, rec *recorder, c Config) (took time.Duration, err error) {
	t.Helper()
	k := mustNew(t, c)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	begin := time.Now()
	type result struct {
		err error
		at  time.Duration
	}
	done := make(chan result, 1)
	go func() {
		e := k.Run(ctx)
		done <- result{e, time.Since(begin)}
	}()
	for {
		synctest.Wait()
		if rec.index("ready") >= 0 {
			took = time.Since(begin)
			cancel()
			return took, (<-done).err
		}
		select {
		case r := <-done:
			return r.at, r.err
		default:
		}
		time.Sleep(time.Second)
	}
}

func TestExtendStart_ExtendsHookStartWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, steps, deps := &recorder{}, &stepRecorder{}, &depsOf{}
		alfred := startingModule("alfred", deps, func(ctx context.Context) error {
			deps.get("alfred").ExtendStart("open the cave")
			return napping(ctx, 5*time.Minute)
		})
		c := supervised(t, rec, alfred)
		c.ExtendStart = steps.extend
		took, err := runStart(t, rec, c)
		if err != nil {
			t.Fatalf("Run = %v: a 5 minute step that extended its start window was abandoned", err)
		}
		if took != 5*time.Minute {
			t.Fatalf("start took %s, want 5m", took)
		}
		if got := steps.list(); len(got) != 1 || got[0] != "open the cave" {
			t.Fatalf("service-manager extensions = %q, want one for the step", got)
		}
	})
}

// The control for the test above: the same step without ExtendStart is
// abandoned at core.lifecycle.start_timeout, so the test above proves the
// extension and not a generous default.
func TestExtendStart_WithoutItTheStartTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, deps := &recorder{}, &depsOf{}
		var hasDeadline bool
		alfred := startingModule("alfred", deps, func(ctx context.Context) error {
			_, hasDeadline = ctx.Deadline()
			return napping(ctx, 5*time.Minute)
		})
		took, err := runStart(t, rec, supervised(t, rec, alfred))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run = %v, want DeadlineExceeded", err)
		}
		if hasDeadline {
			t.Fatal("the hook's ctx has a deadline; the window cannot move one (Hook.Start documents none)")
		}
		if strings.Contains(err.Error(), "as extended by ExtendStart") {
			t.Fatalf("Run = %q claims an extension no step asked for", err)
		}
		if took != startTimeout {
			t.Fatalf("start failed after %s, want %s", took, startTimeout)
		}
	})
}

// A hung step is still killed once its own budget runs out: ExtendStart moves
// the deadline to StartupStepBudget from the call, no further (HEAD's rule:
// extend per step, never from a timer).
func TestExtendStart_HungStepStillKilledAtItsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, deps := &recorder{}, &depsOf{}
		var hookErr error
		alfred := startingModule("alfred", deps, func(ctx context.Context) error {
			deps.get("alfred").ExtendStart("wedge")
			<-ctx.Done()
			hookErr = ctx.Err()
			return hookErr
		})
		took, err := runStart(t, rec, supervised(t, rec, alfred))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run = %v, want DeadlineExceeded", err)
		}
		if took != sdnotify.StartupStepBudget {
			t.Fatalf("start failed after %s, want the step budget %s", took, sdnotify.StartupStepBudget)
		}
		if !strings.Contains(err.Error(), "as extended by ExtendStart") {
			t.Fatalf("Run = %q, want it to say the deadline was extended", err)
		}
		if !errors.Is(hookErr, context.DeadlineExceeded) {
			t.Fatalf("the hook's ctx.Err() = %v, want DeadlineExceeded, as a deadline context reports", hookErr)
		}
	})
}

// Each step that makes progress extends again: three 9 minute steps in one
// Start run 27 minutes, though no single extension reaches that far.
func TestExtendStart_EveryStepExtendsAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, deps := &recorder{}, &depsOf{}
		alfred := startingModule("alfred", deps, func(ctx context.Context) error {
			for range 3 {
				deps.get("alfred").ExtendStart("step")
				if err := napping(ctx, 9*time.Minute); err != nil {
					return err
				}
			}
			return nil
		})
		took, err := runStart(t, rec, supervised(t, rec, alfred))
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
		if took != 27*time.Minute {
			t.Fatalf("start took %s, want 27m", took)
		}
	})
}

// Only the module whose Start is running extends it: another module's
// ExtendStart (a late call from a goroutine it started, say) must not keep a
// hung Start alive.
func TestExtendStart_OnlyTheRunningModuleExtends(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, deps := &recorder{}, &depsOf{}
		bane := startingModule("bane", deps)
		alfred := startingModule("alfred", deps, func(ctx context.Context) error {
			deps.get("bane").ExtendStart("not mine")
			<-ctx.Done()
			return ctx.Err()
		})
		took, err := runStart(t, rec, supervised(t, rec, alfred, bane))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run = %v, want DeadlineExceeded", err)
		}
		if took != startTimeout {
			t.Fatalf("start failed after %s, want %s: another module's ExtendStart extended it", took, startTimeout)
		}
	})
}

// The window belongs to one hook's Start: a hook that follows one that
// extended gets a fresh core.lifecycle.start_timeout of its own.
func TestExtendStart_WindowIsPerHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, deps := &recorder{}, &depsOf{}
		alfred := startingModule("alfred", deps,
			func(ctx context.Context) error {
				deps.get("alfred").ExtendStart("long")
				return napping(ctx, 5*time.Minute)
			},
			func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		)
		took, err := runStart(t, rec, supervised(t, rec, alfred))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run = %v, want DeadlineExceeded", err)
		}
		if want := 5*time.Minute + startTimeout; took != want {
			t.Fatalf("start failed after %s, want %s: the second hook did not get its own window", took, want)
		}
	})
}

// ExtendStart outside any Start (from Register, or once the app is up) asks
// the service manager and extends nothing else.
// Before Run (during Register) a module's ExtendStart reaches the service
// manager. Once its own Start is not running (after READY) it does not: there
// is no start timeout left to extend, and EXTEND_TIMEOUT_USEC sent while the
// unit is deactivating would push TimeoutStopSec out.
func TestExtendStart_OutsideAStartIsHarmless(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, steps, deps := &recorder{}, &stepRecorder{}, &depsOf{}
		alfred := mod("alfred", func(r *module.Registrar) error {
			deps.put(r.Deps())
			r.Deps().ExtendStart("register")
			return nil
		})
		c := supervised(t, rec, alfred)
		c.ExtendStart = steps.extend
		k := mustNew(t, c)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		deps.get("alfred").ExtendStart("after ready")
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
		if got := steps.list(); len(got) != 1 || got[0] != "register" {
			t.Fatalf("service-manager extensions = %q, want only the Register-time one", got)
		}
	})
}

// A hook's Stop (the deactivating state) never asks the service manager for
// time, even though the same module extended while it started.
func TestExtendStart_NotSentDuringStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec, steps, deps := &recorder{}, &stepRecorder{}, &depsOf{}
		alfred := mod("alfred", func(r *module.Registrar) error {
			deps.put(r.Deps())
			r.Hook(module.Hook{
				Name:  "alfred.a",
				Start: func(context.Context) error { r.Deps().ExtendStart("start"); return nil },
				Stop:  func(context.Context) error { r.Deps().ExtendStart("stop"); return nil },
			})
			return nil
		})
		c := supervised(t, rec, alfred)
		c.ExtendStart = steps.extend
		k := mustNew(t, c)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
		if got := steps.list(); len(got) != 1 || got[0] != "start" {
			t.Fatalf("service-manager extensions = %q, want only the start-time one", got)
		}
	})
}

func TestStartWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var w startWindow
		var expired int
		var mu sync.Mutex
		expire := func() { mu.Lock(); expired++; mu.Unlock() }
		count := func() int { mu.Lock(); defer mu.Unlock(); return expired }

		// An unextended window expires once, at its deadline.
		w.begin("alfred", time.Minute, expire)
		time.Sleep(time.Minute - time.Second)
		if count() != 0 {
			t.Fatal("expired early")
		}
		time.Sleep(2 * time.Second)
		if count() != 1 {
			t.Fatalf("expired %d times at the deadline, want 1", count())
		}
		time.Sleep(time.Hour)
		if count() != 1 {
			t.Fatalf("expired %d times, want exactly once", count())
		}
		if w.extend("alfred", time.Hour) {
			t.Fatal("extend reported success on an expired window")
		}

		// An extended window re-arms its timer and expires at the new deadline.
		end := w.begin("alfred", time.Minute, expire)
		if w.extend("bane", time.Hour) {
			t.Fatal("another owner extended the window")
		}
		if !w.extend("alfred", 10*time.Minute) {
			t.Fatal("the owner could not extend")
		}
		time.Sleep(9 * time.Minute)
		if count() != 1 {
			t.Fatal("expired at the original deadline despite the extension")
		}
		if !w.extend("alfred", time.Minute) { // earlier than the deadline: no change
			t.Fatal("the owner could not extend")
		}
		time.Sleep(2 * time.Minute)
		if count() != 2 {
			t.Fatalf("expired %d times, want 2 after the extended deadline", count())
		}
		end() // closing an expired window is harmless

		// A closed window never expires, and its stale timer cannot expire the next.
		end = w.begin("alfred", time.Minute, expire)
		end()
		next := w.begin("alfred", 5*time.Minute, expire)
		time.Sleep(2 * time.Minute)
		if count() != 2 {
			t.Fatalf("expired %d times: a closed window's timer fired", count())
		}
		next()
		time.Sleep(time.Hour)
		if count() != 2 {
			t.Fatalf("expired %d times after the window closed", count())
		}
	})
}
