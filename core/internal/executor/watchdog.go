// file: core/internal/executor/watchdog.go
// version: 1.0.1
// guid: 6372f0c4-063d-47dd-8f0e-ab0caf15e5fa
// last-edited: 2026-11-10

package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// Watchdog strike kinds (raw/06 §5; tasks.Strike.Kind).
const (
	strikeStuck          = "stuck"
	strikeNeverReported  = "never_reported"
	strikeUncheckpointed = "uncheckpointed"
)

// watchdogActor is the actor stamped on a watchdog stop.
func watchdogActor() tasks.Actor {
	return tasks.Actor{AuthMethod: systemMethod, System: "core.tasks.watchdog"}
}

func (e *executor) watchdog() {
	defer e.bg.Done()
	t := time.NewTicker(e.config().WatchdogInterval)
	defer t.Stop()
	for {
		select {
		case <-e.bgCtx.Done():
			return
		case <-t.C:
			e.watchOnce(e.bgCtx)
		}
	}
}

// watchOnce checks every executing Task attempt (5T.3.6): Batch, Fixer and
// RunItems report for themselves, and an Uninterruptible task is exempt. A
// task silent past its ProgressTimeout gets a stuck (or never_reported)
// strike and is stopped as a cancel is, never resumed; a ResumeContinue
// task that ran longer than 3 × min_checkpoint_interval without a
// checkpoint gets one uncheckpointed strike per attempt.
func (e *executor) watchOnce(ctx context.Context) {
	now := e.now()
	cfg := e.config()
	e.mu.Lock()
	var live []*attempt
	for _, a := range e.running {
		if !a.zombie && a.live && a.stopping == "" && a.d.shape == tasks.ShapeTask {
			live = append(live, a)
		}
	}
	e.mu.Unlock()
	for _, a := range live {
		if a.d.c.Resume == tasks.ResumeContinue && !a.ckptd.Load() && now.Sub(a.start) > 3*cfg.MinCheckpointInterval &&
			a.struckCkpt.CompareAndSwap(false, true) {
			a.strike(ctx, strikeUncheckpointed, fmt.Sprintf("no checkpoint in %s", now.Sub(a.start).Round(time.Second)))
		}
		if a.d.never {
			continue
		}
		gap := now.Sub(time.Unix(0, a.lastReport.Load()))
		if gap <= a.d.progTO {
			continue
		}
		kind := strikeStuck
		if !a.reported.Load() {
			kind = strikeNeverReported
		}
		a.strike(ctx, kind, fmt.Sprintf("silent for %s (progress timeout %s)", gap.Round(time.Second), a.d.progTO))
		a.log.Warn("watchdog stops a silent run", "strike", kind, "silent", gap.Round(time.Second))
		if err := e.stop(ctx, a.id, watchdogActor(), reasonWatchdog, tasks.ErrCanceled); err != nil {
			a.log.Warn("watchdog stop failed", "err", err)
		}
	}
}
