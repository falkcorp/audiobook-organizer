// file: core/outbox/health_dead_test.go
// version: 1.4.0
// guid: b584e6da-2910-4648-aa3d-af25cc239b55
// last-edited: 2026-11-15

package outbox_test

import (
	"context"
	"errors"
	"fmt"
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

// Dead-lettering through the relay reads a number of keys linear in the number
// of messages, whatever the store already holds. The relay builds the counters
// from the letters that predate them once, when it starts, and then every dead
// letter's recount reads one row per partition that holds letters. A recount
// that scanned the letters (or an audit run per message, or a build that was not
// latched) reads every stored letter each time: here that is about thirty
// thousand keys for the messages below, against about a thousand.
func TestOutbox_DeadLetteringThroughTheRelayReadsKeysLinearly(t *testing.T) {
	const legacy, messages = 50, 200
	r := newRig(t, rigOpts{
		noRelay:     true,
		cfg:         func(c *outbox.Config) { c.MaxAttempts = 1 },
		statusEvery: time.Hour,
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, func(context.Context, outbox.Message[Dispatch]) error {
					return errors.New("the Batmobile is in the shop")
				}),
			}}}
		},
	})
	for i := range legacy {
		plantLetter(t, r, uint64(1000+i), i%4)
	}
	if n := r.count(outboxstore.DeadCountRoot()); n != 0 {
		t.Fatalf("%d counters before the relay started, want none", n)
	}
	r.startRelay()
	eventually(t, "the relay to build the counters from the stored letters", func() bool { return r.count(outboxstore.DeadCountRoot()) == 4 })
	eventually(t, "the relay's first status refresh", func() bool { return r.healthOf().Status == health.StatusDegraded })
	before := r.ob.KeysScanned()

	for i := range messages {
		r.stage(fmt.Sprintf("wayne-manor-%d", i), Dispatch{Villain: "Joker"})
	}
	// The counters are read straight from the keyspace here, not through Status,
	// so the polling does not add to the keys the outbox itself read.
	eventually(t, "every message to be dead-lettered and counted", func() bool {
		return r.count(outboxstore.DeadRoot()) == legacy+messages && counterSum(t, r) == legacy+messages
	})
	// The recount after the last commit reads a few counter rows; the budget below
	// is far wider than that.
	read := r.ob.KeysScanned() - before
	linear := int64(messages*8 + 2*(legacy+messages))
	t.Logf("%d messages dead-lettered over %d stored letters: %d keys read (linear budget %d, a scan per message would read about %d)",
		messages, legacy, read, linear, messages*(legacy+messages/2))
	if read > linear {
		t.Fatalf("dead-lettering %d messages read %d keys, want at most %d: something reads the stored letters per message", messages, read, linear)
	}
	st, err := r.ob.Status(context.Background())
	if err != nil || deadOf(st, consumerN) != legacy+messages {
		t.Fatalf("Status counts %d dead letters (%v), want %d", deadOf(st, consumerN), err, legacy+messages)
	}
}

// counterSum adds up the deadn/ counters by reading them directly.
func counterSum(t testing.TB, r *rig) int {
	t.Helper()
	root := outboxstore.DeadCountRoot()
	sum := 0
	for kv, err := range r.outKS.Scan(context.Background(), storage.ScanOptions{Start: root, End: prefixEnd(root)}) {
		must(t, err)
		c, err := codec.JSON[outboxstore.DeadCount](outboxstore.RecordVersion).Decode(kv.Value)
		must(t, err)
		sum += c.N
	}
	return sum
}

// failingHandler is a consumer that always fails, so MaxAttempts = 1 dead-letters
// every message it gets.
func failingHandler() func(testing.TB, *rig) []registration {
	return func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.Consume(notice(), consumerN, func(context.Context, outbox.Message[Dispatch]) error {
				return errors.New("the Batmobile is in the shop")
			}),
		}}}
	}
}

// A key under dead/ that is no letter's (a hand edit, a bug of another build)
// must not stop dead-lettering. The relay used to refuse to write a dead letter
// until the counters were built, and RepairDeadCounts aborted on the key, so the
// counters were never built: the message redelivered forever, its pair stayed
// blocked, and nothing showed as Stopped. Now the repair skips and names the key,
// the dead letter is written, and Status and core.outbox say a dead/ key is bad.
func TestOutbox_AStrayKeyUnderDeadDoesNotStopDeadLettering(t *testing.T) {
	r := newRig(t, rigOpts{
		noRelay:     true,
		cfg:         func(c *outbox.Config) { c.MaxAttempts = 1 },
		statusEvery: time.Hour,
		register:    failingHandler(),
	})
	const legacy = 3
	for i := range legacy {
		plantLetter(t, r, uint64(1000+i), i%2)
	}
	stray := append(append([]byte(nil), outboxstore.DeadRoot()...), 'j')
	r.putRaw(stray, []byte(`x`))
	r.startRelay()
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the message to be dead-lettered beside the stray key", func() bool {
		return r.count(outboxstore.DeadRoot()) == legacy+1+1 // the legacy letters, the new one, the stray key
	})
	ctx := context.Background()
	r.ob.RefreshStatus(ctx)
	st, err := r.ob.Status(ctx)
	must(t, err)
	if !stoppedHas(st, outbox.StoppedStatus{Topic: "dead/", Cause: "corrupt_row"}) {
		t.Fatalf("Stopped = %+v, want the stray dead/ key named (Topic dead/, corrupt_row)", st.Stopped)
	}
	if got := deadOf(st, consumerN); got != legacy+1 {
		t.Fatalf("Status counts %d dead letters, want %d (every readable letter, the stray key is no letter)", got, legacy+1)
	}
	if res := r.healthOf(); res.Status != health.StatusDegraded || res.Reason != "delivery_stopped" || !strings.Contains(res.Detail, "dead/") {
		t.Fatalf("core.outbox = %s (%s: %s), want Degraded with delivery_stopped naming dead/", res.Status, res.Reason, res.Detail)
	}
	if got := counterSum(t, r); got != legacy+1 {
		t.Fatalf("the counters add up to %d, want %d", got, legacy+1)
	}
}

