// file: core/outbox/health_dead_test.go
// version: 1.2.0
// guid: b584e6da-2910-4648-aa3d-af25cc239b55
// last-edited: 2026-11-15

package outbox_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
)

// An operator's retry of a dead letter that lands after the relay committed it
// and before the relay told the health component must leave core.outbox Up once
// the retry delivers. The relay used to count the letter in memory after its
// commit, so a retry in that gap was undone by a late increment and the
// component stayed Degraded until the relay's next status refresh, fifteen
// seconds later. The hook runs the retry inside the gap on every run, for an
// ordered consumer (settleFail) and an unordered one (settleRound). The relay's
// periodic refresh is pushed past the test (statusEvery), so a report that
// leaves the component Up comes from the retry or the recount, not from the
// refresh that would otherwise correct a wrong count fifteen seconds later.
func TestOutbox_HealthRecoversWhenARetryBeatsTheDeadCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []outbox.ConsumerOption
		hook func(rg *atomic.Pointer[rig], retry func(seq uint64)) outbox.TestHooks
	}{
		{"ordered", nil, func(_ *atomic.Pointer[rig], retry func(uint64)) outbox.TestHooks {
			return outbox.TestHooks{AfterFailCommit: func(consumer string, seq uint64) {
				if consumer == consumerN {
					retry(seq)
				}
			}}
		}},
		{"unordered", []outbox.ConsumerOption{outbox.Unordered()}, func(rg *atomic.Pointer[rig], retry func(uint64)) outbox.TestHooks {
			return outbox.TestHooks{AfterAckCommit: func(consumer string, _ uint64) {
				if consumer != consumerN {
					return
				}
				l, _, err := rg.Load().ob.ListDead(context.Background(), "", 5)
				if err == nil && len(l) == 1 {
					retry(l[0].Seq)
				}
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var self atomic.Pointer[rig]
			var fixed, retried atomic.Bool
			rc := &recorder{}
			retry := func(seq uint64) {
				if !retried.CompareAndSwap(false, true) {
					return
				}
				fixed.Store(true)
				if err := self.Load().ob.RetryDead(context.Background(), consumerN, seq); err != nil {
					t.Errorf("retry the dead letter from inside the gap: %v", err)
				}
			}
			hooks := tc.hook(&self, retry)
			r := newRig(t, rigOpts{
				cfg:         func(c *outbox.Config) { c.MaxAttempts = 1 },
				statusEvery: time.Hour,
				hooks:       &hooks,
				register: func(testing.TB, *rig) []registration {
					return []registration{{owner: "core", hs: []outbox.Handler{
						outbox.Consume(notice(), consumerN, func(ctx context.Context, m outbox.Message[Dispatch]) error {
							if !fixed.Load() {
								return errors.New("the Batmobile is in the shop")
							}
							return rc.handler(consumerN)(ctx, m)
						}, tc.opts...),
					}}}
				},
			})
			self.Store(r)
			// The relay refreshes status at the end of its first loop and then every
			// fifteen seconds. Waiting for that first report keeps it from correcting
			// the count by accident when the dead-lettering falls into the first loop.
			eventually(t, "the relay's first status report", func() bool { return r.healthOf().Status == health.StatusUp })
			r.stage("wayne-manor", Dispatch{Villain: "Joker"})
			eventually(t, "the retried delivery", func() bool { return rc.count() == 1 })
			eventually(t, "core.outbox to be Up with no dead letter left", func() bool {
				l, _, err := r.ob.ListDead(context.Background(), "", 5)
				must(t, err)
				return len(l) == 0 && r.healthOf().Status == health.StatusUp
			})
			// Up is reported inside the gap too (the retry's own refresh), before the
			// late increment of the old code; the state that matters is the one that
			// holds afterwards.
			stays(t, "core.outbox staying Up", 200*time.Millisecond, func() bool { return r.healthOf().Status == health.StatusUp })
			if !retried.Load() {
				t.Fatal("the hook never saw the dead letter, so the gap was not exercised")
			}
		})
	}
}

// A message that dead-letters turns core.outbox Degraded at once, for an ordered
// consumer (settleFail) and an unordered one (settleRound), from the recount the
// dead-lettering itself runs, not from the relay's next periodic refresh (pushed
// past the test). Without that recount in settleRound the component stays Up
// until the refresh; this is the test that fails when it is skipped.
func TestOutbox_DeadLetterDegradesHealthWithoutWaitingForTheRefresh(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []outbox.ConsumerOption
	}{
		{"ordered", nil},
		{"unordered", []outbox.ConsumerOption{outbox.Unordered()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOpts{
				cfg:         func(c *outbox.Config) { c.MaxAttempts = 1 },
				statusEvery: time.Hour,
				register: func(testing.TB, *rig) []registration {
					return []registration{{owner: "core", hs: []outbox.Handler{
						outbox.Consume(notice(), consumerN, func(context.Context, outbox.Message[Dispatch]) error {
							return errors.New("the Batmobile is in the shop")
						}, tc.opts...),
					}}}
				},
			})
			eventually(t, "the relay's first status report", func() bool { return r.healthOf().Status == health.StatusUp })
			r.stage("wayne-manor", Dispatch{Villain: "Joker"})
			eventually(t, "core.outbox to be Degraded by the dead letter", func() bool {
				res := r.healthOf()
				return res.Status == health.StatusDegraded && res.Reason == "dead_letters"
			})
			if l, _, err := r.ob.ListDead(context.Background(), "", 5); err != nil || len(l) != 1 {
				t.Fatalf("ListDead = %d letters, %v; want the one dead letter", len(l), err)
			}
		})
	}
}

