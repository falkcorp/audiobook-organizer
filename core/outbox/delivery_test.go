// file: core/outbox/delivery_test.go
// version: 1.0.0
// guid: 0c7d3a52-71f3-4d8b-a1de-6b9f42d0e5a3
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// keysOnDifferentPartitions returns two keys the notice topic (two partitions)
// spreads over different partitions.
func keysOnDifferentPartitions(t testing.TB) (a, b string) {
	t.Helper()
	a = "wayne-manor"
	for i := range 200 {
		b = fmt.Sprintf("arkham-%d", i)
		if outboxstore.Partition(a, 2) != outboxstore.Partition(b, 2) {
			return a, b
		}
	}
	t.Fatal("no two keys on different partitions")
	return "", ""
}

// gate fails the Joker's dispatches until released.
type gate struct {
	open  atomic.Bool
	calls atomic.Int64
}

func (g *gate) handler(rc *recorder, consumer string) outbox.HandlerFunc[Dispatch] {
	inner := rc.handler(consumer)
	return func(ctx context.Context, m outbox.Message[Dispatch]) error {
		if m.Payload.Villain == "Joker" && !g.open.Load() {
			g.calls.Add(1)
			return errors.New("the Joker slipped the net")
		}
		return inner(ctx, m)
	}
}

// An ordered consumer takes a partition's messages in seq order, one at a
// time: while the head message is under retry nothing behind it on its
// partition is delivered, and another partition is not held up.
func TestOutbox_PerKeyOrderAndHeadOfLine(t *testing.T) {
	g := &gate{}
	rc := &recorder{}
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) { c.MaxAttempts = 100000 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN, g.handler(rc, consumerN))}}}
		},
	})
	ka, kb := keysOnDifferentPartitions(t)
	r.stage(ka, Dispatch{Villain: "Joker", Block: 1})
	r.stage(ka, Dispatch{Villain: "Penguin", Block: 2})
	r.stage(kb, Dispatch{Villain: "Riddler", Block: 3})

	eventually(t, "the other partition to deliver", func() bool { return rc.count() == 1 })
	if got := rc.villains(); got[0] != "Riddler" {
		t.Fatalf("delivered %v first, want the other partition's Riddler", got)
	}
	eventually(t, "the Joker to be retried", func() bool { return g.calls.Load() >= 3 })
	stays(t, "Penguin held behind the Joker", 100*time.Millisecond, func() bool { return rc.count() == 1 })

	g.open.Store(true)
	eventually(t, "the held messages", func() bool { return rc.count() == 3 })
	got := rc.all()
	if got[1].Villain != "Joker" || got[2].Villain != "Penguin" {
		t.Fatalf("order after release %v, want Joker then Penguin", rc.villains())
	}
	if got[1].Attempt < 4 || got[2].Attempt != 1 {
		t.Fatalf("attempts Joker=%d Penguin=%d, want Joker's retries counted and Penguin's first", got[1].Attempt, got[2].Attempt)
	}
	if got[1].Seq >= got[2].Seq {
		t.Fatalf("seqs %d then %d are not increasing", got[1].Seq, got[2].Seq)
	}
}

// An Unordered consumer does not hold a partition behind a failing message,
// and the failed one is still redelivered until it settles.
func TestOutbox_UnorderedNoHOL(t *testing.T) {
	g := &gate{}
	rc := &recorder{}
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) { c.MaxAttempts = 100000 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, g.handler(rc, consumerN), outbox.Unordered())}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	r.stage("wayne-manor", Dispatch{Villain: "Riddler", Block: 3})

	eventually(t, "the messages behind the Joker", func() bool { return rc.count() == 2 })
	eventually(t, "the Joker to be retried", func() bool { return g.calls.Load() >= 3 })
	g.open.Store(true)
	eventually(t, "the Joker to land", func() bool { return rc.count() == 3 })
	seen := map[string]int{}
	for _, v := range rc.villains() {
		seen[v]++
	}
	if seen["Joker"] != 1 || seen["Penguin"] != 1 || seen["Riddler"] != 1 {
		t.Fatalf("deliveries %v, want each once", seen)
	}
	// The cursor ends at the last message and the retry record is gone.
	eventually(t, "the unordered pair to settle", func() bool {
		return r.count(tryRoot()) == 0
	})
}

