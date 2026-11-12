// file: core/outbox/ops.go
// version: 1.0.0
// guid: 4b1a1f7a-0a62-4d4e-8f1d-8d2a0b5f7c33
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
)

// The operator operations of 05-events §5E.11, which F6-04's routes call.
// Each is one guarded batch (the pair's cursor read with GetForUpdate and
// rewritten, so a delivery racing the operator, or a second operator, fails
// the commit and is retried against fresh state) and none audits: the route
// writes its own audit record after the operation returns.

// DeadLetter is one dead-lettered message as an operator sees it. The payload
// is the message's own; the token names why it died (permanent, schema,
// mode_unset, panic, timeout, error), never an error text.
type DeadLetter struct {
	Consumer       string
	Topic          string
	Seq            uint64
	Partition      int
	Key            string
	IdempotencyKey string
	Producer       string
	Payload        json.RawMessage
	StagedAt       time.Time
	DeadAt         time.Time
	Token          string
	Attempts       int
}

func deadLetter(r outboxstore.DeadRow) DeadLetter {
	d := r.Dead
	return DeadLetter{Consumer: r.Consumer, Topic: r.Topic, Seq: r.Seq, Partition: d.Part, Key: d.Msg.Key,
		IdempotencyKey: d.Msg.ID, Producer: d.Msg.Producer, Payload: d.Msg.Payload, StagedAt: d.Msg.StagedAt,
		DeadAt: d.At, Token: d.Token, Attempts: d.Attempts}
}

// ListDead pages through the dead letters in (consumer, topic, seq) order.
// after is the cursor an earlier page returned ("" starts); next is "" on the
// last page. limit is clamped to 1..500.
func (o *Outbox) ListDead(ctx context.Context, after string, limit int) (letters []DeadLetter, next string, err error) {
	limit = min(max(limit, 1), 500)
	var from []byte
	if after != "" {
		if from, err = base64.RawURLEncoding.DecodeString(after); err != nil {
			return nil, "", fmt.Errorf("outbox: dead-letter cursor: %w", err)
		}
	}
	rows, last, err := o.store.ScanDead(ctx, from, limit)
	if err != nil {
		return nil, "", err
	}
	for _, r := range rows {
		letters = append(letters, deadLetter(r))
	}
	if last != nil {
		next = base64.RawURLEncoding.EncodeToString(last)
	}
	return letters, next, nil
}

// knownConsumer reports whether name is declared, registered, or still has
// state in the store.
func (o *Outbox) knownConsumer(ctx context.Context, name string) (bool, error) {
	if o.Declared(name) {
		return true, nil
	}
	o.mu.Lock()
	_, reg := o.consumer[name]
	o.mu.Unlock()
	if reg {
		return true, nil
	}
	cursors, err := o.store.AllCursors(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range cursors {
		if c.Pair.Consumer == name {
			return true, nil
		}
	}
	counts, err := o.store.DeadCounts(ctx)
	if err != nil {
		return false, err
	}
	return counts[name] > 0, nil
}

// missingDead is the 404 of a dead-letter operation: the consumer is unknown,
// or it has no dead letter at seq.
func (o *Outbox) missingDead(ctx context.Context, consumer string) error {
	known, err := o.knownConsumer(ctx, consumer)
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("%w: %s", ErrUnknownConsumer, consumer)
	}
	return ErrDeadNotFound
}

// changed wakes the relay and refreshes status after an operator action.
func (o *Outbox) changed(ctx context.Context) {
	o.opsEpoch.Add(1)
	o.Wake()
	o.RefreshStatus(ctx)
}

// RetryDead puts one dead letter back into the stream for its consumer alone:
// the dead row is deleted and the message re-staged into intake in the same
// batch, keeping its IdempotencyKey, addressed to the consumer (every other
// consumer of the topic acks it without running). It is delivered at the end
// of the stream under a new seq, so it does not return to its key's original
// position. ErrUnknownConsumer or ErrDeadNotFound when there is nothing to
// retry.
func (o *Outbox) RetryDead(ctx context.Context, consumer string, seq uint64) error {
	err := o.mutateDead(ctx, consumer, seq, true)
	if err == nil {
		o.changed(ctx)
	}
	return err
}

// DiscardDead deletes one dead letter for good.
func (o *Outbox) DiscardDead(ctx context.Context, consumer string, seq uint64) error {
	err := o.mutateDead(ctx, consumer, seq, false)
	if err == nil {
		o.changed(ctx)
	}
	return err
}

