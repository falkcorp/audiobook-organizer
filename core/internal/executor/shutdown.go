// file: core/internal/executor/shutdown.go
// version: 1.0.0
// guid: fd4f7484-5479-4b62-a92e-17b97f1de5d5
// last-edited: 2026-11-10

package executor

import (
	"context"
	"time"

	"github.com/falkcorp/mediacore/lifecycle"
	"github.com/falkcorp/mediacore/tasks"
)

// Shutdown implements tasks.Executor.Shutdown (5T.4 step 4): flip
// shutting-down (the dispatcher claims nothing more and releases a claim in
// flight), cancel every executing run with tasks.ErrShutdown, wait up to
// min(core.tasks.shutdown_drain, ctx's remaining budget), stamp the rest
// interrupted (a run stopping for a cancel ends canceled) with their fence
// revoked, stop the owned goroutines and return. Only when an
// Uninterruptible run is in flight under systemd (NOTIFY_SOCKET set) does
// it ask once for EXTEND_TIMEOUT_USEC of at most
// core.tasks.shutdown_extend_max and wait that long for those runs; it
// never waits unbounded.
func (e *executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if e.shutDone || e.shutting {
		e.mu.Unlock()
		return nil
	}
	e.shutting = true
	live := e.liveLocked()
	for _, a := range live {
		if a.stopping == "" {
			a.stopping = reasonShutdown
		}
	}
	e.mu.Unlock()
	cfg := e.config()
	for _, a := range live {
		a.cancel(tasks.ErrShutdown)
	}
	drain := cfg.ShutdownDrain
	if dl, ok := ctx.Deadline(); ok {
		drain = min(drain, time.Until(dl))
	}
	waitAll(live, drain)
	var never []*attempt
	for _, a := range live {
		if !isDone(a) && a.d.never {
			never = append(never, a)
		}
	}
	if len(never) > 0 && e.opts.Getenv(lifecycle.NotifySocketEnv) != "" {
		var budget time.Duration
		for _, a := range never {
			budget = max(budget, a.d.grace)
		}
		ext := min(budget, cfg.ShutdownExtendMax)
		if err := e.opts.ExtendTimeout(ext); err != nil {
			e.log.Warn("systemd timeout extension refused", "err", err)
		} else {
			e.log.Warn("waiting for uninterruptible runs past the shutdown budget", "runs", len(never), "extension", ext)
			waitAll(never, ext)
		}
	}
	for _, a := range live {
		if isDone(a) {
			continue
		}
		e.mu.Lock()
		stopping := a.stopping
		e.mu.Unlock()
		state, reason := tasks.StateInterrupted, reasonShutdown
		if stopping == reasonCancel || stopping == reasonWatchdog {
			state, reason = tasks.StateCanceled, stopping
		}
		e.abandon(a, state, reason)
	}
	e.mu.Lock()
	e.shutDone = true
	e.mu.Unlock()
	e.bgStop()
	e.bg.Wait()
	return nil
}

// liveLocked lists the attempts executing in this process (claims in
// flight and zombies excluded).
func (e *executor) liveLocked() []*attempt {
	var out []*attempt
	for _, a := range e.running {
		if !a.zombie && a.live {
			out = append(out, a)
		}
	}
	return out
}

// waitAll waits until every attempt's goroutine returned or d elapsed.
func waitAll(as []*attempt, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	for _, a := range as {
		select {
		case <-a.done:
		case <-t.C:
			return
		}
	}
}

func isDone(a *attempt) bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}