// An always-failing consumer gets exactly MaxAttempts deliveries, spaced by at
// least the backoff floor, then the message is dead-lettered and the
// partition moves on: the next message is delivered, and core.outbox reports
// Degraded with the dead_letters reason.
func TestOutbox_DeadLetterAfterMaxAttempts(t *testing.T) {
	var mu sync.Mutex
	var attempts []delivery
	var stamps []time.Time
	next := &recorder{}
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = 3
			c.BackoffMin, c.BackoffMax = 20*time.Millisecond, 40*time.Millisecond
		},
		rand: func() float64 { return 0 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(ctx context.Context, m outbox.Message[Dispatch]) error {
					if m.Payload.Villain == "Penguin" {
						return next.handler(consumerN)(ctx, m)
					}
					mu.Lock()
					defer mu.Unlock()
					attempts = append(attempts, delivery{Attempt: m.Attempt, ID: m.IdempotencyKey})
					stamps = append(stamps, time.Now())
					return errors.New("the vault is empty")
				})}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin"})

	eventually(t, "the message behind the dead letter", func() bool { return next.count() == 1 })
	eventually(t, "core.outbox to report the dead letter", func() bool {
		h := r.healthOf()
		return h.Status == health.StatusDegraded && h.Reason == "dead_letters"
	})
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("%d deliveries, want exactly MaxAttempts=3", len(attempts))
	}
	for i, a := range attempts {
		if a.Attempt != i+1 || a.ID != attempts[0].ID {
			t.Fatalf("attempt %d saw Attempt=%d ID=%s", i, a.Attempt, a.ID)
		}
	}
	for i := 1; i < len(stamps); i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap < 20*time.Millisecond {
			t.Fatalf("attempts %d and %d were %v apart, below the 20ms backoff floor", i, i+1, gap)
		}
	}
	letters, next2, err := r.ob.ListDead(context.Background(), "", 10)
	must(t, err)
	if next2 != "" || len(letters) != 1 {
		t.Fatalf("dead letters %+v next=%q, want exactly the Joker's", letters, next2)
	}
	d := letters[0]
	if d.Consumer != consumerN || d.Token != "error" || d.Attempts != 3 || d.Key != "wayne-manor" || d.IdempotencyKey != attempts[0].ID {
		t.Fatalf("dead letter %+v", d)
	}
	if string(d.Payload) == "" || d.Producer != "core" {
		t.Fatalf("dead letter lost its payload or producer: %+v", d)
	}
}

// What a handler returns decides the stored token and whether a retry is
// pointless: a permanent or schema error dead-letters on the first attempt,
// a panic and a timeout are retried like any failure and never take the relay
// down.
func TestOutbox_FailureTokens(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     func(ctx context.Context) error
		token    string
		attempts int
	}{
		{"permanent", func(context.Context) error { return fmt.Errorf("%w: no such vault", outbox.ErrPermanent) }, "permanent", 1},
		{"schema", func(context.Context) error { return fmt.Errorf("%w: wrong shape", outbox.ErrSchema) }, "schema", 1},
		{"panic", func(context.Context) error { panic("the bat signal fell over") }, "panic", 2},
		{"timeout", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, "timeout", 2},
		{"error", func(context.Context) error { return errors.New("plain") }, "error", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			after := &recorder{}
			r := newRig(t, rigOpts{
				cfg: func(c *outbox.Config) { c.MaxAttempts = 2; c.HandlerTimeout = 30 * time.Millisecond },
				register: func(testing.TB, *rig) []registration {
					return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
						func(ctx context.Context, m outbox.Message[Dispatch]) error {
							if m.Payload.Villain == "Joker" {
								calls.Add(1)
								return tc.fail(ctx)
							}
							return after.handler(consumerN)(ctx, m)
						})}}}
				},
			})
			r.stage("wayne-manor", Dispatch{Villain: "Joker"})
			r.stage("wayne-manor", Dispatch{Villain: "Penguin"})
			eventually(t, "the next message after the dead letter", func() bool { return after.count() == 1 })
			letters, _, err := r.ob.ListDead(context.Background(), "", 10)
			must(t, err)
			if len(letters) != 1 || letters[0].Token != tc.token || letters[0].Attempts != tc.attempts {
				t.Fatalf("dead letters %+v, want one with token %q after %d attempts", letters, tc.token, tc.attempts)
			}
			if int(calls.Load()) != tc.attempts {
				t.Fatalf("handler ran %d times, want %d", calls.Load(), tc.attempts)
			}
		})
	}
}

