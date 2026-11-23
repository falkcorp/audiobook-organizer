// file: core/internal/executor/standdown.go
// version: 1.0.0
// guid: 76fd6f03-58bc-4369-a11e-f3974d8d8409
// last-edited: 2026-11-10

package executor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// markerRestage is the minimum gap between two re-stagings of a live
// hold's marker on renewal (HEAD throttles the same write to once a second,
// scan_standdown.go).
const markerRestage = time.Second

// standHold is one stand-down key's live hold (5T.3.7): its holders, the
// runs it quiesced, and its lease. Guarded by executor.mu.
type standHold struct {
	key      tasks.Key
	holders  []*attempt
	quiesced []tasks.RunID
	expires  time.Time
	staged   time.Time
}

func (h *standHold) liveLocked(now time.Time) bool {
	return len(h.holders) > 0 && now.Before(h.expires)
}

// holdHandle is the tasks.Hold one StandDown call returns.
type holdHandle struct {
	release func()
	renew   func() error
}

func (h *holdHandle) Release()     { h.release() }
func (h *holdHandle) Renew() error { return h.renew() }

// standDown takes a hold on key for the run a: running holders of key that
// may yield (Common.Yields) are quiesced (stopping{quiesce}, context
// canceled with tasks.ErrQuiesced; their attempt ends interrupted_quiesced),
// new runs of key stay queued while the hold lives, and the marker is
// persisted through Stores.StandDown.
func (e *executor) standDown(a *attempt, key tasks.Key) (tasks.Hold, error) {
	lease := e.config().StandDownLease
	e.mu.Lock()
	now := e.now() // under the lock, like the reaper's and the renewals': a clock read before it could be stale by the time it joins a hold
	h := e.holds[key]
	var replaced []*attempt
	if h == nil || !h.liveLocked(now) {
		if h != nil {
			// An expired hold the reaper has not reached is swapped out of e.holds,
			// so the reaper (which walks only e.holds) never will: its holders lose
			// the hold here, the same way the reaper and a failed renewal take it.
			replaced = slices.Clone(h.holders)
			for _, r := range replaced {
				r.holdLost.Store(true)
			}
		}
		h = &standHold{key: key}
		e.holds[key] = h
	}
	h.holders = append(h.holders, a)
	h.expires = now.Add(lease)
	a.holds = append(a.holds, h)
	var victims []*attempt
	for _, r := range e.running {
		if r != a && !r.zombie && r.live && r.d.key == key && r.d.c.Yields && r.stopping == "" {
			r.stopping = reasonQuiesce
			victims = append(victims, r)
			h.quiesced = append(h.quiesced, r.id)
		}
	}
	marker := tasks.StandDown{Key: key, Holder: a.id, Quiesced: slices.Clone(h.quiesced), Expires: h.expires}
	h.staged = now
	set := markerWrite{key: key, m: &marker, ticket: e.markerTicketLocked(key)}
	e.mu.Unlock()
	for _, r := range replaced {
		r.cancel(tasks.ErrHoldLost)
	}
	if err := e.writeMarkers(context.Background(), set); err != nil {
		e.dropHolder(a, h)
		return nil, err
	}
	for _, v := range victims {
		_, err := e.move(context.Background(), v.id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			if cur.State != tasks.StateRunning || cur.Attempt != v.n || cur.FenceEpoch != v.fence {
				return cur, tasks.RunEvent{}, errStale(cur, "quiesce")
			}
			next := cur
			next.State, next.Reason = tasks.StateStopping, reasonQuiesce
			return next, tasks.RunEvent{Event: "quiesce", Reason: reasonQuiesce}, nil
		}, nil)
		if err != nil && !errors.Is(err, tasks.ErrRunConflict) {
			v.log.Warn("quiesce not recorded", "err", err)
		}
		v.cancel(tasks.ErrQuiesced)
	}
	var once sync.Once
	return &holdHandle{
		release: func() { once.Do(func() { e.dropHolder(a, h) }) },
		// The marker write must outlive a canceled run context, as Writer.Do's does.
		renew: func() error { return e.renewHold(context.WithoutCancel(a.ctx), a, h) },
	}, nil
}

