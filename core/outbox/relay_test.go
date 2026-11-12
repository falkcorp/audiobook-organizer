// file: core/outbox/relay_test.go
// version: 1.0.0
// guid: 9a4d6e13-0b57-4c82-8e1f-72c5d3a0b6f4
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/tasks"
)

// skew is a clock a test can move forward without waiting.
type skew struct{ ns atomic.Int64 }

func (s *skew) now() time.Time          { return time.Now().Add(time.Duration(s.ns.Load())) }
func (s *skew) advance(d time.Duration) { s.ns.Add(int64(d)) }

var systemActor = tasks.Actor{System: "core", AuthMethod: "system"}

// The relay's definition is what 05-events §5E.5.3 says: it passes the
// executor's definition rules and the scheduler's (Every is allowed on this
// kind alone), is a Live writer of the core outbox resource, and its timeout
// stays under the tasks cap however long the rotation is configured.
func TestOutbox_RelayDefinition(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true})
	def := r.ob.RelayDefinition()
	if def.Kind() != outbox.RelayKind || def.Kind() != "core.outbox.relay" {
		t.Fatalf("kind %q", def.Kind())
	}
	c := tasks.CommonOf(def)
	if c.Priority != tasks.PrioritySystem || c.Resume != tasks.ResumeFromZero || c.Schedule == nil {
		t.Fatalf("common %+v", c)
	}
	if c.Timeout != time.Hour+5*time.Minute {
		t.Fatalf("timeout %v, want relay_rotate + 5m", c.Timeout)
	}
	if err := scheduler.ValidateSchedules([]tasks.Definition{def}, func(string) (bool, string, bool) { return false, "", false }); err != nil {
		t.Fatalf("scheduler refuses the relay definition: %v", err)
	}

	r.setConfig(func(c *outbox.Config) { c.RelayRotate = 72 * time.Hour })
	if got := tasks.CommonOf(r.ob.RelayDefinition()).Timeout; got != tasks.MaxTimeout {
		t.Fatalf("a 72h rotation gave timeout %v, want it clamped to %v", got, tasks.MaxTimeout)
	}
}

// A relay run ends successfully when its rotation is up, having delivered
// what was staged, and a fresh run (here, the nudger's) takes over and
// delivers the next message.
func TestOutbox_RelayRotationAndNudger(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{
		cfg:      func(c *outbox.Config) { c.RelayRotate = 150 * time.Millisecond },
		register: consumeInto(rc, consumerN),
		noRelay:  true,
	})
	ctx := t.Context()
	go r.ob.RunNudger(ctx)

	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the nudge to start a relay and deliver", func() bool { return rc.count() == 1 })
	eventually(t, "the first run to end at its rotation", func() bool {
		runs := r.runsOf(outbox.RelayKind)
		return len(runs) >= 1 && runs[0].State == tasks.StateSucceeded
	})
	eventually(t, "no relay running between rotations", func() bool { return !r.ob.Running() })

	r.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "a second run to take over", func() bool { return rc.count() == 2 })
	if got := rc.villains(); got[0] != "Joker" || got[1] != "Penguin" {
		t.Fatalf("delivered %v", got)
	}
	for _, run := range r.runsOf(outbox.RelayKind) {
		if run.Mode != tasks.ModeLive || run.Trigger != tasks.TriggerOutbox {
			t.Fatalf("relay run %+v, want Live and triggered by the outbox", run.RunView)
		}
	}
}

// Canceling the relay mid-flight loses nothing and duplicates nothing: the
// nudger starts a new run and the next message arrives, once.
func TestOutbox_RelayRestartsAfterCancel(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN)})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	eventually(t, "the first delivery", func() bool { return rc.count() == 1 })

	first := r.runsOf(outbox.RelayKind)
	if len(first) != 1 {
		t.Fatalf("%d relay runs, want 1", len(first))
	}
	must(t, r.x.CancelAs(context.Background(), first[0].ID, systemActor))
	r.waitRun(first[0].ID, tasks.StateCanceled)
	eventually(t, "the relay to stop", func() bool { return !r.ob.Running() })

	ctx := t.Context()
	go r.ob.RunNudger(ctx)
	r.stage("wayne-manor", Dispatch{Villain: "Penguin"})
	eventually(t, "a new relay to deliver", func() bool { return rc.count() == 2 })
	stays(t, "exactly two deliveries", 150*time.Millisecond, func() bool { return rc.count() == 2 })
	if got := rc.villains(); got[0] != "Joker" || got[1] != "Penguin" {
		t.Fatalf("delivered %v", got)
	}
}