// Backoff is uniform in [lo, cap) with cap doubling per attempt and bounded by
// hi, including at attempt counts that would overflow a naive shift.
func TestOutbox_BackoffBounds(t *testing.T) {
	lo, hi := time.Second, 5*time.Minute
	for _, attempt := range []int{1, 2, 3, 5, 9, 10, 30, 63, 64, 1000, 1 << 20} {
		for _, u := range []float64{0, 0.25, 0.5, 0.999999} {
			d := outbox.BackoffDelay(attempt, lo, hi, u)
			if d < lo || d > hi {
				t.Fatalf("attempt %d u=%v: delay %v outside [%v, %v]", attempt, u, d, lo, hi)
			}
		}
	}
	if d := outbox.BackoffDelay(1, lo, hi, 0.999999); d > lo+time.Millisecond {
		t.Fatalf("the first retry waits at most the floor, got %v", d)
	}
	if a, b := outbox.BackoffDelay(2, lo, hi, 0.5), outbox.BackoffDelay(6, lo, hi, 0.5); b <= a {
		t.Fatalf("the cap does not grow with attempts: %v then %v", a, b)
	}
	if d := outbox.BackoffDelay(1000, lo, hi, 0); d != lo {
		t.Fatalf("u=0 is the floor, got %v", d)
	}
	if d := outbox.BackoffDelay(7, lo, lo, 0.7); d != lo {
		t.Fatalf("hi == lo pins the delay, got %v", d)
	}
}

// A consumer's own MaxAttempts and Backoff override the config.
func TestOutbox_ConsumerOverridesAttemptsAndBackoff(t *testing.T) {
	var calls atomic.Int64
	var gaps []time.Duration
	var last time.Time
	var mu sync.Mutex
	r := newRig(t, rigOpts{
		cfg:  func(c *outbox.Config) { c.MaxAttempts = 20 },
		rand: func() float64 { return 0 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(context.Context, outbox.Message[Dispatch]) error {
					mu.Lock()
					now := time.Now()
					if !last.IsZero() {
						gaps = append(gaps, now.Sub(last))
					}
					last = now
					mu.Unlock()
					calls.Add(1)
					return errors.New("no")
				}, outbox.MaxAttempts(2), outbox.Backoff(30*time.Millisecond, 60*time.Millisecond))}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "a dead letter", func() bool { l, _, _ := r.ob.ListDead(context.Background(), "", 5); return len(l) == 1 })
	if calls.Load() != 2 {
		t.Fatalf("ran %d times, want the consumer's own MaxAttempts of 2", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gaps) != 1 || gaps[0] < 30*time.Millisecond {
		t.Fatalf("gaps %v, want one of at least the consumer's 30ms floor", gaps)
	}
}

// RetryDead re-stages a dead letter for the one consumer it died on: that
// consumer gets it again under its original IdempotencyKey, every other
// consumer of the topic acks it without running its handler, and the dead
// letter is gone.
func TestOutbox_RetryDeadLetter(t *testing.T) {
	var fixed atomic.Bool
	rn, rn2 := &recorder{}, &recorder{}
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) { c.MaxAttempts = 1 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, func(ctx context.Context, m outbox.Message[Dispatch]) error {
					if !fixed.Load() {
						return errors.New("the Batmobile is in the shop")
					}
					return rn.handler(consumerN)(ctx, m)
				}),
				outbox.Consume(notice(), consumerN2, rn2.handler(consumerN2)),
			}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the second consumer's delivery", func() bool { return rn2.count() == 1 })
	var dead outbox.DeadLetter
	eventually(t, "the dead letter", func() bool {
		l, _, err := r.ob.ListDead(context.Background(), "", 5)
		must(t, err)
		if len(l) == 1 {
			dead = l[0]
		}
		return len(l) == 1
	})
	fixed.Store(true)

	if err := r.ob.RetryDead(context.Background(), "core.notify.nobody", dead.Seq); !errors.Is(err, outbox.ErrUnknownConsumer) {
		t.Fatalf("retry for an unknown consumer: %v, want ErrUnknownConsumer", err)
	}
	if err := r.ob.RetryDead(context.Background(), consumerN, dead.Seq+99); !errors.Is(err, outbox.ErrDeadNotFound) {
		t.Fatalf("retry of a missing dead letter: %v, want ErrDeadNotFound", err)
	}
	must(t, r.ob.RetryDead(context.Background(), consumerN, dead.Seq))
	eventually(t, "the retried delivery", func() bool { return rn.count() == 1 })
	got := rn.all()[0]
	if got.ID != dead.IdempotencyKey || got.Villain != "Joker" || got.Attempt != 1 {
		t.Fatalf("retried delivery %+v, dead letter was %+v", got, dead)
	}
	if got.Seq <= dead.Seq {
		t.Fatalf("retried message kept seq %d; a re-staged message takes a new seq above %d", got.Seq, dead.Seq)
	}
	stays(t, "the other consumer not rerun", 150*time.Millisecond, func() bool { return rn2.count() == 1 })
	l, _, err := r.ob.ListDead(context.Background(), "", 5)
	must(t, err)
	if len(l) != 0 {
		t.Fatalf("dead letters after retry: %+v", l)
	}
	eventually(t, "core.outbox to recover", func() bool { return r.healthOf().Status == health.StatusUp })
	if err := r.ob.RetryDead(context.Background(), consumerN, dead.Seq); !errors.Is(err, outbox.ErrDeadNotFound) {
		t.Fatalf("second retry of the same dead letter: %v, want ErrDeadNotFound", err)
	}
}

