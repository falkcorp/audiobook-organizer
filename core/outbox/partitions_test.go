// file: core/outbox/partitions_test.go
// version: 1.0.2
// guid: e10c5bfb-43c5-423f-8265-428e0f871145
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// strandedFour is a store whose signal topic was sequenced over four
// partitions, one message on each, none delivered: the state a 4 to 2 change of
// the declaration finds. keyFor names a key for each partition.
func strandedFour(t *testing.T) (cp *crashPoints, keyFor map[int]string) {
	t.Helper()
	return stranded(t, 0, 1, 2, 3)
}

// stranded is strandedFour with a message on the named partitions only. Its
// villains are "pp<n>", and every one is delivered to nobody: the one consumer
// blocks.
func stranded(t *testing.T, pps ...int) (cp *crashPoints, keyFor map[int]string) {
	t.Helper()
	cp = newCrashPoints()
	keyFor = map[int]string{}
	for i := 0; len(keyFor) < 4; i++ {
		k := fmt.Sprintf("bat-%d", i)
		keyFor[outboxstore.Partition(k, 4)] = k
	}
	r1 := newRig(t, rigOpts{fs: cp.fs, partitions: 4, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(signalName, consumerN,
			func(ctx context.Context, _ outbox.RawMessage) error { <-ctx.Done(); return ctx.Err() })}}}
	}})
	eventually(t, "the consumer's four cursors", func() bool { return r1.cursors(consumerN) == 4 })
	for _, pp := range pps {
		r1.stageGotham(keyFor[pp], Dispatch{Villain: fmt.Sprintf("pp%d", pp)})
	}
	eventually(t, "the messages sequenced", func() bool { return r1.messages(signalName) == len(pps) })
	r1.close()
	return cp, keyFor
}

// payloads is what a set of raw handlers saw, by consumer and payload.
type payloads struct {
	mu  sync.Mutex
	got map[string]int
}

func (p *payloads) handler(consumer string) func(context.Context, outbox.RawMessage) error {
	return func(_ context.Context, m outbox.RawMessage) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.got == nil {
			p.got = map[string]int{}
		}
		p.got[consumer+":"+string(m.Payload)]++
		return nil
	}
}

func (p *payloads) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.got)
}

// has says whether some payload seen contains sub.
func (p *payloads) has(sub string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.got {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return false
}

// A topic whose declared partition count changed between boots stops as a
// whole, at every place that would work on its messages: intake (which already
// refused), delivery of messages sequenced over the old count, creation of a
// cursor for a consumer that is new, the retention sweep and an operator's
// purge. Without that, a 4 to 2 change strands the messages of partitions 2 and
// 3 behind a healthy-looking relay. The stop is reported (core.outbox Degraded,
// one log line), loses no message, and lifts when the declaration matches again.
func TestOutbox_PartitionCountChangeStopsTheTopic(t *testing.T) {
	cp, _ := strandedFour(t)

	// Second build: two partitions, the old consumer and a new one.
	seen := &payloads{}
	logs := &logCapture{}
	r2 := newRig(t, rigOpts{fs: cp.fs, partitions: 2, log: logs.logger(), sweep: sweepFast, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.ConsumeRaw(signalName, consumerN, seen.handler(consumerN)),
			outbox.ConsumeRaw(signalName, consumerN2, seen.handler(consumerN2)),
		}}}
	}})
	eventually(t, "the topic's stop to be reported", func() bool {
		h := r2.healthOf()
		return h.Status == health.StatusDegraded && h.Reason == "delivery_stopped"
	})
	if d := r2.healthOf().Detail; !strings.Contains(d, "partitions_changed") || !strings.Contains(d, signalName) {
		t.Fatalf("health detail %q does not name the topic and the cause", d)
	}
	for range 20 {
		r2.ob.Wake()
		time.Sleep(10 * time.Millisecond)
	}
	stays(t, "nothing delivered, nothing deleted, no cursor made for the new consumer", 600*time.Millisecond, func() bool {
		return seen.count() == 0 && r2.messages(signalName) == 4 && r2.cursors(consumerN2) == 0
	})
	if n := logs.count("its partition count changed"); n != 1 {
		t.Fatalf("the stop was logged %d times, want once:\n%s", n, logs.text())
	}

	// An operator's purge would delete by the wrong partitions: refused.
	if _, err := r2.ob.PurgeBacklog(context.Background(), consumerN); !errors.Is(err, outboxstore.ErrPartitionsChanged) {
		t.Fatalf("PurgeBacklog on a topic with a changed partition count = %v, want ErrPartitionsChanged", err)
	}
	if n := r2.messages(signalName); n != 4 {
		t.Fatalf("%d messages left after the refused purge, want 4", n)
	}
	r2.close()

	// Third build: the declaration is back at the stamp's count, the way out of
	// a change that nobody meant (the other way out is a migration that
	// re-sequences the messages, which this row does not have). Every stranded
	// message is delivered, the ones on partitions 2 and 3 included, and the stop
	// is gone.
	seen3 := &payloads{}
	r3 := newRig(t, rigOpts{fs: cp.fs, partitions: 4, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(signalName, consumerN, seen3.handler(consumerN))}}}
	}})
	eventually(t, "all four messages delivered once the declaration is restored", func() bool {
		return seen3.has("pp0") && seen3.has("pp1") && seen3.has("pp2") && seen3.has("pp3")
	})
	if n := seen3.count(); n != 4 {
		t.Fatalf("%d distinct deliveries, want exactly the four stranded messages", n)
	}
	eventually(t, "health to be up", func() bool { return r3.healthOf().Status == health.StatusUp })
}

