// file: core/outbox/settle_test.go
// version: 1.0.0
// guid: 0fb4149f-c7e3-4255-863d-bdd7729edc7a
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// counterTotal sums the samples of one counter family of the provider.
func counterTotal(t testing.TB, p *metrics.Provider, family string) float64 {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	must(t, err)
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
	}
	return sum
}

// A delivery whose message another writer already passed (an operator's purge
// settling the pair while the handler ran) is not an ack and is not counted as
// delivered: the settle batch reads the cursor first and sees it is past the
// message. A message staged after the purge is delivered and counted normally.
func TestOutbox_AckOfAMessageAnotherWriterPassedIsStale(t *testing.T) {
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	must(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("outbox", "topic", "consumer", "state")
	must(t, err)
	inst, err := outbox.NewInstruments(sc)
	must(t, err)
	delivered := metricstest.FamilyPrefix(sc) + "delivered_total"

	var self atomic.Pointer[rig]
	var purged atomic.Bool
	var skipped atomic.Int64
	rc := &recorder{}
	hooks := &outbox.TestHooks{AfterHandler: func(consumer string, _ uint64) {
		if consumer != consumerN || !purged.CompareAndSwap(false, true) {
			return
		}
		n, perr := self.Load().ob.PurgeBacklog(context.Background(), consumerN)
		if perr != nil {
			t.Errorf("purge inside the handler window: %v", perr)
		}
		skipped.Store(int64(n))
	}}
	r := newRig(t, rigOpts{inst: inst, hooks: hooks, register: consumeInto(rc, consumerN)})
	self.Store(r)

	// Two messages of one key sit on one partition: the first is delivered, and
	// the purge it triggers moves the cursor past the second as well.
	b := r.gothamKS.NewBatch()
	must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "wayne-manor", Dispatch{Villain: "Joker", Block: 1}))
	must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "wayne-manor", Dispatch{Villain: "Penguin", Block: 2}))
	must(t, b.Commit(context.Background(), storage.Sync))
	b.Close()
	eventually(t, "the purge to run inside the first delivery", func() bool { return purged.Load() })
	stays(t, "nothing delivered past the purge", 300*time.Millisecond, func() bool { return rc.count() == 1 })
	if got := counterTotal(t, p, delivered); got != 0 {
		t.Fatalf("delivered_total = %v after a delivery whose ack lost to the purge, want 0", got)
	}

	r.stage("wayne-manor", Dispatch{Villain: "Riddler", Block: 3})
	eventually(t, "the message staged after the purge", func() bool { return rc.count() == 2 })
	eventually(t, "the counted delivery", func() bool { return counterTotal(t, p, delivered) >= 1 })
	stays(t, "exactly one counted delivery", 100*time.Millisecond, func() bool { return counterTotal(t, p, delivered) == 1 })
	if got := rc.villains(); got[0] != "Joker" || got[1] != "Riddler" {
		t.Fatalf("delivered %v, want the Joker then the Riddler", got)
	}
}

// A retry is delivered only once its backoff time has passed, also after a
// restart: the retry record carries the time, and a relay that starts with an
// empty memory reads it back instead of redelivering at once.
func TestOutbox_RetryAfterRestartWaitsForBackoff(t *testing.T) {
	clk := &skew{}
	cp := newCrashPoints()
	cfg := func(c *outbox.Config) {
		c.MaxAttempts = 5
		c.BackoffMin, c.BackoffMax = time.Hour, time.Hour
		c.RelayRotate = 24 * time.Hour
	}
	rnd := func() float64 { return 0 }

	var calls atomic.Int64
	r1 := newRig(t, rigOpts{fs: cp.fs, now: clk.now, rand: rnd, cfg: cfg, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
			func(context.Context, outbox.Message[Dispatch]) error {
				calls.Add(1)
				return errors.New("the Joker slipped the net")
			})}}}
	}})
	r1.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	eventually(t, "the failed first attempt", func() bool { return calls.Load() == 1 })
	eventually(t, "its retry record to be committed", func() bool { return r1.count(tryRoot()) == 1 })
	r1.close()

	rc := &recorder{}
	_ = newRig(t, rigOpts{fs: cp.fs, now: clk.now, rand: rnd, cfg: cfg, register: consumeInto(rc, consumerN)})
	stays(t, "no delivery before the backoff time", 300*time.Millisecond, func() bool { return rc.count() == 0 })
	clk.advance(2 * time.Hour)
	eventually(t, "the retry once its time has passed", func() bool { return rc.count() == 1 })
	if got := rc.all()[0]; got.Attempt != 2 || got.Villain != "Joker" {
		t.Fatalf("retried as %+v, want the Joker's second attempt", got)
	}
}

