// file: core/outbox/retention_test.go
// version: 1.0.0
// guid: 3b8f5a21-6d4c-4e90-a7b2-1c9e0f3d8a65
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
)

func (r *rig) messages(topic string) int { return r.count(outboxstore.MsgTopicPrefix(topic)) }

func (r *rig) cursors(consumer string) int { return r.count(outboxstore.CurPrefix(consumer)) }

// sweepFast is the retention interval the tests use.
const sweepFast = 15 * time.Millisecond

// A message is kept until every consumer of its topic took it over: the
// slowest consumer holds the rest, and the sweep deletes the lot once it
// catches up.
func TestOutbox_RetentionKeepsForSlowestHolder(t *testing.T) {
	var open atomic.Bool
	fast, slowRec := &recorder{}, &recorder{}
	r := newRig(t, rigOpts{
		sweep: sweepFast,
		cfg:   func(c *outbox.Config) { c.MaxAttempts = 100000 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, fast.handler(consumerN)),
				outbox.Consume(notice(), consumerN2, func(ctx context.Context, m outbox.Message[Dispatch]) error {
					if !open.Load() {
						return context.DeadlineExceeded
					}
					return slowRec.handler(consumerN2)(ctx, m)
				}),
			}}}
		},
	})
	for i := range 3 {
		r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: i})
	}
	eventually(t, "the fast consumer to take all three", func() bool { return fast.count() == 3 })
	stays(t, "the messages kept for the slow consumer", 200*time.Millisecond, func() bool { return r.messages(noticeName) == 3 })

	open.Store(true)
	eventually(t, "the slow consumer to catch up", func() bool { return slowRec.count() == 3 })
	eventually(t, "the sweep to delete what both took", func() bool { return r.messages(noticeName) == 0 })
	stays(t, "no redelivery from the deletion", 100*time.Millisecond, func() bool { return fast.count() == 3 && slowRec.count() == 3 })
}

// A message a consumer is still retrying outlives its cursor: an unordered
// consumer's cursor passes a failing message, and retention keeps the message
// until the retry settles.
func TestOutbox_RetentionKeepsMessageUnderUnorderedRetry(t *testing.T) {
	g := &gate{}
	rc := &recorder{}
	r := newRig(t, rigOpts{
		sweep: sweepFast,
		cfg:   func(c *outbox.Config) { c.MaxAttempts = 100000 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, g.handler(rc, consumerN), outbox.Unordered())}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	eventually(t, "the Penguin", func() bool { return rc.count() == 1 })
	eventually(t, "the Joker's retries", func() bool { return g.calls.Load() >= 2 })
	stays(t, "the Joker's message retained while it retries", 200*time.Millisecond, func() bool { return r.messages(noticeName) >= 1 })

	g.open.Store(true)
	eventually(t, "the Joker", func() bool { return rc.count() == 2 })
	eventually(t, "retention to catch up", func() bool { return r.messages(noticeName) == 0 })
}

// disabledFixture boots twice: the first boot has consumers N and N2 take two
// messages (so both hold cursors), the second boots without N2's module and
// takes three more. N2 stays declared, so its messages are kept.
func disabledFixture(t *testing.T) (r2 *rig, rn2 *recorder) {
	t.Helper()
	cp := newCrashPoints()
	rn, rn2a := &recorder{}, &recorder{}
	r1 := newRig(t, rigOpts{fs: cp.fs, sweep: sweepFast, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, rn.handler(consumerN)),
			outbox.Consume(notice(), consumerN2, rn2a.handler(consumerN2)),
		}}}
	}})
	r1.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	r1.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	eventually(t, "both consumers to take both", func() bool { return rn.count() == 2 && rn2a.count() == 2 })
	eventually(t, "retention to empty the topic", func() bool { return r1.messages(noticeName) == 0 })
	r1.close()

	rn3 := &recorder{}
	r2 = newRig(t, rigOpts{fs: cp.fs, sweep: sweepFast, register: consumeInto(rn3, consumerN)})
	for i, v := range []string{"Riddler", "Bane", "Scarecrow"} {
		r2.stage("wayne-manor", Dispatch{Villain: v, Block: 10 + i})
	}
	eventually(t, "the enabled consumer to take three", func() bool { return rn3.count() == 3 })
	// The handler returning is not the ack committing: wait for the cursor, or a
	// later boot would (correctly) redeliver the last message.
	eventually(t, "the enabled consumer's acks to commit", func() bool {
		st, err := r2.ob.Status(context.Background())
		must(t, err)
		for _, c := range st.Consumers {
			if c.Name == consumerN {
				return c.Pending == 0
			}
		}
		return false
	})
	return r2, rn2a
}

// A disabled module's consumer keeps its messages: they are not deleted while
// the consumer is declared and has a cursor, and Status lists them as its
// retained backlog.
func TestOutbox_DisabledConsumerRetainsBacklog(t *testing.T) {
	r2, _ := disabledFixture(t)
	stays(t, "the disabled consumer's messages retained", 250*time.Millisecond, func() bool { return r2.messages(noticeName) == 3 })
	st, err := r2.ob.Status(context.Background())
	must(t, err)
	for _, c := range st.Consumers {
		if c.Name != consumerN2 {
			continue
		}
		if c.Enabled || !c.Declared || c.Pending != 3 || c.RetainedBacklog != 3 {
			t.Fatalf("disabled consumer status %+v, want disabled with 3 retained", c)
		}
		return
	}
	t.Fatalf("Status misses the disabled consumer: %+v", st.Consumers)
}