// A counter row that does not decode is derived data: Status names it (under its
// pair, leaving it out of the dead count) instead of failing, and the next
// refresh rewrites it from the letters. Status used to return the decode error,
// so a single bad counter hid the whole status.
func TestOutbox_StatusNamesAnUnreadableDeadCounterAndRefreshRepairsIt(t *testing.T) {
	logs := &logCapture{}
	r := newRig(t, rigOpts{noExec: true, log: logs.logger(), register: consumeInto(&recorder{}, consumerN)})
	seedDead(t, r, consumerN, 7, true)
	ctx := context.Background()
	if _, err := r.ob.Status(ctx); err != nil { // builds the counters
		t.Fatal(err)
	}
	pp := outboxstore.Partition("wayne-manor", 2)
	r.putRaw(outboxstore.DeadCountKey(consumerN, noticeName, pp), []byte(`not a counter`))

	st, err := r.ob.Status(ctx)
	if err != nil {
		t.Fatalf("Status = %v with one unreadable counter, want the counter named instead", err)
	}
	want := outbox.StoppedStatus{Topic: noticeName, Consumer: consumerN, Partition: pp, Cause: "corrupt_row"}
	if !stoppedHas(st, want) {
		t.Fatalf("Stopped = %+v, want %+v", st.Stopped, want)
	}
	if got := deadOf(st, consumerN); got != 0 {
		t.Fatalf("Status counts %d dead letters, want 0: the unreadable counter is left out of the sum", got)
	}

	r.ob.RefreshStatus(ctx)
	if st, err = r.ob.Status(ctx); err != nil || stoppedHas(st, want) || deadOf(st, consumerN) != 1 {
		t.Fatalf("after the refresh: Status counts %d (%v), Stopped = %+v; want 1 dead letter and the counter no longer named", deadOf(st, consumerN), err, st.Stopped)
	}
	if n := logs.count("counter_unreadable=true"); n != 1 {
		t.Fatalf("the repair of the unreadable counter was logged %d times, want once:\n%s", n, logs.text())
	}
	if res := r.healthOf(); res.Status != health.StatusDegraded || res.Reason != "dead_letters" {
		t.Fatalf("core.outbox = %s (%s) after the repair, want Degraded for the letter alone", res.Status, res.Reason)
	}
}

// A build of the counters that keeps failing must not stop dead-lettering, and
// must not hide: core.outbox is Degraded with delivery_stopped naming the
// counters until a build succeeds. The relay used to refuse to write any dead
// letter while the counters were unbuilt, so an ordered message redelivered
// forever and an unordered round redelivered its successes, with nothing Stopped.
func TestOutbox_ACounterBuildThatKeepsFailingIsNamedAndDoesNotStopDeadLettering(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	hooks := &outbox.TestHooks{SeedDeadCounts: func() error {
		if failing.Load() {
			return errors.New("the Batcave disk is full")
		}
		return nil
	}}
	r := newRig(t, rigOpts{
		cfg:         func(c *outbox.Config) { c.MaxAttempts = 1 },
		statusEvery: time.Hour,
		hooks:       hooks,
		register:    failingHandler(),
	})
	eventually(t, "core.outbox to name the counters that cannot be built", func() bool {
		res := r.healthOf()
		return res.Status == health.StatusDegraded && res.Reason == "delivery_stopped" && strings.Contains(res.Detail, "dead_counters")
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the message to be dead-lettered while the counters are unbuilt", func() bool { return r.count(outboxstore.DeadRoot()) == 1 })

	failing.Store(false)
	ctx := context.Background()
	eventually(t, "the counters to build and core.outbox to stop naming them", func() bool {
		r.ob.Wake()
		st, err := r.ob.Status(ctx)
		res := r.healthOf()
		return err == nil && deadOf(st, consumerN) == 1 && res.Reason == "dead_letters" && !strings.Contains(res.Detail, "dead_counters")
	})
	if got := counterSum(t, r); got != 1 {
		t.Fatalf("the counters add up to %d, want 1", got)
	}
}
