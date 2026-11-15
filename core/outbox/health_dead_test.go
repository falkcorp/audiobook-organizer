// file: core/outbox/health_dead_test.go
// version: 1.0.0
// guid: b584e6da-2910-4648-aa3d-af25cc239b55
// last-edited: 2026-11-15

package outbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/outbox"
)

// An operator's retry of a dead letter that lands after the relay committed it
// and before the relay told the health component must leave core.outbox Up once
// the retry delivers. The relay used to count the letter in memory after its
// commit, so a retry in that gap was undone by a late increment and the
// component stayed Degraded until the relay's next status refresh, fifteen
// seconds later. The hook runs the retry inside the gap on every run, for an
// ordered consumer (settleFail) and an unordered one (settleRound).
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
				cfg:   func(c *outbox.Config) { c.MaxAttempts = 1 },
				hooks: &hooks,
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
