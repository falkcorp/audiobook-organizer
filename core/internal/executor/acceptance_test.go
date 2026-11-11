// file: core/internal/executor/acceptance_test.go
// version: 1.0.1
// guid: 0d06a72a-c352-44d0-b19d-3122c4700486
// last-edited: 2026-11-10

package executor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/tasks"
)

// TestDispatch_WaitsForReadySignal (COR4-8, 5T.4 step 2): a books run that
// reads books is not dispatched before books.memdb reports Up, and while
// it waits it holds no worker: another run dispatches on the one worker.
func TestDispatch_WaitsForReadySignal(t *testing.T) {
	reg := readyRegistry(t)
	memdb, err := reg.Register(health.Check{ID: "books.memdb", Title: "Book memdb", Readiness: true})
	if err != nil {
		t.Fatal(err)
	}
	var readerRan atomic.Int32
	reader := taskDef("books.read-casefiles", func(rc *tasks.RC, _ noParams) (string, error) {
		readerRan.Add(1)
		return "read", nil
	}, readOnly("books.read-casefiles"), func(c *tasks.Common) { c.Ready = []tasks.ReadySignal{"books.memdb"} })
	other := taskDef("gotham.signal", okBody)
	h := newHarness(t, []tasks.Definition{reader, other}, func(o *Options) {
		o.Health = reg
		o.Config.Workers = 1
	})
	rid := h.start("books.read-casefiles", tasks.StartRequest{})
	h.stays(rid, tasks.StateQueued, 150*time.Millisecond)
	if readerRan.Load() != 0 {
		t.Fatal("the reader ran before books.memdb was Up")
	}
	oid := h.start("gotham.signal", tasks.StartRequest{})
	h.waitState(oid, tasks.StateSucceeded) // the waiting run held no worker
	if got := h.get(rid).State; got != tasks.StateQueued {
		t.Fatalf("reader is %s while books.memdb is not Up", got)
	}
	memdb.Report(health.Result{Status: health.StatusUp})
	h.waitState(rid, tasks.StateSucceeded)
	if readerRan.Load() != 1 {
		t.Fatalf("reader ran %d times, want 1", readerRan.Load())
	}
}

// TestShutdown_DrainBoundedAndStampsInterrupted (5T.4 step 4): Shutdown
// cancels every run with ErrShutdown, waits min(shutdown_drain, budget),
// stamps a run that did not return interrupted with its fence revoked (a
// late write is ErrFenced), and a cooperative run ends interrupted on its
// own.
func TestShutdown_DrainBoundedAndStampsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration // 0: no deadline on Shutdown's ctx
		want   time.Duration
	}{
		{"drain bounds the wait", 0, 10 * time.Second},
		{"the caller's budget bounds the wait", 3 * time.Second, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				late := make(chan error, 1)
				started := make(chan tasks.RunID, 2)
				stubborn := taskDef("gotham.stubborn", func(rc *tasks.RC, _ noParams) (string, error) {
					started <- rc.RunID()
					<-release // ignores its context
					late <- rc.Writer().Do(context.Background(), tasks.Write{Name: "gotham.close", Resource: casesRef("gotham.stubborn"),
						Subject: tasks.ResID("gotham", "cases", "joker")}, func(context.Context) ([]tasks.Change, error) { return nil, nil })
					return "late", nil
				})
				coop := taskDef("arkham.coop", blockUntilCtx(started))
				h := newHarness(t, []tasks.Definition{stubborn, coop}, func(o *Options) {
					o.Config = Config{ShutdownDrain: 10 * time.Second, DispatchTick: time.Second, WatchdogInterval: time.Hour}
				})
				sid := h.start("gotham.stubborn", tasks.StartRequest{})
				cid := h.start("arkham.coop", tasks.StartRequest{})
				<-started
				<-started
				before := h.get(sid)
				ctx := context.Background()
				if tc.budget > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.budget)
					defer cancel()
				}
				t0 := time.Now()
				if err := h.x.Shutdown(ctx); err != nil {
					t.Fatalf("Shutdown: %v", err)
				}
				if took := time.Since(t0); took != tc.want {
					t.Fatalf("Shutdown took %s, want %s", took, tc.want)
				}
				c := h.get(cid)
				if c.State != tasks.StateInterrupted || c.Reason != reasonShutdown {
					t.Fatalf("cooperative run is %s/%q, want interrupted/shutdown", c.State, c.Reason)
				}
				s := h.get(sid)
				if s.State != tasks.StateInterrupted || s.Reason != reasonShutdown {
					t.Fatalf("stubborn run is %s/%q, want interrupted/shutdown", s.State, s.Reason)
				}
				if s.FenceEpoch != before.FenceEpoch+1 {
					t.Fatalf("stubborn fence epoch %d, want %d (revoked)", s.FenceEpoch, before.FenceEpoch+1)
				}
				close(release)
				if err := <-late; !isErr(err, tasks.ErrFenced) {
					t.Fatalf("late write after the stamp: %v, want ErrFenced", err)
				}
				synctest.Wait()
				if got := h.get(sid); got.State != tasks.StateInterrupted {
					t.Fatalf("the zombie's return changed its row to %s", got.State)
				}
			})
		})
	}
}