// An unordered consumer that fails everything holds at most maxTryUnordered
// (64) messages under retry, however many are waiting behind them: the bound
// is on the retry record's size, not only on the decision to take more.
func TestOutbox_UnorderedRetryListStaysBounded(t *testing.T) {
	const bound, backlog = 64, 200
	g := &gate{}
	rc := &recorder{}
	clk := &skew{}
	// A long backoff keeps every retry not yet due, so each round has the whole
	// window free for new messages and the record fills to its bound; with short
	// backoffs the due retries would fill the window and starve intake instead.
	r := newRig(t, rigOpts{
		now:  clk.now,
		rand: func() float64 { return 0 },
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = 1000000
			c.BackoffMin, c.BackoffMax = time.Hour, time.Hour
			c.RelayRotate = 24 * time.Hour
		},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, g.handler(rc, consumerN), outbox.Unordered())}}}
		},
	})
	villains := make([]string, backlog)
	for i := range villains {
		villains[i] = "Joker"
	}
	r.stageMany("wayne-manor", villains...)

	pair := outboxstore.Pair{Consumer: consumerN, Topic: noticeName, PP: outboxstore.Partition("wayne-manor", 2)}
	held := func() int {
		st, err := outboxstore.New(r.outKS).ReadPair(context.Background(), pair)
		must(t, err)
		return len(st.Try.Entries)
	}
	most := 0
	sample := func() bool {
		most = max(most, held())
		return most >= bound
	}
	eventually(t, "the retry record to fill", sample)
	stays(t, "the retry record not to grow past its bound", 400*time.Millisecond, func() bool {
		sample()
		return most <= bound
	})
	if most != bound {
		t.Fatalf("the retry record held up to %d entries, want it to fill to exactly %d", most, bound)
	}
	if rc.count() != 0 {
		t.Fatalf("%d messages delivered while the gate was closed", rc.count())
	}

	g.open.Store(true)
	clk.advance(2 * time.Hour)
	eventually(t, "the whole backlog to land", func() bool { return rc.count() == backlog })
	stays(t, "no delivery twice", 100*time.Millisecond, func() bool { return rc.count() == backlog })
}

// A live consumer with no cursor on a partition holds every message of it: the
// retention sweep must not delete what another consumer already took past.
func TestOutbox_RetentionKeepsMessagesForConsumerWithoutCursor(t *testing.T) {
	fast, other := &recorder{}, &recorder{}
	r := newRig(t, rigOpts{sweep: sweepFast, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, fast.handler(consumerN)),
			outbox.Consume(notice(), consumerN2, other.handler(consumerN2)),
		}}}
	}})
	eventually(t, "both consumers to have their cursors", func() bool { return r.cursors(consumerN2) == 2 && r.cursors(consumerN) == 2 })

	// Take the second consumer's cursors away: it is registered and the relay
	// believes it initialised, but on storage it has no cursor yet.
	st := outboxstore.New(r.outKS)
	for pp := range 2 {
		b := st.OpenBatch()
		must(t, st.StageDropPair(context.Background(), b, outboxstore.Pair{Consumer: consumerN2, Topic: noticeName, PP: pp}))
		must(t, b.Commit(context.Background(), storage.Sync))
		b.Close()
	}
	eventually(t, "the cursors to be gone", func() bool { return r.cursors(consumerN2) == 0 })

	for i := range 3 {
		r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: i})
	}
	eventually(t, "the first consumer to take all three", func() bool { return fast.count() == 3 })
	stays(t, "the messages kept for the consumer without a cursor", 400*time.Millisecond, func() bool { return r.messages(noticeName) == 3 })
}
