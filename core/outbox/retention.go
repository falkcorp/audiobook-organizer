// file: core/outbox/retention.go
// version: 1.0.1
// guid: 6a1a7b3f-3f0e-4d9c-9b47-5f7a4c0f2b75
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
)

// retentionScan bounds the age scan of a topic nobody holds.
const retentionScan = 256

// holder is one consumer holding a topic's messages.
type holder struct {
	name string
	// expired: an orphan (declared nowhere) that has been gone for
	// orphan_retention; it holds nothing and its cursor is dropped.
	expired bool
}

// sweep deletes what no holder needs any more (05-events §5E.5.2 Retention).
//
// A message goes when every consumer in its topic's declared-consumer set has a
// cursor past it: the registered consumers (core's and the enabled modules'),
// and every disabled consumer that has cursors, whose messages are therefore
// kept while its module is off. A declared consumer that never ran has no
// cursor and holds nothing. A topic nobody holds is not "acked by everyone":
// its messages are kept for orphan_retention (by the message's own age). Only
// the cursors of consumer names in neither set (removed from the build) expire,
// orphan_retention after the first sweep that saw the name absent, and the
// messages they alone held are then dropped and counted.
//
// "Absent since" is persisted in the cursor (Cursor.Absent): the cursor's last
// write says when the consumer last ran, not when it left the build, and a
// consumer that sat idle on a quiet topic for longer than orphan_retention
// would otherwise lose its place the moment a build without it booted. While
// the grace period runs the orphan holds everything above its cursor like any
// other holder, and a consumer that comes back has the mark cleared.
//
// A holder's floor is its cursor, or one before its earliest retry entry: an
// unordered consumer's cursor passes a message that is still being retried,
// and the message must outlive the retry.
//
// Each deletion is one batch that re-reads every holder's cursor with
// GetForUpdate and checks the decision against what it read: a live holder's
// floor must still be at or above the seq being deleted, and an expired
// orphan's cursor must still be expired. The floors read before the batch can
// be stale, and the batch's own Expect only guards the value it reads, so
// without that check a floor that went down in between (or a cursor that was
// touched) would be deleted on the strength of an old reading. A decision that
// no longer holds fails the batch (storage.ErrConflict) and the next sweep
// decides again from fresh reads.
func (r *relay) sweep(ctx context.Context) error {
	o := r.o
	cursors, err := o.store.AllCursors(ctx)
	if err != nil {
		return fmt.Errorf("reading cursors: %w", err)
	}
	registered := map[string]*consumerReg{}
	for _, reg := range r.regs {
		registered[reg.name] = reg
	}
	r.reconcileAbsence(ctx, cursors, registered)
	now := o.now()
	// Index the cursors by topic and consumer.
	byTopic := map[string]map[string][]outboxstore.CursorRow{}
	for _, c := range cursors {
		m := byTopic[c.Pair.Topic]
		if m == nil {
			m = map[string][]outboxstore.CursorRow{}
			byTopic[c.Pair.Topic] = m
		}
		m[c.Pair.Consumer] = append(m[c.Pair.Consumer], c)
	}
	orphanExpired := func(name string, rows []outboxstore.CursorRow) bool {
		if registered[name] != nil || o.Declared(name) {
			return false
		}
		for _, c := range rows {
			if !absentLongEnough(c.Cur, now, r.cfg.OrphanRetention) {
				return false
			}
		}
		return true
	}

	handled := map[outboxstore.Pair]bool{}
	for _, topic := range o.sortedTopics() {
		if ctx.Err() != nil || r.stopped() {
			return nil
		}
		ti, err := o.topic(topic)
		if err != nil {
			continue
		}
		hi, found, err := o.store.ReadHigh(ctx, topic)
		if err != nil {
			r.fault("reading the high-water mark of "+topic, err)
			continue
		}
		// A topic whose messages were spread over another partition count is
		// stopped, and the sweep leaves it alone: it would delete by the wrong
		// partitions.
		if !r.partitionsOK(ti, hi, found) {
			continue
		}
		r.mu.Lock()
		_, isStopped := r.topicStop[topic]
		r.mu.Unlock()
		if isStopped {
			continue
		}
		var holders []holder
		seen := map[string]bool{}
		for _, reg := range r.regs {
			if reg.topic == topic {
				holders = append(holders, holder{name: reg.name})
				seen[reg.name] = true
			}
		}
		for name, rows := range byTopic[topic] {
			if !seen[name] {
				holders = append(holders, holder{name: name, expired: orphanExpired(name, rows)})
			}
		}
		slices.SortFunc(holders, func(a, b holder) int {
			switch {
			case a.name < b.name:
				return -1
			case a.name > b.name:
				return 1
			}
			return 0
		})
		for pp := range ti.partitions {
			if err := r.sweepPartition(ctx, ti, pp, holders, handled); err != nil {
				r.fault(fmt.Sprintf("retention of %s/%02d", topic, pp), err)
			}
		}
	}
	// Expired orphans on topics the catalog no longer declares hold no
	// messages worth counting; their cursors are dropped all the same.
	for topic, m := range byTopic {
		for name, rows := range m {
			if !orphanExpired(name, rows) {
				continue
			}
			for _, c := range rows {
				if handled[c.Pair] {
					continue
				}
				if err := r.dropPair(ctx, c.Pair); err != nil {
					r.fault("dropping an orphan cursor of "+topic, err)
				}
			}
		}
	}
	return nil
}