// TestCancel_GraceThenFenceRevoked (5T.3.6): a cancel moves the run to
// stopping and cancels its context; within the grace its writes still
// land; after the grace the fence is revoked (FenceEpoch+1, the row ends
// canceled, writes are ErrFenced) and the goroutine is a zombie that keeps
// the concurrency key until it exits.
func TestCancel_GraceThenFenceRevoked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type cmd struct{ reply chan error }
		cmds := make(chan cmd)
		release := make(chan struct{})
		started := make(chan tasks.RunID, 4)
		var calls atomic.Int32
		def := taskDef("gotham.interrogation", func(rc *tasks.RC, _ noParams) (string, error) {
			if calls.Add(1) > 1 {
				return "second", nil
			}
			started <- rc.RunID()
			for {
				select {
				case c := <-cmds:
					c.reply <- rc.Writer().Do(context.Background(), tasks.Write{Name: "gotham.note", Resource: casesRef("gotham.interrogation"),
						Subject: tasks.ResID("gotham", "cases", "riddler")}, func(context.Context) ([]tasks.Change, error) { return nil, nil })
				case <-release:
					return "ignored the cancel", nil
				}
			}
		}, withCancel(tasks.Cooperative(5*time.Second)))
		h := newHarness(t, []tasks.Definition{def}, func(o *Options) {
			o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
		})
		write := func() error {
			c := cmd{reply: make(chan error, 1)}
			cmds <- c
			return <-c.reply
		}
		id := h.start("gotham.interrogation", tasks.StartRequest{})
		<-started
		before := h.get(id)
		if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "alfred"}); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if r := h.get(id); r.State != tasks.StateStopping || r.Reason != reasonCancel {
			t.Fatalf("after cancel: %s/%q, want stopping/cancel", r.State, r.Reason)
		}
		synctest.Wait() // every goroutine idle: a zero grace would have abandoned the attempt by now
		if err := write(); err != nil {
			t.Fatalf("write inside the grace: %v, want nil (fence intact)", err)
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		r := h.get(id)
		if r.State != tasks.StateCanceled || r.FenceEpoch != before.FenceEpoch+1 || r.Finished.IsZero() {
			t.Fatalf("after the grace: %s fence %d finished %v, want canceled, fence %d, finished", r.State, r.FenceEpoch, r.Finished, before.FenceEpoch+1)
		}
		if err := write(); !isErr(err, tasks.ErrFenced) {
			t.Fatalf("write after the grace: %v, want ErrFenced", err)
		}
		next := h.start("gotham.interrogation", tasks.StartRequest{})
		h.stays(next, tasks.StateQueued, 3*time.Second) // the zombie keeps the key
		close(release)
		h.waitState(next, tasks.StateSucceeded)
		if got := h.get(id); got.State != tasks.StateCanceled {
			t.Fatalf("the zombie's return rewrote its row to %s", got.State)
		}
	})
}

