// file: core/outbox/operators_test.go
// version: 1.0.0
// guid: 79b776b8-4d28-4cfd-addc-c1ac58312a45
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// rewritePair rewrites a pair's cursor through the guard protocol (LoadPair,
// edit, StagePair), the way a writer other than the relay would.
func rewritePair(t testing.TB, r *rig, p outboxstore.Pair, at time.Time, edit func(*outboxstore.PairState)) {
	t.Helper()
	ctx := context.Background()
	st := outboxstore.New(r.outKS)
	b := st.OpenBatch()
	defer b.Close()
	ps, err := st.LoadPair(ctx, b, p)
	must(t, err)
	edit(&ps)
	must(t, st.StagePair(b, p, ps, at))
	must(t, b.Commit(ctx, storage.Sync))
}

// seedDead writes one dead letter of consumer on the notice topic, and, when
// withCursor, the consumer's cursors too. Without them the pair has no cursor
// row, the shape of a consumer whose cursors expired while its letters stayed.
func seedDead(t testing.TB, r *rig, consumer string, seq uint64, withCursor bool) {
	t.Helper()
	ctx := context.Background()
	st := outboxstore.New(r.outKS)
	now := time.Now()
	b := st.OpenBatch()
	must(t, st.StageDead(ctx, b, consumer, noticeName, seq, outboxstore.Dead{
		Msg:  outboxstore.Msg{ID: "01J0000000000000000000BAT1", Key: "wayne-manor", Payload: rawJSON(t, Dispatch{Villain: "Joker"}), StagedAt: now, Producer: "core"},
		Part: outboxstore.Partition("wayne-manor", 2), Token: "error", Attempts: 3, At: now,
	}))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	if !withCursor {
		return
	}
	cb := st.OpenBatch()
	defer cb.Close()
	_, err := st.StageCursorInit(ctx, cb, consumer, noticeName, 2, false, now)
	must(t, err)
	must(t, cb.Commit(ctx, storage.Sync))
}

// A dead letter is acted on once: of two operators on the same letter, whatever
// they ask for, exactly one succeeds and the other is told the letter is gone;
// a retry is never replayed twice and a replay never races a discard into
// both happening. The seam holds the first operator after it read the letter
// and staged its work, lets the second finish, then releases the first, whose
// commit must fail. It runs with and without a cursor row for the pair: the
// guard is the letter's own key, not the cursor.
func TestOutbox_ConcurrentDeadOperatorsActOnce(t *testing.T) {
	type op func(ob *outbox.Outbox) error
	const seq = 7
	retry := func(ob *outbox.Outbox) error { return ob.RetryDead(context.Background(), consumerN, seq) }
	discard := func(ob *outbox.Outbox) error { return ob.DiscardDead(context.Background(), consumerN, seq) }
	for _, shape := range []struct {
		name       string
		withCursor bool
	}{{"pair has a cursor", true}, {"pair has no cursor", false}} {
		for _, tc := range []struct {
			name          string
			first, second op
			replays       int // intake rows the winner (the second) leaves
		}{
			{"retry then retry", retry, retry, 1},
			{"retry then discard", retry, discard, 0},
			{"discard then retry", discard, retry, 1},
			{"discard then discard", discard, discard, 0},
		} {
			t.Run(shape.name+"/"+tc.name, func(t *testing.T) {
				reached, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int64
				hooks := &outbox.TestHooks{BeforeDeadCommit: func(string, uint64) {
					if calls.Add(1) == 1 {
						close(reached)
						<-release
					}
				}}
				r := newRig(t, rigOpts{noExec: true, hooks: hooks, register: consumeInto(&recorder{}, consumerN)})
				seedDead(t, r, consumerN, seq, shape.withCursor)

				firstErr := make(chan error, 1)
				go func() { firstErr <- tc.first(r.ob) }()
				select {
				case <-reached:
				case <-time.After(10 * time.Second):
					t.Fatal("the first operator never reached its commit")
				}
				if err := tc.second(r.ob); err != nil {
					t.Fatalf("the second operator, which committed first: %v", err)
				}
				close(release)
				if err := <-firstErr; !errors.Is(err, outbox.ErrDeadNotFound) {
					t.Fatalf("the first operator = %v, want ErrDeadNotFound (its commit lost to the second)", err)
				}
				if got := calls.Load(); got != 2 {
					t.Fatalf("%d commits were attempted, want the two operators' one each", got)
				}
				if n := r.count(outboxstore.DeadRoot()); n != 0 {
					t.Fatalf("%d dead letters left", n)
				}
				if n := r.count(outboxstore.InRoot()); n != tc.replays {
					t.Fatalf("%d intake rows after the two operations, want %d", n, tc.replays)
				}
			})
		}
	}
}