// A relay turned off by its dial is not started by a nudge, and a Preview run
// of it does nothing (the relay delivers in Live mode only).
func TestOutbox_NudgerRespectsDialAndPreviewDoesNothing(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN), noRelay: true})
	r.setConfig(func(c *outbox.Config) { c.RelayEnabled = false })
	ctx := t.Context()
	go r.ob.RunNudger(ctx)
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	stays(t, "no relay started while the dial is off", 200*time.Millisecond, func() bool { return len(r.runsOf(outbox.RelayKind)) == 0 })

	r.setConfig(func(c *outbox.Config) { c.RelayEnabled = true })
	ref, err := r.x.Start(context.Background(), outbox.RelayKind, tasks.StartRequest{Mode: tasks.ModeRequestPreview})
	must(t, err)
	rec := r.waitRun(ref.ID, tasks.StateSucceeded)
	if rec.Mode != tasks.ModePreview {
		t.Fatalf("mode %v", rec.Mode)
	}
	stays(t, "a Preview relay delivers nothing", 150*time.Millisecond, func() bool { return rc.count() == 0 && !r.ob.Running() })
}

// concurrency tracks the most handlers in flight at once.
type concurrency struct {
	cur, max atomic.Int64
}

func (c *concurrency) enter() {
	n := c.cur.Add(1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			return
		}
	}
}
func (c *concurrency) leave() { c.cur.Add(-1) }

// slow is a plain handler that holds a slot for d.
func slow(c *concurrency, d time.Duration, done *atomic.Int64) outbox.HandlerFunc[Dispatch] {
	return func(ctx context.Context, _ outbox.Message[Dispatch]) error {
		c.enter()
		defer c.leave()
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
		done.Add(1)
		return nil
	}
}

// Delivery fans out across (consumer, partition) pairs on a pool bounded by
// relay_workers: with 2 workers never more than 2 handlers run at once, and
// with 4 workers over 6 pairs more than 2 do.
func TestOutbox_RelayWorkersBound(t *testing.T) {
	for _, tc := range []struct {
		workers      int
		wantAtLeast  int64
		wantAtMostIs int64
	}{
		{1, 1, 1}, {2, 2, 2}, {4, 3, 4},
	} {
		t.Run(fmt.Sprintf("workers=%d", tc.workers), func(t *testing.T) {
			var c concurrency
			var done atomic.Int64
			r := newRig(t, rigOpts{
				cfg: func(cfg *outbox.Config) { cfg.RelayWorkers = tc.workers },
				register: func(testing.TB, *rig) []registration {
					return []registration{{owner: "core", hs: []outbox.Handler{
						outbox.Consume(notice(), consumerN, slow(&c, 40*time.Millisecond, &done)),
						outbox.Consume(notice(), consumerN2, slow(&c, 40*time.Millisecond, &done)),
						outbox.Consume(notice(), consumerN3, slow(&c, 40*time.Millisecond, &done)),
					}}}
				},
			})
			ka, kb := keysOnDifferentPartitions(t)
			for range 2 {
				r.stage(ka, Dispatch{Villain: "Joker"})
				r.stage(kb, Dispatch{Villain: "Penguin"})
			}
			eventually(t, "all 12 deliveries", func() bool { return done.Load() == 12 })
			if got := c.max.Load(); got > tc.wantAtMostIs || got < tc.wantAtLeast {
				t.Fatalf("peak concurrency %d, want between %d and %d", got, tc.wantAtLeast, tc.wantAtMostIs)
			}
		})
	}
}