// Two recounts that overlap cannot publish the older count last. The first
// recount reads one dead letter and is held before it publishes; the letter is
// removed and a second recount starts. Whatever the second does, the component
// must end up reporting the newest count (none), not the first recount's late
// publication of the one it read. This holds only because a recount reads the
// store inside the lock that publishes it: with the read outside, the second
// reads zero, publishes Up, and the held first one then publishes Degraded last.
func TestOutbox_OverlappingRecountsCannotPublishAnOlderCountLast(t *testing.T) {
	var calls atomic.Int32
	reached, release := make(chan struct{}), make(chan struct{})
	hooks := &outbox.TestHooks{AfterDeadRead: func() {
		if calls.Add(1) == 1 {
			close(reached)
			<-release
		}
	}}
	r := newRig(t, rigOpts{noExec: true, hooks: hooks, register: consumeInto(&recorder{}, consumerN)})
	seedDead(t, r, consumerN, 7, true)
	ctx := context.Background()

	first := make(chan struct{})
	go func() { defer close(first); r.ob.RefreshStatus(ctx) }()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the first recount never reached its hook")
	}
	// The letter is removed behind the held recount's back, counter and all.
	st := outboxstore.New(r.outKS)
	b := st.OpenBatch()
	found, err := st.StageDeleteDead(ctx, b, consumerN, noticeName, 7)
	must(t, err)
	if !found {
		t.Fatal("the seeded dead letter is not there to remove")
	}
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()

	second := make(chan struct{})
	go func() { defer close(second); r.ob.RefreshStatus(ctx) }()
	// A correct recount waits for the first one's lock; a wrong one finishes now.
	// Either way the first is released afterwards and both must complete.
	select {
	case <-second:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for _, done := range []chan struct{}{first, second} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a recount never finished")
		}
	}
	if res := r.healthOf(); res.Status != health.StatusUp {
		t.Fatalf("core.outbox = %s (%s) after both recounts, want Up: the older count was published last", res.Status, res.Reason)
	}
}

// plantLetter writes a dead letter of consumerN straight into dead/, with no
// counter: the shape of a store written before the counters existed.
func plantLetter(t *testing.T, r *rig, seq uint64, part int) {
	t.Helper()
	d := outboxstore.Dead{
		Msg:  outboxstore.Msg{ID: "01J0000000000000000000BAT1", Key: "wayne-manor", Payload: rawJSON(t, Dispatch{Villain: "Joker"}), StagedAt: time.Now(), Producer: "core"},
		Part: part, Token: "error", Attempts: 3, At: time.Now(),
	}
	raw, err := codec.JSON[outboxstore.Dead](outboxstore.RecordVersion).Encode(d)
	must(t, err)
	r.putRaw(outboxstore.DeadKey(consumerN, noticeName, seq), raw)
}

