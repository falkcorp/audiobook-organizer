// file: core/internal/executor/watchdog_test.go
// version: 1.0.0
// guid: 03cef091-01c2-4e5b-9642-19f877fa8c15
// last-edited: 2026-11-10

package executor

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// strikesOf lists a run's watchdog strike kinds in order.
func (h *harness) strikesOf(id tasks.RunID) []string {
	h.t.Helper()
	var out []string
	for s, err := range h.stores.Runs.Strikes(context.Background(), id) {
		if err != nil {
			h.t.Fatalf("strikes: %v", err)
		}
		out = append(out, s.Kind)
	}
	return out
}

// TestWatchdog_SilentTaskStoppedWithStrike (5T.3.6, synctest; not in the
// brief's list, added for the watchdog invariants): a Task silent past its
// ProgressTimeout is struck (never_reported, or stuck once it has reported)
// and stopped as a cancel is, with reason watchdog; a task that keeps
// reporting and an Uninterruptible task are left alone; a ResumeContinue
// task with no checkpoint in 3 x min_checkpoint_interval gets one
// uncheckpointed strike and keeps running.
func TestWatchdog_SilentTaskStoppedWithStrike(t *testing.T) {
	type cell struct {
		name        string
		body        func(rc *tasks.RC, _ noParams) (string, error)
		edits       []func(*tasks.Common)
		wantState   tasks.RunState
		wantReason  string
		wantStrikes []string
	}
	silent := func(rc *tasks.RC, _ noParams) (string, error) {
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}
	reportsOnce := func(rc *tasks.RC, _ noParams) (string, error) {
		rc.Progress().Set(1, tasks.Known(10))
		<-rc.Context().Done()
		return "", context.Cause(rc.Context())
	}
	var ticks atomic.Int64
	chatty := func(rc *tasks.RC, _ noParams) (string, error) {
		for {
			select {
			case <-rc.Context().Done():
				return "", context.Cause(rc.Context())
			case <-time.After(20 * time.Second):
				rc.Progress().Set(ticks.Add(1), tasks.Unknown())
				if err := rc.Checkpoint(ticks.Load()); err != nil {
					return "", err
				}
			}
		}
	}
	chattyNoCheckpoint := func(rc *tasks.RC, _ noParams) (string, error) {
		for {
			select {
			case <-rc.Context().Done():
				return "", context.Cause(rc.Context())
			case <-time.After(20 * time.Second):
				rc.Progress().Set(ticks.Add(1), tasks.Unknown())
			}
		}
	}
	quickTO := func(c *tasks.Common) { c.ProgressTimeout = time.Minute; c.Cancel = tasks.Cooperative(time.Second) }
	cells := []cell{
		{name: "never reported", body: silent, edits: []func(*tasks.Common){quickTO, withResume(tasks.ResumeDrop)},
			wantState: tasks.StateCanceled, wantReason: reasonWatchdog, wantStrikes: []string{strikeNeverReported}},
		{name: "reported then silent", body: reportsOnce, edits: []func(*tasks.Common){quickTO, withResume(tasks.ResumeDrop)},
			wantState: tasks.StateCanceled, wantReason: reasonWatchdog, wantStrikes: []string{strikeStuck}},
		{name: "keeps reporting", body: chatty, edits: []func(*tasks.Common){quickTO},
			wantState: tasks.StateRunning},
		{name: "uninterruptible is exempt", body: silent, edits: []func(*tasks.Common){quickTO, withResume(tasks.ResumeDrop),
			func(c *tasks.Common) { c.Cancel = tasks.Uninterruptible("sealing the vault", time.Minute) }},
			wantState: tasks.StateRunning},
		{name: "continue without checkpoints", body: chattyNoCheckpoint, edits: []func(*tasks.Common){quickTO},
			wantState: tasks.StateRunning, wantStrikes: []string{strikeUncheckpointed}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, []tasks.Definition{taskDef("gotham.stakeout", c.body, c.edits...)}, func(o *Options) {
					o.Config = Config{DispatchTick: time.Second, WatchdogInterval: 30 * time.Second, MinCheckpointInterval: 20 * time.Second}
				})
				id := h.start("gotham.stakeout", tasks.StartRequest{})
				time.Sleep(3*time.Minute + 30*time.Second)
				synctest.Wait()
				r := h.get(id)
				if r.State != c.wantState || r.Reason != c.wantReason {
					t.Fatalf("after 3.5m: %s/%q, want %s/%q", r.State, r.Reason, c.wantState, c.wantReason)
				}
				if got := h.strikesOf(id); !equalStrings(got, c.wantStrikes) {
					t.Fatalf("strikes %v, want %v", got, c.wantStrikes)
				}
				if c.wantState == tasks.StateRunning {
					if err := h.x.Cancel(context.Background(), id); err != nil {
						t.Fatal(err)
					}
					time.Sleep(2 * time.Minute) // an Uninterruptible body returns at its own pace
				}
			})
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