// Re-enabling the module delivers the retained backlog in order, and only then
// is it deleted.
func TestOutbox_DisabledConsumerCatchesUpOnReenable(t *testing.T) {
	r2, _ := disabledFixture(t)
	r2.close()

	rn, rn2 := &recorder{}, &recorder{}
	r3 := newRig(t, rigOpts{fs: r2.fs, sweep: sweepFast, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, rn.handler(consumerN)),
			outbox.Consume(notice(), consumerN2, rn2.handler(consumerN2)),
		}}}
	}})
	eventually(t, "the backlog", func() bool { return rn2.count() == 3 })
	got := rn2.villains()
	if got[0] != "Riddler" || got[1] != "Bane" || got[2] != "Scarecrow" {
		t.Fatalf("backlog delivered as %v", got)
	}
	eventually(t, "retention to delete what everyone took", func() bool { return r3.messages(noticeName) == 0 })
	stays(t, "the enabled consumer not rerun", 100*time.Millisecond, func() bool { return rn.count() == 0 })
}

// PurgeBacklog is the only way a disabled consumer's messages are discarded:
// it returns how many it skipped, retention then deletes them, and nothing is
// ever delivered to the consumer for them.
func TestOutbox_PurgeBacklogDiscardsDisabledConsumersMessages(t *testing.T) {
	r2, _ := disabledFixture(t)
	if _, err := r2.ob.PurgeBacklog(context.Background(), "core.notify.nobody"); err == nil {
		t.Fatal("a purge of an unknown consumer succeeded")
	}
	skipped, err := r2.ob.PurgeBacklog(context.Background(), consumerN2)
	must(t, err)
	if skipped != 3 {
		t.Fatalf("purge skipped %d, want 3", skipped)
	}
	eventually(t, "retention to delete the purged messages", func() bool { return r2.messages(noticeName) == 0 })
	if again, err := r2.ob.PurgeBacklog(context.Background(), consumerN2); err != nil || again != 0 {
		t.Fatalf("second purge = %d, %v; want 0, nil", again, err)
	}
	st, err := r2.ob.Status(context.Background())
	must(t, err)
	for _, c := range st.Consumers {
		if c.Name == consumerN2 && c.Pending != 0 {
			t.Fatalf("pending %d after the purge", c.Pending)
		}
	}
}

// A topic nobody consumes keeps its messages for orphan_retention, then drops
// them; before that nothing is deleted.
func TestOutbox_NoHolderTopicKeptThenDropped(t *testing.T) {
	clk := &skew{}
	r := newRig(t, rigOpts{
		now: clk.now, sweep: sweepFast,
		cfg:      func(c *outbox.Config) { c.RelayRotate = 24 * time.Hour; c.OrphanRetention = time.Hour },
		register: func(testing.TB, *rig) []registration { return nil },
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the message to be sequenced", func() bool { return r.messages(noticeName) == 1 })
	stays(t, "an unconsumed topic keeps its messages", 200*time.Millisecond, func() bool { return r.messages(noticeName) == 1 })
	clk.advance(2 * time.Hour)
	eventually(t, "the sweep to drop the aged message", func() bool { return r.messages(noticeName) == 0 })
}

// The cursors of a consumer removed from the build expire after
// orphan_retention: until then they hold messages, after it the messages only
// they held are dropped.
func TestOutbox_RemovedConsumerExpires(t *testing.T) {
	clk := &skew{}
	cfg := func(c *outbox.Config) { c.RelayRotate = 24 * time.Hour; c.OrphanRetention = time.Hour }
	cp := newCrashPoints()
	rn, rg := &recorder{}, &recorder{}
	r1 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: cfg, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, rn.handler(consumerN)),
			outbox.Consume(notice(), consumerN3, rg.handler(consumerN3)),
		}}}
	}})
	r1.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "both to take it", func() bool { return rn.count() == 1 && rg.count() == 1 })
	eventually(t, "the topic to empty", func() bool { return r1.messages(noticeName) == 0 })
	r1.close()

	// The second build no longer declares consumerN3 anywhere.
	rn2 := &recorder{}
	r2 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, sweep: sweepFast, cfg: cfg,
		core: []string{consumerN, consumerN2}, register: consumeInto(rn2, consumerN)})
	if n := r2.cursors(consumerN3); n == 0 {
		t.Fatal("the removed consumer has no cursors to expire")
	}
	r2.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "the enabled consumer to take it", func() bool { return rn2.count() == 1 })
	stays(t, "the removed consumer's cursors still hold the message", 200*time.Millisecond, func() bool { return r2.messages(noticeName) == 1 })

	clk.advance(2 * time.Hour)
	eventually(t, "the removed consumer's cursors to expire", func() bool { return r2.cursors(consumerN3) == 0 })
	eventually(t, "the message only it held to be dropped", func() bool { return r2.messages(noticeName) == 0 })
}
