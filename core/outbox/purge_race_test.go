// file: core/outbox/purge_race_test.go
// version: 1.0.0
// guid: 5ca8af97-df52-4a34-b857-ecb849a32bc1
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

// tryEntries is how many retry entries consumer holds on the notice topic.
func (r *rig) tryEntries(consumer string) int {
	r.t.Helper()
	n := 0
	for pp := range 2 {
		st, err := outboxstore.New(r.outKS).ReadPair(context.Background(), outboxstore.Pair{Consumer: consumer, Topic: noticeName, PP: pp})
		must(r.t, err)
		n += len(st.Try.Entries)
	}
	return n
}

// intentRows is how many write-ahead intent rows the run ledger holds: the
// relay's run writes one each time it records progress, so a relay that does
// work without delivering anything shows up here.
func (r *rig) intentRows() int {
	r.t.Helper()
	page := keys.Prefix(keys.Str("page"))
	n := 0
	for kv, err := range r.taskKS.Scan(context.Background(), storage.ScanOptions{Start: page, End: prefixEnd(page), KeysOnly: true}) {
		must(r.t, err)
		segs, err := keys.Split(kv.Key)
		if err == nil && len(segs) >= 3 && string(segs[2]) == "intent" {
			n++
		}
	}
	return n
}

// purgeOnce returns a BeforeAckCommit hook that purges consumer's backlog the
// first time the relay is about to commit a round for it after arm is set: the
// purge lands between the round's staging and its commit, the shape of an
// operator acting while a delivery is in flight.
func purgeOnce(t *testing.T, self *atomic.Pointer[rig], consumer string, arm, purged *atomic.Bool) *outbox.TestHooks {
	return &outbox.TestHooks{BeforeAckCommit: func(c string, _ uint64) {
		if c != consumer || !arm.Load() || !purged.CompareAndSwap(false, true) {
			return
		}
		if _, err := self.Load().ob.PurgeBacklog(context.Background(), consumer); err != nil {
			t.Errorf("purge: %v", err)
		}
	}}
}

// An operator's purge that lands between a task-starting delivery's staging
// and its commit discards the message, and no run starts for it: the run start
// is part of the same batch as the cursor move, which the purge made stale.
func TestOutbox_PurgeDuringConsumeIntoTaskCommitStartsNoRun(t *testing.T) {
	var self atomic.Pointer[rig]
	var arm, purged atomic.Bool
	arm.Store(true)
	rs := &responder{}
	register := func(testing.TB, *rig) []registration {
		h := outbox.ConsumeIntoTask(signal(), consumerC, runKind, func(m outbox.Message[Dispatch]) (any, error) {
			return respondParams{Villain: m.Payload.Villain, Block: m.Payload.Block}, nil
		}, outbox.Live())
		return []registration{{owner: "gotham", hs: []outbox.Handler{h}}}
	}
	r := newRig(t, rigOpts{hooks: purgeOnce(t, &self, consumerC, &arm, &purged), register: register, defs: []tasks.Definition{rs.def()}})
	self.Store(r)
	r.stageGotham("batmobile", Dispatch{Villain: "Poison Ivy", Block: 9})
	eventually(t, "the purge to run", func() bool { return purged.Load() })
	stays(t, "no run started for the purged message", 700*time.Millisecond, func() bool { return len(r.runsOf(runKind)) == 0 })
	if n := len(rs.all()); n != 0 {
		t.Fatalf("the run body ran %d times for a purged message", n)
	}
}

// A purge that lands inside an unordered round's settle batch leaves nothing to
// retry: the messages of the round are not entered into the retry list after
// the purge moved the cursor past them, and the entry the pair already held is
// gone with the purge, so none of the failing messages is called again.
func TestOutbox_PurgeDuringUnorderedRoundLeavesNothingToRetry(t *testing.T) {
	var self atomic.Pointer[rig]
	var arm, purged atomic.Bool
	var mu sync.Mutex
	calls := map[string]int{}
	r := newRig(t, rigOpts{hooks: purgeOnce(t, &self, consumerN, &arm, &purged),
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = 100000
			c.BackoffMin, c.BackoffMax = 800*time.Millisecond, 800*time.Millisecond
		},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(_ context.Context, m outbox.Message[Dispatch]) error {
					mu.Lock()
					calls[m.Payload.Villain]++
					mu.Unlock()
					if m.Payload.Villain == "Joker" || m.Payload.Villain == "Bane" {
						return errors.New("slipped the net")
					}
					return nil
				}, outbox.Unordered())}}}
		}})
	self.Store(r)
	count := func(v string) int { mu.Lock(); defer mu.Unlock(); return calls[v] }

	// The Joker fails once and waits in the retry list, not yet due.
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the Joker to be in the retry list", func() bool { return count("Joker") == 1 && r.tryEntries(consumerN) == 1 })
	arm.Store(true)
	r.stage("wayne-manor", Dispatch{Villain: "Bane"})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "the purge to run inside the round that took the Bane", func() bool { return purged.Load() })

	// Longer than a backoff: whatever was going to be retried would have been.
	stays(t, "no failing message called again after the purge", 2*time.Second, func() bool {
		return count("Joker") == 1 && count("Bane") == 1
	})
	if n := r.tryEntries(consumerN); n != 0 {
		t.Fatalf("%d retry entries left after the purge", n)
	}
}

