// file: core/internal/executor/writer_test.go
// version: 1.0.0
// guid: d2387b9f-4100-4a09-af79-f3829a530ac5
// last-edited: 2026-11-11

package executor

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// doOutcome is what a body's write after its stop saw.
type doOutcome struct {
	err    error
	called bool
	cause  error // the run context's cause: which stop it was
}

// TestWriter_RefusesOnceTheRunIsStopped (5T.3.5, synctest; review of #4586):
// once a run is stopped its writer refuses with ErrFenced and never calls
// fn, whatever stopped it: a cancel (still inside its grace, fence epoch
// intact), the watchdog, the attempt's Timeout, a stand-down quiesce or
// Shutdown. Before the fix only an abandoned attempt was refused, so a body
// that ignored its canceled context kept writing until the grace ran out.
func TestWriter_RefusesOnceTheRunIsStopped(t *testing.T) {
	type cell struct {
		name  string
		edits []func(*tasks.Common)
		cfg   func(*Config)
		stop  func(h *harness, id tasks.RunID)
		cause error
	}
	lab := func(c *tasks.Common) { c.Effects = tasks.Writes(tasks.Res("gotham", "lab")) }
	cells := []cell{
		{name: "cancel", cause: tasks.ErrCanceled, stop: func(h *harness, id tasks.RunID) {
			if err := h.x.CancelAs(context.Background(), id, tasks.Actor{UserID: "gordon"}); err != nil {
				h.t.Fatal(err)
			}
		}},
		{name: "watchdog", cause: tasks.ErrCanceled, edits: []func(*tasks.Common){func(c *tasks.Common) { c.ProgressTimeout = time.Minute }},
			cfg:  func(c *Config) { c.WatchdogInterval = 30 * time.Second },
			stop: func(*harness, tasks.RunID) { time.Sleep(3 * time.Minute) }},
		{name: "timeout", cause: tasks.ErrTimedOut, edits: []func(*tasks.Common){withTimeout(time.Minute)},
			stop: func(*harness, tasks.RunID) { time.Sleep(2 * time.Minute) }},
		{name: "quiesce", cause: tasks.ErrQuiesced, edits: []func(*tasks.Common){func(c *tasks.Common) { c.Yields = true }},
			stop: func(h *harness, _ tasks.RunID) { h.start("gotham.compact", tasks.StartRequest{}) }},
		{name: "shutdown", cause: tasks.ErrShutdown, stop: func(h *harness, _ tasks.RunID) {
			go func() { _ = h.x.Shutdown(context.Background()) }()
		}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{}, 1)
				got := make(chan doOutcome, 1)
				body := func(rc *tasks.RC, _ noParams) (string, error) {
					started <- struct{}{}
					<-rc.Context().Done()
					o := doOutcome{cause: context.Cause(rc.Context())}
					o.err = rc.Writer().Do(context.Background(), tasks.Write{Name: "gotham.note", Resource: casesRef("gotham.scan"),
						Subject: tasks.ResID("gotham", "cases", "riddler")}, func(context.Context) ([]tasks.Change, error) {
						o.called = true
						return nil, nil
					})
					got <- o
					return "", context.Cause(rc.Context())
				}
				holder := taskDef("gotham.compact", func(rc *tasks.RC, _ noParams) (string, error) {
					if _, err := rc.StandDown("gotham.scan"); err != nil {
						return "", err
					}
					<-rc.Context().Done()
					return "", context.Cause(rc.Context())
				}, lab)
				edits := append([]func(*tasks.Common){withCancel(tasks.Cooperative(time.Hour))}, c.edits...)
				h := newHarness(t, []tasks.Definition{taskDef("gotham.scan", body, edits...), holder}, func(o *Options) {
					o.Config = Config{DispatchTick: time.Second, WatchdogInterval: time.Hour}
					if c.cfg != nil {
						c.cfg(&o.Config)
					}
				})
				id := h.start("gotham.scan", tasks.StartRequest{})
				<-started
				before := h.get(id)
				c.stop(h, id)
				o := <-got
				if !isErr(o.cause, c.cause) {
					t.Fatalf("the run stopped with %v, want the %s's %v", o.cause, c.name, c.cause)
				}
				if !isErr(o.err, tasks.ErrFenced) || o.called {
					t.Fatalf("write after the %s: err %v, fn called %v; want ErrFenced and fn never called", c.name, o.err, o.called)
				}
				if c.name == "cancel" {
					if r := h.get(id); r.FenceEpoch != before.FenceEpoch {
						t.Fatalf("fence epoch %d, was %d: the refusal must not wait for the grace", r.FenceEpoch, before.FenceEpoch)
					}
				}
			})
		})
	}
}