// The stop lifts by itself, in the running relay, once the stamp and the
// declaration agree again, and intake and delivery resume at once. A running
// relay's declaration is fixed for its build, so the only way the two come back
// into line under it is an edit of the seq row, and this test makes one. That is
// not a repair: it is safe here only because both messages sit on partitions (0
// and 1) that are the same under 2 and under 4, so nothing is stranded by it.
// Over any other layout it would hide the messages of the partitions that the
// smaller count does not have (TestOutbox_PartitionCountChangeStopsTheTopic
// shows the way out of that: restore the declaration).
func TestOutbox_PartitionStopLiftsWhenTheStampMatchesAgain(t *testing.T) {
	cp, keyFor := stranded(t, 0, 1)
	for pp := range 2 {
		if outboxstore.Partition(keyFor[pp], 2) != pp {
			t.Fatalf("key %q is on partition %d under two, not %d: the layout this test relies on is gone", keyFor[pp], outboxstore.Partition(keyFor[pp], 2), pp)
		}
	}
	seen := &payloads{}
	r := newRig(t, rigOpts{fs: cp.fs, partitions: 2, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(signalName, consumerN, seen.handler(consumerN))}}}
	}})
	eventually(t, "the stop to be reported", func() bool { return r.healthOf().Reason == "delivery_stopped" })
	stays(t, "nothing delivered while stopped", 300*time.Millisecond, func() bool { return seen.count() == 0 })

	// The seq row is in a serialized range: a write to it reads it for update.
	ctx := context.Background()
	b := outboxstore.New(r.outKS).OpenBatch()
	b.Bind(r.outKS)
	_, err := b.GetForUpdate(ctx, r.outKS, outboxstore.SeqKey(signalName))
	must(t, err)
	must(t, b.In(r.outKS).Set(outboxstore.SeqKey(signalName), []byte(`j{"v":1,"high":2,"partitions":2}`)))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	r.ob.Wake()

	eventually(t, "health to return to up", func() bool { return r.healthOf().Status == health.StatusUp })
	eventually(t, "both stranded messages to be delivered", func() bool { return seen.has("pp0") && seen.has("pp1") })

	// Intake lifted too, at once and not after the stopped topic's 30 s retry
	// pause: a message staged now is sequenced and delivered.
	r.stageGotham("bat-after", Dispatch{Villain: "Scarecrow"})
	eventually(t, "a message staged after the lift to be delivered", func() bool { return seen.has("Scarecrow") })
}