// dropPair deletes an expired orphan's cursor and try record.
func (r *relay) dropPair(ctx context.Context, p outboxstore.Pair) error {
	o := r.o
	if h := o.hooks.get(); h.beforeRetentionBatch != nil {
		h.beforeRetentionBatch(p.Topic, -1)
	}
	return r.gate(ctx, "outbox.retention", p.Topic, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		if err := r.stageDropIfExpired(ctx, b, p); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// stageDropIfExpired stages the removal of an orphan's pair if the cursor read
// through b has still been absent for orphan_retention; a cursor that changed
// since the sweep decided (the consumer came back and its mark was cleared)
// is kept and fails the batch.
func (r *relay) stageDropIfExpired(ctx context.Context, b *storage.Batch, p outboxstore.Pair) error {
	o := r.o
	st, err := o.store.LoadPair(ctx, b, p)
	if err != nil {
		return err
	}
	if st.Has && !absentLongEnough(st.Cur, o.now(), r.cfg.OrphanRetention) {
		return storage.ErrConflict
	}
	return o.store.StageDropPair(ctx, b, p)
}

// absentLongEnough says whether the cursor's consumer has been out of the
// build for the whole orphan grace period.
func absentLongEnough(c outboxstore.Cursor, now time.Time, grace time.Duration) bool {
	return !c.Absent.IsZero() && now.Sub(c.Absent) >= grace
}

// reconcileAbsence keeps every cursor's Absent mark true: a cursor whose
// consumer is in neither the registered nor the declared set is stamped with
// the time of this sweep (the start of its grace period), and a cursor whose
// consumer is back loses the stamp. Each change is its own guarded batch, so a
// writer that got there first fails it and the next sweep looks again. cursors
// is updated in place for what this sweep goes on to decide.
func (r *relay) reconcileAbsence(ctx context.Context, cursors []outboxstore.CursorRow, registered map[string]*consumerReg) {
	o := r.o
	for i := range cursors {
		c := &cursors[i]
		present := registered[c.Pair.Consumer] != nil || o.Declared(c.Pair.Consumer)
		if present == c.Cur.Absent.IsZero() {
			continue
		}
		if ctx.Err() != nil || r.stopped() {
			return
		}
		var want time.Time
		if !present {
			want = o.now().UTC()
		}
		err := r.gate(ctx, "outbox.retention", c.Pair.Topic, func(ctx context.Context) (err error) {
			b := o.store.OpenBatch()
			defer closeBatch(b, &err)
			st, err := o.store.LoadPair(ctx, b, c.Pair)
			if err != nil {
				return err
			}
			if !st.Has || st.Cur.Absent.IsZero() == want.IsZero() {
				return nil // gone, or already as wanted
			}
			st.Cur.Absent = want
			if err := o.store.StagePair(b, c.Pair, st, o.now()); err != nil {
				return err
			}
			return b.Commit(ctx, storage.Sync)
		})
		switch {
		case err == nil:
			c.Cur.Absent = want
		case errors.Is(err, storage.ErrConflict):
			// Another writer moved the cursor: the next sweep reconciles it.
		default:
			r.fault(fmt.Sprintf("marking the absence of %s/%s/%02d", c.Pair.Consumer, c.Pair.Topic, c.Pair.PP), err)
		}
	}
}

// floor is a holder's effective cursor on one partition: the highest seq it no
// longer needs, with ok false when it has no cursor there (it then holds
// everything).
func (r *relay) floor(ctx context.Context, name string, ti *topicInfo, pp int) (uint64, bool, error) {
	st, err := r.o.store.ReadPair(ctx, outboxstore.Pair{Consumer: name, Topic: ti.name, PP: pp})
	if err != nil || !st.Has {
		return 0, false, err
	}
	return floorOf(st), true, nil
}

// floorOf is the floor of a pair that has a cursor: its cursor, lowered to one
// before its earliest retry entry.
func floorOf(st outboxstore.PairState) uint64 {
	f := st.Cur.Seq
	for _, e := range st.Try.Entries {
		if e.Seq > 0 && e.Seq-1 < f {
			f = e.Seq - 1
		}
	}
	return f
}

func (r *relay) sweepPartition(ctx context.Context, ti *topicInfo, pp int, holders []holder, handled map[outboxstore.Pair]bool) error {
	o := r.o
	var live, expired []holder
	for _, h := range holders {
		if h.expired {
			expired = append(expired, h)
		} else {
			live = append(live, h)
		}
	}
	floors := map[string]uint64{}
	haveCursor := map[string]bool{}
	for _, h := range holders {
		f, ok, err := r.floor(ctx, h.name, ti, pp)
		if err != nil {
			return err
		}
		floors[h.name], haveCursor[h.name] = f, ok
	}

	var through uint64 // delete messages at or below this seq
	dropped := 0
	switch {
	case len(live) == 0:
		// Nobody holds the topic: keep for orphan_retention, then drop and count.
		rows, err := o.store.OldestMessages(ctx, ti.name, pp, retentionScan)
		if err != nil {
			return err
		}
		cutoff := o.now().Add(-r.cfg.OrphanRetention)
		for _, m := range rows {
			if !m.Msg.StagedAt.Before(cutoff) {
				break
			}
			through = m.Seq
			dropped++
		}
	default:
		through = ^uint64(0)
		for _, h := range live {
			if !haveCursor[h.name] {
				through = 0 // a consumer that has no cursor yet needs everything
				break
			}
			through = min(through, floors[h.name])
		}
		if len(expired) > 0 {
			lowest := ^uint64(0)
			for _, h := range expired {
				if haveCursor[h.name] {
					lowest = min(lowest, floors[h.name])
				}
			}
			if lowest < through {
				n, err := o.store.CountThrough(ctx, ti.name, pp, lowest, through)
				if err != nil {
					return err
				}
				dropped = n
			}
		}
	}

	hasExpiredCursor := false
	for _, h := range expired {
		hasExpiredCursor = hasExpiredCursor || haveCursor[h.name]
	}
	if through == 0 && !hasExpiredCursor {
		return nil
	}
	if h := o.hooks.get(); h.beforeRetentionBatch != nil {
		h.beforeRetentionBatch(ti.name, pp)
	}
	err := r.gate(ctx, "outbox.retention", ti.name, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		for _, h := range live {
			st, err := o.store.LoadPair(ctx, b, outboxstore.Pair{Consumer: h.name, Topic: ti.name, PP: pp})
			if err != nil {
				return err
			}
			// The deletion rests on this holder's floor reaching through; the
			// batch read it now, so check it still does.
			if through > 0 && (!st.Has || floorOf(st) < through) {
				return storage.ErrConflict
			}
		}
		for _, h := range expired {
			if haveCursor[h.name] {
				if err := r.stageDropIfExpired(ctx, b, outboxstore.Pair{Consumer: h.name, Topic: ti.name, PP: pp}); err != nil {
					return err
				}
			}
		}
		if through > 0 {
			if err := o.store.StageDeleteThrough(b, ti.name, pp, through); err != nil {
				return err
			}
		}
		return b.Commit(ctx, storage.Sync)
	})
	switch {
	case err == nil:
		for _, h := range expired {
			handled[outboxstore.Pair{Consumer: h.name, Topic: ti.name, PP: pp}] = true
		}
		o.inst.orphansDropped(ti.name, dropped)
		if dropped > 0 {
			r.log.Warn("outbox: dropped messages only a removed consumer held", slog.String("topic", ti.name), slog.Int("messages", dropped))
		}
		return nil
	case errors.Is(err, storage.ErrConflict):
		return nil // a cursor moved under the sweep: the next sweep re-reads it
	default:
		return err
	}
}