// DiscardDead drops a dead letter for good: nothing is redelivered and health
// recovers.
func TestOutbox_DiscardDead(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) { c.MaxAttempts = 1 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(ctx context.Context, m outbox.Message[Dispatch]) error {
					if m.Payload.Villain == "Joker" {
						return errors.New("no")
					}
					return rc.handler(consumerN)(ctx, m)
				})}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	var dead outbox.DeadLetter
	eventually(t, "the dead letter", func() bool {
		l, _, _ := r.ob.ListDead(context.Background(), "", 5)
		if len(l) == 1 {
			dead = l[0]
		}
		return len(l) == 1
	})
	if err := r.ob.DiscardDead(context.Background(), consumerN, dead.Seq+1); !errors.Is(err, outbox.ErrDeadNotFound) {
		t.Fatalf("discard of a missing dead letter: %v", err)
	}
	must(t, r.ob.DiscardDead(context.Background(), consumerN, dead.Seq))
	l, _, err := r.ob.ListDead(context.Background(), "", 5)
	must(t, err)
	if len(l) != 0 {
		t.Fatalf("dead letters after discard: %+v", l)
	}
	eventually(t, "core.outbox to recover", func() bool { return r.healthOf().Status == health.StatusUp })
	stays(t, "no redelivery of a discarded message", 100*time.Millisecond, func() bool { return rc.count() == 0 })
}

// ListDead pages in (consumer, seq) order and its cursor resumes exactly where
// the page ended.
func TestOutbox_ListDeadPages(t *testing.T) {
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) { c.MaxAttempts = 1 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(context.Context, outbox.Message[Dispatch]) error { return errors.New("no") })}}}
		},
	})
	for i := range 5 {
		r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: i})
	}
	eventually(t, "five dead letters", func() bool { l, _, _ := r.ob.ListDead(context.Background(), "", 50); return len(l) == 5 })
	var all []outbox.DeadLetter
	cursor := ""
	pages := 0
	for {
		l, next, err := r.ob.ListDead(context.Background(), cursor, 2)
		must(t, err)
		all = append(all, l...)
		pages++
		if next == "" {
			break
		}
		cursor = next
	}
	if pages != 3 || len(all) != 5 {
		t.Fatalf("%d pages, %d letters, want 3 and 5", pages, len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("pages out of order: %d then %d", all[i-1].Seq, all[i].Seq)
		}
	}
}

// injectIntake commits a raw intake row, as a producer on a different build
// could have staged it.
func (r *rig) injectIntake(topic, id, key string, payload []byte) {
	r.t.Helper()
	b := r.outKS.NewBatch()
	defer b.Close()
	must(r.t, outboxstore.New(r.outKS).StageIntake(b, topic, id, outboxstore.Intake{
		ID: id, Key: key, Payload: payload, StagedAt: time.Now(), Producer: "core"}))
	must(r.t, b.Commit(context.Background(), storage.Sync))
}