// TestResume_PolicyMatrix (5T.3.6): 4 resume policies × {shutdown, crash,
// timeout, cancel}. Shutdown and crash leave the run interrupted (or
// running) and the next boot applies the policy; timeout and cancel are
// terminal whatever the policy.
func TestResume_PolicyMatrix(t *testing.T) {
	policies := []tasks.ResumePolicy{tasks.ResumeContinue, tasks.ResumeFromZero, tasks.ResumeDrop, tasks.ResumeAsk}
	afterBoot := map[tasks.ResumePolicy]tasks.RunState{
		tasks.ResumeContinue: tasks.StateSucceeded, tasks.ResumeFromZero: tasks.StateSucceeded,
		tasks.ResumeDrop: tasks.StateInterruptedDropped, tasks.ResumeAsk: tasks.StateAwaitingDecision,
	}
	for _, pol := range policies {
		for _, scen := range []string{"shutdown", "crash", "timeout", "cancel"} {
			t.Run(fmt.Sprintf("%s/%s", pol, scen), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					started := make(chan tasks.RunID, 4)
					var calls atomic.Int32
					resumed := make(chan bool, 1)
					timeout := time.Hour // out of the way of the other scenarios
					if scen == "timeout" {
						timeout = time.Minute
					}
					def := taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
						if calls.Add(1) > 1 {
							resumed <- rc.Resumed()
							return "second attempt", nil
						}
						if err := rc.Checkpoint(map[string]int{"rooftop": 3}); err != nil {
							return "", err
						}
						started <- rc.RunID()
						<-rc.Context().Done()
						return "", context.Cause(rc.Context())
					}, withResume(pol), withTimeout(timeout))
					defs := []tasks.Definition{def}
					cfg := func(o *Options) {
						o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour, MinCheckpointInterval: time.Nanosecond}
					}
					h := newHarness(t, defs, cfg)
					id := h.start("gotham.patrol", tasks.StartRequest{})
					<-started
					firstFence := h.get(id).FenceEpoch
					var next *harness
					switch scen {
					case "shutdown":
						if err := h.x.Shutdown(context.Background()); err != nil {
							t.Fatal(err)
						}
						if r := h.get(id); r.State != tasks.StateInterrupted {
							t.Fatalf("after shutdown: %s, want interrupted", r.State)
						}
						next = h.reboot(defs, cfg)
					case "crash":
						next = h.crashBoot(defs, cfg)
					case "timeout":
						time.Sleep(timeout + time.Second)
						synctest.Wait()
						r := h.waitState(id, tasks.StateTimedOut, tasks.StateSucceeded, tasks.StateFailed)
						if r.State != tasks.StateTimedOut || r.Reason != reasonTimeout {
							t.Fatalf("after the timeout: %s/%q, want timed_out/timeout", r.State, r.Reason)
						}
						return
					case "cancel":
						if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
							t.Fatal(err)
						}
						if r := h.waitState(id, tasks.StateCanceled, tasks.StateFailed); r.State != tasks.StateCanceled {
							t.Fatalf("after the cancel: %s, want canceled", r.State)
						}
						return
					}
					want := afterBoot[pol]
					r := next.waitState(id, want)
					if want == tasks.StateSucceeded {
						got := <-resumed
						if wantResumed := pol == tasks.ResumeContinue; got != wantResumed {
							t.Fatalf("second attempt Resumed() = %v, want %v", got, wantResumed)
						}
						if r.Attempt != 2 {
							t.Fatalf("attempt %d, want 2 (same run, one more attempt)", r.Attempt)
						}
						if r.FenceEpoch <= firstFence {
							t.Fatalf("fence epoch %d after the second claim, want above the first attempt's %d", r.FenceEpoch, firstFence)
						}
					} else if calls.Load() != 1 {
						t.Fatalf("body ran %d times under %s, want 1", calls.Load(), pol)
					}
				})
			})
		}
	}
}
