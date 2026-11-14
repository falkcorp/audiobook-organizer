// file: core/health/checks.go
// version: 1.0.1
// guid: 0c9d2f4e-3b86-4a1d-8e75-4f2a6b9c1d03
// last-edited: 2026-11-13

package health

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"
)

// The probe loop, the heartbeat and the readiness conditions that are not
// components (05-health §5H.3.3, §5H.5, §5H.6). None of it runs until the
// kernel calls the Start functions: NewRegistry and Register start no
// goroutine.

// StartChecks runs every probed component's Probe on its Interval (default
// core.health.check_interval), one goroutine per component with a jittered
// period so checks do not align, each under its Timeout, never concurrently
// with itself. A component registered while the checks run joins them at once.
// A Probe's panic or overrun becomes a Down result (check_panic,
// check_timeout) and never reaches the loop. The returned stop ends every
// loop and waits for them; a second StartChecks while one runs returns a stop
// that does nothing.
func (r *Registry) StartChecks(ctx context.Context) (stop func()) {
	r.mu.Lock()
	if r.runCtx != nil {
		r.mu.Unlock()
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	r.runCtx = ctx
	for _, comp := range r.comps {
		if comp.check.Probe != nil {
			r.spawnLocked(comp)
		}
	}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.runCtx = nil // no loop starts after this
		r.mu.Unlock()
		cancel()
		r.wg.Wait()
	}
}

// spawnLocked starts comp's loop under the running context. r.mu is held and
// r.runCtx is set.
func (r *Registry) spawnLocked(comp *component) {
	r.wg.Add(1)
	go r.loop(r.runCtx, comp)
}

// interval is comp's period: its own, else core.health.check_interval.
func (r *Registry) interval(comp *component) time.Duration {
	if comp.check.Interval > 0 {
		return comp.check.Interval
	}
	return r.cfg().CheckInterval
}

// loop calls comp's Probe once per interval until ctx ends.
func (r *Registry) loop(ctx context.Context, comp *component) {
	defer r.wg.Done()
	t := time.NewTimer(r.jittered(r.interval(comp)))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.runOnce(ctx, comp)
		t.Reset(r.jittered(r.interval(comp)))
	}
}

// jittered is d spread by up to 10 percent either way, so the loops of
// components with equal intervals drift apart instead of probing together.
func (r *Registry) jittered(d time.Duration) time.Duration {
	if r.noJitter || d < 10 {
		return d
	}
	return d - d/10 + time.Duration(rand.Int64N(int64(d/5)+1))
}

// runOnce is one check of comp: skipped while an abandoned call of the same
// Probe is still running, recorded as Down check_timeout when it overruns.
func (r *Registry) runOnce(ctx context.Context, comp *component) {
	if comp.running.Load() {
		return
	}
	res, took, ok, _ := r.runCheck(ctx, comp)
	if !ok {
		return // shutdown: the probe was cancelled, record nothing
	}
	res = r.clamp(res)
	r.mu.Lock()
	comp.result, comp.duration = res, took
	r.broadcastLocked()
	r.mu.Unlock()
	r.metrics.recordCheck(comp.check.ID, took, res, res.Status == StatusDown)
	r.afterChange()
}

// StartHeartbeat starts the kernel heartbeat goroutine (05-health §5H.3.3).
// Every tick it runs work in order (a work func that blocks models a wedged
// process: the beat is then never recorded), records the beat that /livez
// follows, and then lets sup send the watchdog ping when it is due, so a
// wedged process stops pinging and systemd restarts it. The tick is a quarter
// of core.health.live_stall, or half the watchdog period when that is shorter.
// The first beat is recorded before StartHeartbeat returns. stop ends the
// heartbeat and marks the registry as not beating (live again by the F2
// rule); it does not wait for a work func that is wedged. sup may be nil. A
// second StartHeartbeat while one runs starts nothing and returns a stop that
// does nothing, as StartChecks does.
func (r *Registry) StartHeartbeat(ctx context.Context, sup *Supervisor, work ...func()) (stop func()) {
	if !r.beating.CompareAndSwap(false, true) {
		// One heartbeat per registry: a second would double the watchdog pings
		// and its stop would mark the first one's registry as not beating.
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	r.lastBeat.Store(r.now().UnixNano())
	go func() {
		t := time.NewTimer(r.beatEvery(sup))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for _, w := range work {
				w()
			}
			if ctx.Err() != nil {
				return
			}
			now := r.now()
			r.lastBeat.Store(now.UnixNano())
			sup.WatchdogTick(now)
			t.Reset(r.beatEvery(sup))
		}
	}()
	return func() {
		cancel()
		r.beating.Store(false)
	}
}

// beatEvery is the heartbeat period.
func (r *Registry) beatEvery(sup *Supervisor) time.Duration {
	d := r.cfg().LiveStall / 4
	if w := sup.watchdogEvery(); w > 0 && w < d {
		d = w
	}
	return d
}

// BeginShutdown marks the process as shutting down: /readyz answers 503 with
// the token core.kernel:stopping from here, before the HTTP listener drains
// (§5H.4), and the status topic publishes ready:false once. It is idempotent.
func (r *Registry) BeginShutdown() {
	r.mu.Lock()
	if r.shuttingDown {
		r.mu.Unlock()
		return
	}
	r.shuttingDown = true
	r.broadcastLocked()
	r.mu.Unlock()
	r.afterChange()
}

// setStartup records whether startup is pending (a Startup attached and not
// yet complete) and the degraded startup tokens. Called by Startup when it
// attaches; later changes go through setStartupPending and setStartupTokens,
// which each touch only their own field.
func (r *Registry) setStartup(pending bool, tokens []string) {
	r.mu.Lock()
	r.startupPending = pending
	r.startupTokens = tokens
	r.broadcastLocked()
	r.mu.Unlock()
	r.afterChange()
}

// setStartupPending sets only whether startup is pending, so Complete cannot
// lose a token a step recorded meanwhile.
func (r *Registry) setStartupPending(pending bool) {
	r.mu.Lock()
	r.startupPending = pending
	r.broadcastLocked()
	r.mu.Unlock()
	r.afterChange()
}

// setStartupTokens adds tokens to the degraded startup tokens and leaves
// whether startup is pending alone: a step that fails while Complete runs must
// not put "starting" back (a read-then-write of pending would). Tokens only
// ever grow, so the union makes two steps recording at once order-independent.
func (r *Registry) setStartupTokens(tokens []string) {
	r.mu.Lock()
	merged := slices.Clone(r.startupTokens)
	for _, t := range tokens {
		if !slices.Contains(merged, t) {
			merged = append(merged, t)
		}
	}
	slices.Sort(merged)
	r.startupTokens = merged
	r.broadcastLocked()
	r.mu.Unlock()
	r.afterChange()
}
