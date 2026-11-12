// file: core/outbox/partitions_test.go
// version: 1.0.0
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
	for pp := range 4 {
		r1.stageGotham(keyFor[pp], Dispatch{Villain: fmt.Sprintf("pp%d", pp)})
	}
	eventually(t, "four messages sequenced", func() bool { return r1.messages(signalName) == 4 })
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

	// Third build: the declaration matches the stamp again, and the stranded
	// messages are delivered.
	seen3 := &payloads{}
	r3 := newRig(t, rigOpts{fs: cp.fs, partitions: 4, register: func(testing.TB, *rig) []registration {
		return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(signalName, consumerN, seen3.handler(consumerN))}}}
	}})
	eventually(t, "all four messages delivered once the count matches", func() bool { return seen3.count() == 4 })
	eventually(t, "health to be up", func() bool { return r3.healthOf().Status == health.StatusUp })
}

// The stop lifts by itself, in the running relay, once the stamp and the
// declaration agree again (here the stamp is rewritten by hand to the declared
// count, the way an operator repairing the store would): health returns to Up
// and delivery resumes.
func TestOutbox_PartitionStopLiftsWhenTheStampMatchesAgain(t *testing.T) {
	cp, _ := strandedFour(t)
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
	must(t, b.In(r.outKS).Set(outboxstore.SeqKey(signalName), []byte(`j{"v":1,"high":4,"partitions":2}`)))
	must(t, b.Commit(ctx, storage.Sync))
	b.Close()
	r.ob.Wake()

	eventually(t, "health to return to up", func() bool { return r.healthOf().Status == health.StatusUp })
	eventually(t, "delivery to resume", func() bool { return seen.count() > 0 })

	// Intake lifted too, at once and not after the stopped topic's 30 s retry
	// pause: a message staged now is sequenced and delivered.
	r.stageGotham("bat-after", Dispatch{Villain: "Scarecrow"})
	eventually(t, "a message staged after the lift to be delivered", func() bool { return seen.has("Scarecrow") })
}