// dropHolder removes a from h; the last holder clears the marker and
// re-queues the runs the hold quiesced. The marker is cleared only when h is
// still the key's hold: a hold that was replaced (its lease lapsed and another
// run took the key before this holder let go) no longer owns the marker, and
// clearing it would wipe the new holder's.
func (e *executor) dropHolder(a *attempt, h *standHold) {
	e.mu.Lock()
	h.holders = slices.DeleteFunc(h.holders, func(x *attempt) bool { return x == a })
	a.holds = slices.DeleteFunc(a.holds, func(x *standHold) bool { return x == h })
	last := len(h.holders) == 0
	var quiesced []tasks.RunID
	var clear *markerWrite
	if last {
		if e.holds[h.key] == h {
			delete(e.holds, h.key)
			clear = &markerWrite{key: h.key, ticket: e.markerTicketLocked(h.key)}
		}
		quiesced = h.quiesced
		h.quiesced = nil
	}
	e.mu.Unlock()
	if !last {
		return
	}
	if clear != nil {
		if err := e.writeMarkers(context.Background(), *clear); err != nil {
			e.log.Warn("stand-down marker not cleared; the next boot treats its runs as stood down", "key", string(h.key), "err", err)
		}
	}
	for _, id := range quiesced {
		rec, err := e.runs.Get(context.Background(), id)
		if err != nil || rec.State != tasks.StateInterruptedQuiesced {
			continue // still winding down: its attempt end re-queues it (finish)
		}
		if err := e.requeue(context.Background(), rec, tasks.StateInterruptedQuiesced, false); err != nil && !errors.Is(err, tasks.ErrRunConflict) {
			e.log.Warn("quiesced run not re-queued", "run", string(id), "err", err)
		}
	}
	e.wakeDispatcher()
}

// releaseHolds drops every hold an exiting attempt still holds.
func (e *executor) releaseHolds(a *attempt) {
	e.mu.Lock()
	hs := slices.Clone(a.holds)
	e.mu.Unlock()
	for _, h := range hs {
		e.dropHolder(a, h)
	}
}

// renewHolds renews a's holds (every Writer.Do, item start and progress report
// calls it); a hold whose lease already expired is lost (tasks.ErrHoldLost):
// the run's context is canceled with that cause and its writer refuses from
// then on. The marker is re-staged at most once a second.
func (e *executor) renewHolds(ctx context.Context, a *attempt) error {
	e.mu.Lock()
	// The clock is read under the lock that also decides the reaper's expiry
	// test, so a renewal and a reap of the same hold are ordered by the lock
	// and not by whichever read its clock first.
	now := e.now()
	lease := e.config().StandDownLease
	if a.holdLost.Load() {
		e.mu.Unlock()
		a.cancel(tasks.ErrHoldLost)
		return tasks.ErrHoldLost
	}
	var stage []markerWrite
	for _, h := range a.holds {
		w, err := e.renewLocked(a, h, now, lease)
		if err != nil {
			e.mu.Unlock()
			a.cancel(tasks.ErrHoldLost)
			return err
		}
		if w != nil {
			stage = append(stage, *w)
		}
	}
	e.mu.Unlock()
	return e.writeMarkers(ctx, stage...)
}

// renewHold renews one hold of a (Hold.Renew). It fails with tasks.ErrHoldLost
// once the lease had lapsed, once any other hold of the run was lost (the run's
// writer is fenced from then on), and once h is no longer one of a's holds
// (released, or reaped): a released hold is never revived and the lease of
// another holder of its key is never touched.
func (e *executor) renewHold(ctx context.Context, a *attempt, h *standHold) error {
	e.mu.Lock()
	now := e.now() // under the lock, as in renewHolds
	lease := e.config().StandDownLease
	if a.holdLost.Load() {
		e.mu.Unlock()
		a.cancel(tasks.ErrHoldLost)
		return tasks.ErrHoldLost
	}
	if !slices.Contains(a.holds, h) {
		e.mu.Unlock()
		return fmt.Errorf("%w: the hold on %s was released", tasks.ErrHoldLost, h.key)
	}
	w, err := e.renewLocked(a, h, now, lease)
	e.mu.Unlock()
	if err != nil {
		a.cancel(tasks.ErrHoldLost)
		return err
	}
	if w == nil {
		return nil
	}
	return e.writeMarkers(ctx, *w)
}