// A store whose dead letters predate the counters is counted correctly from the
// first read, and an operator's retry of one of them lowers the count by one:
// the counters are built from the letters (once, in one batch) before anything
// reads or changes them, so a retry never lowers a counter that was never
// raised.
func TestOutbox_DeadCountersAreBuiltFromLettersThatPredateThem(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true, register: consumeInto(&recorder{}, consumerN)})
	plantLetter(t, r, 7, 0)
	plantLetter(t, r, 8, 1)
	plantLetter(t, r, 9, 1)
	if n := r.count(outboxstore.DeadCountRoot()); n != 0 {
		t.Fatalf("%d counters before the first read, want none: the store should look like one from before they existed", n)
	}
	ctx := context.Background()
	st, err := r.ob.Status(ctx)
	must(t, err)
	if got := deadOf(st, consumerN); got != 3 {
		t.Fatalf("Status counts %d dead letters, want 3", got)
	}
	if n := r.count(outboxstore.DeadCountRoot()); n != 2 {
		t.Fatalf("%d counters after the first read, want 2 (one per partition that holds a letter)", n)
	}
	must(t, r.ob.RetryDead(ctx, consumerN, 8))
	if st, err = r.ob.Status(ctx); err != nil || deadOf(st, consumerN) != 2 {
		t.Fatalf("after a retry Status counts %d (%v), want 2", deadOf(st, consumerN), err)
	}
	must(t, r.ob.DiscardDead(ctx, consumerN, 9))
	must(t, r.ob.DiscardDead(ctx, consumerN, 7))
	if st, err = r.ob.Status(ctx); err != nil || deadOf(st, consumerN) != 0 {
		t.Fatalf("with every letter gone Status counts %d (%v), want 0", deadOf(st, consumerN), err)
	}
	if n := r.count(outboxstore.DeadCountRoot()); n != 0 {
		t.Fatalf("%d counters left with no letters, want none", n)
	}
	if res := r.healthOf(); res.Status != health.StatusUp {
		t.Fatalf("core.outbox = %s (%s) with no dead letter left, want Up", res.Status, res.Reason)
	}
}

// A counter that has drifted from its letters (an older build wrote a letter
// without it, a row was edited by hand) is found by the status refresh and
// rewritten from the letters, and the log names both numbers; it is never
// overwritten without saying so.
func TestOutbox_RefreshStatusRepairsADriftedDeadCounterAndSaysSo(t *testing.T) {
	logs := &logCapture{}
	r := newRig(t, rigOpts{noExec: true, log: logs.logger(), register: consumeInto(&recorder{}, consumerN)})
	seedDead(t, r, consumerN, 7, true)
	ctx := context.Background()
	if _, err := r.ob.Status(ctx); err != nil { // builds the counters
		t.Fatal(err)
	}
	// Drift: the letter stays, its counter goes.
	pp := outboxstore.Partition("wayne-manor", 2)
	b := r.outKS.NewIndexedBatch()
	r.guard(ctx, b, outboxstore.DeadCountKey(consumerN, noticeName, pp))
	must(t, b.Delete(outboxstore.DeadCountKey(consumerN, noticeName, pp)))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	if st, err := r.ob.Status(ctx); err != nil || deadOf(st, consumerN) != 0 {
		t.Fatalf("setup: with the counter gone Status counts %d (%v), want 0 (the drift)", deadOf(st, consumerN), err)
	}

	r.ob.RefreshStatus(ctx)
	if st, err := r.ob.Status(ctx); err != nil || deadOf(st, consumerN) != 1 {
		t.Fatalf("after the refresh Status counts %d (%v), want 1", deadOf(st, consumerN), err)
	}
	if res := r.healthOf(); res.Status != health.StatusDegraded || res.Reason != "dead_letters" {
		t.Fatalf("core.outbox = %s (%s) after the refresh, want Degraded for the letter", res.Status, res.Reason)
	}
	if n := logs.count("dead-letter counter did not match its letters"); n != 1 {
		t.Fatalf("the repair was logged %d times, want once:\n%s", n, logs.text())
	}
	if !strings.Contains(logs.text(), "counter=0") || !strings.Contains(logs.text(), "letters=1") {
		t.Fatalf("the log does not name both numbers:\n%s", logs.text())
	}
	r.ob.RefreshStatus(ctx)
	if n := logs.count("dead-letter counter did not match its letters"); n != 1 {
		t.Fatalf("a second refresh logged the repair again (%d lines):\n%s", n, logs.text())
	}
}

func deadOf(st outbox.Status, consumer string) int {
	for _, c := range st.Consumers {
		if c.Name == consumer {
			return c.Dead
		}
	}
	return -1
}