// A raw consumer's payload is checked against the topic's declared schema
// before the handler sees it: a payload that does not fit dead-letters at
// once as "schema", the handler never runs on it, and valid messages go on.
func TestOutbox_RawSchemaMismatchDeadLetters(t *testing.T) {
	var raws []outbox.RawMessage
	var mu sync.Mutex
	r := newRig(t, rigOpts{
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(noticeName, consumerN,
				func(_ context.Context, m outbox.RawMessage) error {
					mu.Lock()
					raws = append(raws, m)
					mu.Unlock()
					return nil
				})}}}
		},
	})
	r.injectIntake(noticeName, "01JZZZZZZZZZZZZZZZZZZZZZ01", "wayne-manor", []byte(`{"villain": 5}`))
	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	eventually(t, "the valid message", func() bool { mu.Lock(); defer mu.Unlock(); return len(raws) == 1 })
	eventually(t, "the schema dead letter", func() bool { l, _, _ := r.ob.ListDead(context.Background(), "", 5); return len(l) == 1 })
	l, _, _ := r.ob.ListDead(context.Background(), "", 5)
	if l[0].Token != "schema" || l[0].Attempts != 1 {
		t.Fatalf("dead letter %+v, want token schema after one attempt", l[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(raws) != 1 || string(raws[0].Payload) == `{"villain": 5}` {
		t.Fatalf("handler saw %+v", raws)
	}
}

// Each redelivery of one message carries the same IdempotencyKey, and the
// attempt number counts up.
func TestOutbox_IdempotencyKeyStableAcrossRedelivery(t *testing.T) {
	var mu sync.Mutex
	var seen []delivery
	r := newRig(t, rigOpts{
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(_ context.Context, m outbox.Message[Dispatch]) error {
					mu.Lock()
					defer mu.Unlock()
					seen = append(seen, delivery{ID: m.IdempotencyKey, Attempt: m.Attempt, Seq: m.Seq})
					if m.Attempt < 3 {
						return errors.New("again")
					}
					return nil
				})}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "three deliveries", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 3 })
	mu.Lock()
	defer mu.Unlock()
	for i, d := range seen {
		if d.ID != seen[0].ID || d.Seq != seen[0].Seq || d.Attempt != i+1 {
			t.Fatalf("delivery %d = %+v, first = %+v", i, d, seen[0])
		}
	}
	if len(seen[0].ID) != 26 {
		t.Fatalf("IdempotencyKey %q is not a ULID", seen[0].ID)
	}
}

// Two core consumers of one topic each get every message, in order, on their
// own cursors: one failing does not hold the other.
func TestOutbox_ConsumersAreIndependent(t *testing.T) {
	g := &gate{}
	rn, rn2 := &recorder{}, &recorder{}
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) { c.MaxAttempts = 100000 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, g.handler(rn, consumerN)),
				outbox.Consume(notice(), consumerN2, rn2.handler(consumerN2)),
			}}}
		},
	})
	for i, v := range []string{"Joker", "Penguin", "Riddler"} {
		r.stage("wayne-manor", Dispatch{Villain: v, Block: i})
	}
	eventually(t, "the independent consumer to finish", func() bool { return rn2.count() == 3 })
	if got := rn2.villains(); got[0] != "Joker" || got[1] != "Penguin" || got[2] != "Riddler" {
		t.Fatalf("second consumer saw %v", got)
	}
	if rn.count() != 0 {
		t.Fatalf("first consumer ran past its head-of-line failure: %v", rn.villains())
	}
	g.open.Store(true)
	eventually(t, "the first consumer to catch up", func() bool { return rn.count() == 3 })
}

// A Where predicate that rejects a message acks it without running the
// handler (and without a run, for ConsumeIntoTask), and a later message that
// matches is delivered.
func TestOutbox_WhereAcksUnmatchedWithoutRun(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{
		register: consumeInto(rc, consumerN, outbox.Where(func(m outbox.Message[Dispatch]) bool { return m.Payload.Villain == "Penguin" })),
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "the matching message", func() bool { return rc.count() == 1 })
	if rc.villains()[0] != "Penguin" {
		t.Fatalf("delivered %v", rc.villains())
	}
	eventually(t, "the cursors to pass both messages", func() bool {
		st, err := r.ob.Status(context.Background())
		must(t, err)
		for _, c := range st.Consumers {
			if c.Name == consumerN {
				return c.Enabled && c.Pending == 0
			}
		}
		return false
	})
	l, _, _ := r.ob.ListDead(context.Background(), "", 5)
	if len(l) != 0 {
		t.Fatalf("a filtered message dead-lettered: %+v", l)
	}
}
