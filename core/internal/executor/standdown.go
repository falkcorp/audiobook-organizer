// file: core/internal/executor/standdown.go
// version: 1.0.0
// guid: 76fd6f03-58bc-4369-a11e-f3974d8d8409
// last-edited: 2026-11-10

package executor

import (
	"context"
	"errors"
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
}

func (h *holdHandle) Release() { h.release() }

// standDown takes a hold on key for the run a: running holders of key that
// may yield (Common.Yields) are quiesced (stopping{quiesce}, context
// canceled with tasks.ErrQuiesced; their attempt ends interrupted_quiesced),
// new runs of key stay queued while the hold lives, and the marker is
// persisted through Stores.StandDown.
func (e *executor) standDown(a *attempt, key tasks.Key) (tasks.Hold, error) {
	now := e.now()
	lease := e.config().StandDownLease
	e.mu.Lock()
	h := e.holds[key]
	if h == nil || !h.liveLocked(now) {
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
	e.mu.Unlock()
	if err := e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
		return e.opts.Stores.StandDown.Set(ctx, b, key, &marker)
	}); err != nil {
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
	return &holdHandle{release: func() { once.Do(func() { e.dropHolder(a, h) }) }}, nil
}

// dropHolder removes a from h; the last holder clears the marker and
// re-queues the runs the hold quiesced.
func (e *executor) dropHolder(a *attempt, h *standHold) {
	e.mu.Lock()
	h.holders = slices.DeleteFunc(h.holders, func(x *attempt) bool { return x == a })
	a.holds = slices.DeleteFunc(a.holds, func(x *standHold) bool { return x == h })
	last := len(h.holders) == 0
	var quiesced []tasks.RunID
	if last {
		if e.holds[h.key] == h {
			delete(e.holds, h.key)
		}
		quiesced = h.quiesced
		h.quiesced = nil
	}
	e.mu.Unlock()
	if !last {
		return
	}
	if err := e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
		return e.opts.Stores.StandDown.Set(ctx, b, h.key, nil)
	}); err != nil {
		e.log.Warn("stand-down marker not cleared; the next boot treats its runs as stood down", "key", string(h.key), "err", err)
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

// renewHolds renews a's holds (every Writer.Do and item start calls it);
// a hold whose lease already expired is lost (ErrHoldLost). The marker is
// re-staged at most once a second.
func (e *executor) renewHolds(ctx context.Context, a *attempt) error {
	now := e.now()
	lease := e.config().StandDownLease
	e.mu.Lock()
	if a.holdLost {
		e.mu.Unlock()
		return ErrHoldLost
	}
	if len(a.holds) == 0 {
		e.mu.Unlock()
		return nil
	}
	var restage []tasks.StandDown
	for _, h := range a.holds {
		if !now.Before(h.expires) {
			a.holdLost = true
			e.mu.Unlock()
			return ErrHoldLost
		}
		h.expires = now.Add(lease)
		if now.Sub(h.staged) >= markerRestage {
			h.staged = now
			restage = append(restage, tasks.StandDown{Key: h.key, Holder: a.id, Quiesced: slices.Clone(h.quiesced), Expires: h.expires})
		}
	}
	e.mu.Unlock()
	if len(restage) == 0 {
		return nil
	}
	return e.write(context.WithoutCancel(ctx), func(ctx context.Context, b *storage.Batch) error {
		for _, m := range restage {
			if err := e.opts.Stores.StandDown.Set(ctx, b, m.Key, &m); err != nil {
				return err
			}
		}
		return nil
	})
}

// reapHolds releases holds whose lease expired without renewal: their
// keys dispatch again and their quiesced runs are re-queued.
func (e *executor) reapHolds(_ context.Context) {
	now := e.now()
	e.mu.Lock()
	var dead []*standHold
	for _, h := range e.holds {
		if len(h.holders) > 0 && !now.Before(h.expires) {
			dead = append(dead, h)
		}
	}
	e.mu.Unlock()
	for _, h := range dead {
		e.mu.Lock()
		holders := slices.Clone(h.holders)
		for _, a := range holders {
			a.holdLost = true
		}
		e.mu.Unlock()
		for _, a := range holders {
			e.dropHolder(a, h)
		}
	}
}