// An operator whose commit keeps losing to other writers of the same letter
// gives up with ErrDeadConflict, a clear "it changed under you, list it again",
// and has replayed or removed nothing. Here every attempt is beaten by a
// rewrite of the letter that lands between the operator's read and its commit.
func TestOutbox_DeadOperatorThatKeepsLosingGetsAClearConflict(t *testing.T) {
	const seq = 7
	var self atomic.Pointer[rig]
	var calls atomic.Int64
	hooks := &outbox.TestHooks{BeforeDeadCommit: func(string, uint64) {
		n := calls.Add(1)
		r := self.Load()
		ctx := context.Background()
		st := outboxstore.New(r.outKS)
		b := st.OpenBatch()
		defer b.Close()
		must(t, st.StageDead(ctx, b, consumerN, noticeName, seq, outboxstore.Dead{
			Msg:  outboxstore.Msg{ID: "01J0000000000000000000BAT1", Key: "wayne-manor", Payload: rawJSON(t, Dispatch{Villain: "Joker"}), Producer: "core"},
			Part: outboxstore.Partition("wayne-manor", 2), Token: "error", Attempts: int(n), At: time.Now(),
		}))
		must(t, b.Commit(ctx, storage.Sync))
	}}
	r := newRig(t, rigOpts{noExec: true, hooks: hooks, register: consumeInto(&recorder{}, consumerN)})
	self.Store(r)
	seedDead(t, r, consumerN, seq, true)

	err := r.ob.RetryDead(context.Background(), consumerN, seq)
	if !errors.Is(err, outbox.ErrDeadConflict) {
		t.Fatalf("RetryDead = %v, want ErrDeadConflict", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("%d commit attempts, want the operator to have retried", calls.Load())
	}
	if n := r.count(outboxstore.DeadRoot()); n != 1 {
		t.Fatalf("%d dead letters, want the one still there", n)
	}
	if n := r.count(outboxstore.InRoot()); n != 0 {
		t.Fatalf("%d intake rows, want none replayed", n)
	}
}

// The retention sweep deletes on the strength of the floors it read, and its
// batch must check them again against what the batch itself reads: a holder
// whose cursor went back between the two keeps the messages it needs. Here the
// second consumer's cursor is set back to 0 after the sweep decided to delete
// the one message both had taken.
func TestOutbox_RetentionRechecksFloorsInItsBatch(t *testing.T) {
	var self atomic.Pointer[rig]
	var once atomic.Bool
	hooks := &outbox.TestHooks{BeforeRetentionBatch: func(topic string, pp int) {
		if topic != noticeName || !once.CompareAndSwap(false, true) {
			return
		}
		rewritePair(t, self.Load(), outboxstore.Pair{Consumer: consumerN2, Topic: noticeName, PP: pp}, time.Now(),
			func(ps *outboxstore.PairState) { ps.Cur.Seq = 0 })
	}}
	rc1, rc2 := &recorder{}, &recorder{}
	r := newRig(t, rigOpts{sweep: sweepFast, hooks: hooks, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, rc1.handler(consumerN)),
			outbox.Consume(notice(), consumerN2, rc2.handler(consumerN2)),
		}}}
	}})
	self.Store(r)

	r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	eventually(t, "both consumers to take the message", func() bool { return rc1.count() == 1 && rc2.count() == 1 })
	eventually(t, "the sweep to decide and its batch to run", func() bool { return once.Load() })
	stays(t, "the message kept for the consumer whose cursor went back", 400*time.Millisecond, func() bool { return r.messages(noticeName) == 1 })

	// The topic grows, the second consumer takes what its cursor says it still
	// needs, and only then is everything deleted.
	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	eventually(t, "the second consumer to take the Joker again and the Penguin", func() bool { return rc2.count() == 3 })
	eventually(t, "retention to catch up", func() bool { return r.messages(noticeName) == 0 })
	if got := rc2.villains(); got[0] != "Joker" || got[1] != "Joker" || got[2] != "Penguin" {
		t.Fatalf("second consumer saw %v", got)
	}
	if got := rc1.villains(); len(got) != 2 || got[0] != "Joker" || got[1] != "Penguin" {
		t.Fatalf("first consumer saw %v", got)
	}
}

// An orphan's cursor is dropped only if it is still expired when the batch
// reads it: one touched after the sweep decided (an operator's purge of its
// backlog) is kept, and dropped by a later sweep once it has expired again.
func TestOutbox_OrphanCursorTouchedAfterTheSweepDecidedIsKept(t *testing.T) {
	clk := &skew{}
	cfg := func(c *outbox.Config) { c.RelayRotate = 24 * time.Hour; c.OrphanRetention = time.Hour }
	cp := newCrashPoints()
	r1 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: cfg, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, (&recorder{}).handler(consumerN)),
			outbox.Consume(notice(), consumerN3, (&recorder{}).handler(consumerN3)),
		}}}
	}})
	eventually(t, "both consumers' cursors", func() bool { return r1.cursors(consumerN) == 2 && r1.cursors(consumerN3) == 2 })
	r1.close()

	var self atomic.Pointer[rig]
	var once atomic.Bool
	hooks := &outbox.TestHooks{BeforeRetentionBatch: func(string, int) {
		if !once.CompareAndSwap(false, true) {
			return
		}
		for pp := range 2 {
			rewritePair(t, self.Load(), outboxstore.Pair{Consumer: consumerN3, Topic: noticeName, PP: pp}, clk.now(), func(*outboxstore.PairState) {})
		}
	}}
	// The second build no longer declares consumerN3 anywhere.
	r2 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: cfg, hooks: hooks,
		core: []string{consumerN, consumerN2}, register: consumeInto(&recorder{}, consumerN)})
	self.Store(r2)
	if n := r2.cursors(consumerN3); n != 2 {
		t.Fatalf("the removed consumer has %d cursors to expire, want 2", n)
	}

	clk.advance(2 * time.Hour)
	eventually(t, "the sweep to decide the orphan's cursors expired", func() bool { return once.Load() })
	stays(t, "the touched cursors kept", 400*time.Millisecond, func() bool { return r2.cursors(consumerN3) == 2 })
	clk.advance(2 * time.Hour)
	eventually(t, "the cursors to expire again and be dropped", func() bool { return r2.cursors(consumerN3) == 0 })
}