// A topic stopped on a partition-count change costs the relay reads, not
// writes. Cursor creation goes through the run's writer, which records an intent
// row (a synced write) and renews the run's holds whatever the function inside
// returns, so a consumer that is refused and tried again on every loop would
// write for as long as the stop lasts, days if nobody acts. The stamp is checked
// before the writer is entered. Asserted as a ratio of ledger rows to wakes, not
// as a time: the relay loops several times for each wake.
func TestOutbox_PartitionStopWritesNoLedgerRowsPerLoop(t *testing.T) {
	cp, _ := strandedFour(t)
	seen := &payloads{}
	r := newRig(t, rigOpts{fs: cp.fs, partitions: 2, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{
			outbox.ConsumeRaw(signalName, consumerN, seen.handler(consumerN)),
			outbox.ConsumeRaw(signalName, consumerN2, seen.handler(consumerN2)),
		}}}
	}})
	eventually(t, "the stop to be reported", func() bool { return r.healthOf().Reason == "delivery_stopped" })
	time.Sleep(100 * time.Millisecond) // the loop that reported the stop finishes
	before := r.intentRows()
	const wakes = 200
	for range wakes {
		r.ob.Wake()
		time.Sleep(3 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if grown := r.intentRows() - before; grown*10 > wakes {
		t.Fatalf("%d ledger rows written over %d wakes of a stopped topic, want at most a tenth as many", grown, wakes)
	}
	if seen.count() != 0 || r.cursors(consumerN2) != 0 {
		t.Fatalf("the stopped topic delivered %d times and made %d cursors for the new consumer, want none", seen.count(), r.cursors(consumerN2))
	}
}

// A stop for a changed partition count belongs to partitionsOK, which reads the
// stamp and lifts the stop itself. The intake pass must not clear it: when the
// relay's loops are more than stopRetryEvery apart (a stalled store, a
// suspended host; here a clock that jumps 31 s between rounds) the intake looks
// at a stopped topic again, finds nothing due to sequence, and used to clear
// the topic's health, so the next delivery pass reported the stop a second
// time. The stop is logged once and health reads degraded after every round,
// with and without a consumer registered for the topic (a registered consumer
// refreshes the stop on every loop, one without relies on the sweep).
func TestOutbox_PartitionStopSurvivesLongGapsBetweenLoops(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(testing.TB, *rig) []registration
	}{
		{"no consumer registered", func(testing.TB, *rig) []registration { return nil }},
		{"one consumer registered", func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.ConsumeRaw(signalName, consumerN, (&payloads{}).handler(consumerN)),
			}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp, _ := strandedFour(t)
			clk := &skew{}
			logs := &logCapture{}
			r := newRig(t, rigOpts{fs: cp.fs, partitions: 2, now: clk.now, log: logs.logger(), sweep: sweepFast,
				cfg:      func(c *outbox.Config) { c.RelayRotate = 24 * time.Hour },
				register: tc.register})
			eventually(t, "the stop to be reported", func() bool { return r.healthOf().Reason == "delivery_stopped" })

			// A delayed intake row, so the intake pass has something to scan and
			// nothing due.
			b := r.gothamKS.NewBatch()
			must(t, outbox.Stage(r.ob.Writer("gotham"), b, signal(), "bat-delayed", Dispatch{Villain: "Riddler"},
				outbox.NotBefore(clk.now().Add(24*time.Hour))))
			must(t, b.Commit(context.Background(), storage.Sync))
			b.Close()

			for round := range 4 {
				clk.advance(31 * time.Second)
				for range 10 {
					r.ob.Wake()
					time.Sleep(10 * time.Millisecond)
				}
				if h := r.healthOf(); h.Status != health.StatusDegraded || h.Reason != "delivery_stopped" {
					t.Fatalf("round %d: health %v %q, want degraded delivery_stopped", round, h.Status, h.Reason)
				}
				if n := logs.count("its partition count changed"); n != 1 {
					t.Fatalf("round %d: the stop was logged %d times, want once:\n%s", round, n, logs.text())
				}
			}
		})
	}
}