// renewLocked extends h's lease from now, or marks a's hold lost when the lease
// had already lapsed (the caller then cancels the run, unlocked). It returns the
// marker to re-stage when the last staging is at least markerRestage old and h
// is still the key's hold. Caller holds e.mu.
func (e *executor) renewLocked(a *attempt, h *standHold, now time.Time, lease time.Duration) (*markerWrite, error) {
	if !now.Before(h.expires) {
		a.holdLost.Store(true)
		return nil, fmt.Errorf("%w: the lease on %s lapsed", tasks.ErrHoldLost, h.key)
	}
	h.expires = now.Add(lease)
	if now.Sub(h.staged) < markerRestage || e.holds[h.key] != h {
		return nil, nil
	}
	h.staged = now
	m := tasks.StandDown{Key: h.key, Holder: a.id, Quiesced: slices.Clone(h.quiesced), Expires: h.expires}
	return &markerWrite{key: h.key, m: &m, ticket: e.markerTicketLocked(h.key)}, nil
}

// markerWrite is one write of a key's stand-down marker (nil clears it) with
// the ticket it was decided under.
type markerWrite struct {
	key    tasks.Key
	m      *tasks.StandDown
	ticket uint64
}

// markerTicketLocked numbers a decision about key's marker; the newest decision
// is the one that stays on disk. Caller holds e.mu.
func (e *executor) markerTicketLocked(key tasks.Key) uint64 {
	e.markerGen[key]++
	return e.markerGen[key]
}

// writeMarkers persists marker decisions. Writes are serialized (markerMu is held
// from the first check to the commit), and each is skipped when a newer decision
// about its key was made since it was decided: a re-staging computed before a
// release can never land after the release's clear, and a clear can never land on
// a newer hold's marker. A skipped write is not an error.
func (e *executor) writeMarkers(ctx context.Context, ws ...markerWrite) error {
	if len(ws) == 0 {
		return nil
	}
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	return e.write(context.WithoutCancel(ctx), func(ctx context.Context, b *storage.Batch) error {
		for _, w := range ws {
			e.mu.Lock()
			stale := e.markerGen[w.key] != w.ticket
			e.mu.Unlock()
			if stale {
				continue
			}
			if err := e.opts.Stores.StandDown.Set(ctx, b, w.key, w.m); err != nil {
				return err
			}
		}
		return nil
	})
}

// reapHolds cleans up holds whose lease expired without renewal: each holder's
// run loses its hold (its writer refuses and its context is canceled with
// tasks.ErrHoldLost), the holders are dropped, the marker is cleared and the runs
// the hold quiesced are re-queued. It does not free the key: the key is free from
// the instant of expiry, because dispatch (dispatchOnce) and keyHeld test
// liveLocked(now) themselves, however long the reaper takes to get here. The
// expiry test and the holders' loss are one critical section, so a renewal that
// extended the lease is either before it (the hold is not dead) or after it (the
// renewal fails): never both.
//
// What it cannot do is stop an fn already running inside a Writer.Do (or a body
// between writes) that outlasts a whole lease: the permit is checked before fn
// runs, not during it, so that fn can still commit after the lapse. That is the
// shape of HEAD's Fence.Check, not something this reaper closes.
func (e *executor) reapHolds(_ context.Context) {
	type dead struct {
		h       *standHold
		holders []*attempt
	}
	e.mu.Lock()
	now := e.now()
	var ds []dead
	for _, h := range e.holds {
		if len(h.holders) > 0 && !now.Before(h.expires) {
			d := dead{h: h, holders: slices.Clone(h.holders)}
			for _, a := range d.holders {
				a.holdLost.Store(true)
			}
			ds = append(ds, d)
		}
	}
	e.mu.Unlock()
	for _, d := range ds {
		for _, a := range d.holders {
			a.cancel(tasks.ErrHoldLost)
		}
		for _, a := range d.holders {
			e.dropHolder(a, d.h)
		}
	}
}