// A retry that is itself in flight when the purge lands is not entered into the
// retry list again when it fails: the purge cleared its entry, and a failure
// settled against a cleared entry is for a message nobody wants any more.
func TestOutbox_PurgeDuringUnorderedRetryDoesNotRequeueIt(t *testing.T) {
	var self atomic.Pointer[rig]
	var arm, purged atomic.Bool
	var calls atomic.Int64
	r := newRig(t, rigOpts{hooks: purgeOnce(t, &self, consumerN, &arm, &purged),
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = 100000
			c.BackoffMin, c.BackoffMax = 250*time.Millisecond, 250*time.Millisecond
		},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(context.Context, outbox.Message[Dispatch]) error {
					calls.Add(1)
					return errors.New("slipped the net")
				}, outbox.Unordered())}}}
		}})
	self.Store(r)

	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the first attempt to settle into the retry list", func() bool { return calls.Load() >= 1 && r.tryEntries(consumerN) == 1 })
	arm.Store(true)
	eventually(t, "the purge to run inside the retry's settle", func() bool { return purged.Load() })
	// The retry ran (it is what the purge landed in), and nothing runs after it.
	was := calls.Load()
	if was < 2 {
		t.Fatalf("handler calls = %d when the purge ran, want the first attempt and a retry at least", was)
	}
	stays(t, "the purged message not tried again", 1200*time.Millisecond, func() bool { return calls.Load() == was })
	if n := r.tryEntries(consumerN); n != 0 {
		t.Fatalf("%d retry entries left after the purge", n)
	}
}

// A pair whose retry list is full and not due costs reads only, however often
// the relay is woken: a round that is allowed no new messages must not start,
// and so must not write a run-ledger intent row (an fsync) for nothing. Growth
// is compared with the number of wakes, never with elapsed time.
func TestOutbox_FullRetryListIdlesWithoutLedgerWrites(t *testing.T) {
	var calls atomic.Int64
	r := newRig(t, rigOpts{
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = 100000
			c.BackoffMin, c.BackoffMax = time.Hour, time.Hour
		},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(context.Context, outbox.Message[Dispatch]) error {
					calls.Add(1)
					return errors.New("slipped the net")
				}, outbox.Unordered())}}}
		}})
	// One key, so one partition: its 64-entry retry list fills and 16 messages wait behind it.
	for i := range 80 {
		r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: i})
	}
	eventually(t, "the retry list to fill", func() bool { return r.tryEntries(consumerN) >= 64 })
	time.Sleep(200 * time.Millisecond) // the round that filled it settles its ledger row
	before := r.intentRows()

	const wakes = 200
	for range wakes {
		r.ob.Wake()
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	grown := r.intentRows() - before
	if grown*10 > wakes {
		t.Fatalf("the ledger grew by %d intent rows over %d wakes of a pair that has nothing to do; want a tenth of the wakes at most", grown, wakes)
	}
	if n := calls.Load(); n != 64 {
		t.Fatalf("handler calls = %d, want the 64 that fill the retry list and no more", n)
	}
}

// A retry entry whose message is no longer in the store (removed out of band)
// is dropped by the next round, not kept: it could never be retried, and left in
// the pair's bounded retry list it would keep the pair awake and take a slot for
// good. The handler is not called for it again, and the drop is logged.
func TestOutbox_RetryEntryOfAGoneMessageIsDropped(t *testing.T) {
	var calls atomic.Int64
	logs := &logCapture{}
	r := newRig(t, rigOpts{log: logs.logger(),
		cfg: func(c *outbox.Config) {
			c.MaxAttempts = 100000
			c.BackoffMin, c.BackoffMax = 100*time.Millisecond, 100*time.Millisecond
		},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(context.Context, outbox.Message[Dispatch]) error {
					calls.Add(1)
					return errors.New("slipped the net")
				}, outbox.Unordered())}}}
		}})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the Joker to wait in the retry list", func() bool { return calls.Load() == 1 && r.tryEntries(consumerN) == 1 })

	// The message goes (an operator's hand in the store); its retry entry stays.
	b := r.outKS.NewBatch()
	must(t, b.Delete(outboxstore.MsgKey(noticeName, outboxstore.Partition("wayne-manor", 2), 1)))
	must(t, b.Commit(context.Background(), storage.Sync))
	b.Close()

	eventually(t, "the entry to be dropped", func() bool { return r.tryEntries(consumerN) == 0 })
	stays(t, "the handler not called again", 500*time.Millisecond, func() bool { return calls.Load() == 1 })
	if n := logs.count("were gone from the store"); n != 1 {
		t.Fatalf("the drop was logged %d times, want once:\n%s", n, logs.text())
	}
}