// mutateDead is RetryDead and DiscardDead. The letter is read through the
// batch with GetForUpdate (LoadDead), so the commit carries a precondition on
// the letter itself on every path: of two operators on one letter, or an
// operator racing the relay, the second commit fails with ErrConflict, and
// RetryOnConflict re-reads, which finds the letter gone (ErrDeadNotFound, the
// answer for an operator that lost). Nothing here depends on the pair still
// having a cursor row.
func (o *Outbox) mutateDead(ctx context.Context, consumer string, seq uint64, restage bool) error {
	topic, _, found, err := o.store.FindDead(ctx, consumer, seq)
	if err != nil {
		return err
	}
	if !found {
		return o.missingDead(ctx, consumer)
	}
	if restage {
		if _, err := o.topic(topic); err != nil {
			return fmt.Errorf("outbox: cannot retry into %s: %w", topic, err)
		}
	}
	err = storage.RetryOnConflict(ctx, settleAttempts, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		cur, ok, err := o.store.LoadDead(ctx, b, consumer, topic, seq)
		if err != nil {
			return err
		}
		if !ok {
			return ErrDeadNotFound
		}
		if err := o.store.StageDeleteDead(b, consumer, topic, seq); err != nil {
			return err
		}
		if restage {
			id, err := o.ids.next()
			if err != nil {
				return err
			}
			rec := outboxstore.Intake{Msg: cur.Msg}
			rec.Msg.To = consumer
			if err := o.store.StageIntake(b, topic, id, rec); err != nil {
				return err
			}
		}
		if h := o.hooks.get(); h.beforeDeadCommit != nil {
			h.beforeDeadCommit(consumer, seq)
		}
		return b.Commit(ctx, storage.Sync)
	})
	if errors.Is(err, storage.ErrConflict) {
		return fmt.Errorf("%w: %s at seq %d", ErrDeadConflict, consumer, seq)
	}
	return err
}

// PurgeBacklog advances every cursor of consumer to its topic's head and
// clears its retries, discarding the messages it has not taken over; it
// returns how many it skipped. It is the only way a disabled module's retained
// messages are discarded (05-events §5E.11). Dead letters stay, and messages
// sequenced after the purge are delivered normally.
func (o *Outbox) PurgeBacklog(ctx context.Context, consumer string) (skipped int, err error) {
	topic, err := o.consumerTopic(ctx, consumer)
	if err != nil {
		return 0, err
	}
	ti, err := o.topic(topic)
	if err != nil {
		return 0, fmt.Errorf("outbox: cannot purge %s: %w", consumer, err)
	}
	for pp := range ti.partitions {
		n, err := o.purgePair(ctx, outboxstore.Pair{Consumer: consumer, Topic: topic, PP: pp})
		skipped += n
		if err != nil {
			return skipped, err
		}
	}
	o.changed(ctx)
	return skipped, nil
}

// consumerTopic finds the one topic a consumer reads: its registration, or its
// cursors.
func (o *Outbox) consumerTopic(ctx context.Context, consumer string) (string, error) {
	o.mu.Lock()
	reg := o.consumer[consumer]
	o.mu.Unlock()
	if reg != nil {
		return reg.topic, nil
	}
	cursors, err := o.store.AllCursors(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range cursors {
		if c.Pair.Consumer == consumer {
			return c.Pair.Topic, nil
		}
	}
	known, err := o.knownConsumer(ctx, consumer)
	if err != nil {
		return "", err
	}
	if !known {
		return "", fmt.Errorf("%w: %s", ErrUnknownConsumer, consumer)
	}
	return "", fmt.Errorf("%w: %s has no backlog (it never ran)", ErrUnknownConsumer, consumer)
}

func (o *Outbox) purgePair(ctx context.Context, p outboxstore.Pair) (int, error) {
	hi, _, err := o.store.ReadHigh(ctx, p.Topic)
	if err != nil {
		return 0, err
	}
	skipped := 0
	err = storage.RetryOnConflict(ctx, settleAttempts, func(ctx context.Context) (err error) {
		b := o.store.OpenBatch()
		defer closeBatch(b, &err)
		st, err := o.store.LoadPair(ctx, b, p)
		if err != nil {
			return err
		}
		if !st.Has {
			skipped = 0
			return nil
		}
		below := 0
		for _, e := range st.Try.Entries {
			if e.Seq <= st.Cur.Seq {
				below++
			}
		}
		n, err := o.store.CountThrough(ctx, p.Topic, p.PP, st.Cur.Seq, hi.High)
		if err != nil {
			return err
		}
		skipped = n + below
		if skipped == 0 && len(st.Try.Entries) == 0 {
			return nil
		}
		st.Cur.Seq = max(st.Cur.Seq, hi.High)
		st.Try.Entries = nil
		if err := o.store.StagePair(b, p, st, o.now()); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if errors.Is(err, storage.ErrConflict) {
		return 0, fmt.Errorf("outbox: purge of %s/%02d kept losing races with delivery: %w", p.Topic, p.PP, err)
	}
	return skipped, err
}