// An Unordered consumer runs at most unordered_parallelism handlers at once on
// a partition and does reach that parallelism.
func TestOutbox_UnorderedParallelismBound(t *testing.T) {
	var c concurrency
	var done atomic.Int64
	r := newRig(t, rigOpts{
		cfg: func(cfg *outbox.Config) { cfg.UnorderedParallelism = 3; cfg.RelayWorkers = 8 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{
				outbox.Consume(notice(), consumerN, slow(&c, 40*time.Millisecond, &done), outbox.Unordered())}}}
		},
	})
	for i := range 12 {
		r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: i})
	}
	eventually(t, "12 deliveries", func() bool { return done.Load() == 12 })
	if got := c.max.Load(); got != 3 {
		t.Fatalf("peak concurrency %d, want exactly unordered_parallelism=3", got)
	}
}

// An ordered consumer never has two handlers of one partition in flight: the
// partition's messages run one at a time in seq order even with many workers.
func TestOutbox_OrderedPartitionRunsOneAtATime(t *testing.T) {
	var c concurrency
	var done atomic.Int64
	var mu sync.Mutex
	var seqs []uint64
	r := newRig(t, rigOpts{
		cfg: func(cfg *outbox.Config) { cfg.RelayWorkers = 8 },
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
				func(ctx context.Context, m outbox.Message[Dispatch]) error {
					mu.Lock()
					seqs = append(seqs, m.Seq)
					mu.Unlock()
					return slow(&c, 15*time.Millisecond, &done)(ctx, m)
				})}}}
		},
	})
	for i := range 8 {
		r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: i})
	}
	eventually(t, "8 deliveries", func() bool { return done.Load() == 8 })
	if got := c.max.Load(); got != 1 {
		t.Fatalf("peak concurrency %d on one partition of an ordered consumer, want 1", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("seqs %v are not consecutive and increasing", seqs)
		}
	}
}

// ConsumeTx starts a run through Tx.StartRun in the relay's own batch; an
// unset mode is a programming error that dead-letters at once as mode_unset
// and starts nothing.
func TestOutbox_TxStartRun(t *testing.T) {
	rs := &responder{}
	r := newRig(t, rigOpts{
		defs: []tasks.Definition{rs.def()},
		register: func(testing.TB, *rig) []registration {
			return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeTx(notice(), consumerN,
				func(_ context.Context, tx *outbox.Tx, m outbox.Message[Dispatch]) error {
					req := tasks.StartRequest{Params: rawJSON(t, respondParams{Villain: m.Payload.Villain, Block: m.Payload.Block})}
					if m.Payload.Villain != "Joker" {
						req.Mode = tasks.ModeRequestLive
					}
					_, err := tx.StartRun(runKind, req)
					return err
				})}}}
		},
	})
	r.stage("wayne-manor", Dispatch{Villain: "Joker", Block: 1})
	r.stage("wayne-manor", Dispatch{Villain: "Penguin", Block: 2})
	eventually(t, "the Penguin's run and the Joker's dead letter", func() bool {
		l, _, _ := r.ob.ListDead(context.Background(), "", 5)
		return len(l) == 1 && len(rs.all()) == 1
	})
	l, _, _ := r.ob.ListDead(context.Background(), "", 5)
	if l[0].Token != "mode_unset" || l[0].Attempts != 1 {
		t.Fatalf("dead letter %+v, want mode_unset after one attempt", l[0])
	}
	if got := rs.all(); got[0].Villain != "Penguin" {
		t.Fatalf("run params %+v", got)
	}
	if n := len(r.runsOf(runKind)); n != 1 {
		t.Fatalf("%d runs of %s, want only the Penguin's", n, runKind)
	}
}

// Without a bound executor the consumers that stage runs fail loudly (ErrNoRuns
// is not retried into silence) while plain consumers are unaffected.
func TestOutbox_StageNeedsNoExecutor(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true})
	r.stage("wayne-manor", Dispatch{Villain: "Joker"})
	if n := r.count(outboxstore.InPrefix(noticeName)); n != 1 {
		t.Fatalf("%d intake rows, want the staged message", n)
	}
	if h := r.healthOf(); h.Status == health.StatusDegraded {
		t.Fatalf("health %+v: staging with no relay is not a fault", h)
	}
}
