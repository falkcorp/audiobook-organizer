// file: core/outbox/stage_test.go
// version: 1.0.0
// guid: 7d1c6f4e-3bb4-4a7b-9f6e-4f0ab7c1d902
// last-edited: 2026-11-12

package outbox_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// A message exists if and only if the producer's batch committed: before the
// commit nothing is visible in the outbox, and after it the producer's own
// write and the intake row are both there, and the message is delivered.
func TestOutbox_StageCommitsWithBatch(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN), noRelay: true})
	ctx := context.Background()

	b := r.gothamKS.NewBatch()
	defer b.Close()
	must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "batmobile", Dispatch{Villain: "Joker", Block: 7}))
	must(t, b.Set([]byte("case/batmobile"), []byte("open")))
	if n := r.count(outboxstore.InRoot()); n != 0 {
		t.Fatalf("an uncommitted Stage left %d intake rows", n)
	}
	must(t, b.Commit(ctx, storage.Sync))

	if n := r.count(outboxstore.InPrefix(noticeName)); n != 1 {
		t.Fatalf("intake rows after commit = %d, want 1", n)
	}
	if _, err := r.gothamKS.Get(ctx, []byte("case/batmobile")); err != nil {
		t.Fatalf("the producer's own write is missing: %v", err)
	}
	r.startRelay()
	eventually(t, "the delivery", func() bool { return rc.count() == 1 })
	got := rc.all()[0]
	if got.Villain != "Joker" || got.Block != 7 || got.Key != "batmobile" || got.Attempt != 1 {
		t.Fatalf("delivery = %+v", got)
	}
	eventually(t, "the intake row to be consumed", func() bool { return r.count(outboxstore.InRoot()) == 0 })
}

// A discarded batch leaves nothing: no intake row, no producer write, no
// delivery.
func TestOutbox_DiscardLeavesNothing(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN)})
	b := r.gothamKS.NewBatch()
	must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "batmobile", Dispatch{Villain: "Penguin"}))
	must(t, b.Set([]byte("case/batmobile"), []byte("open")))
	must(t, b.Close())
	stays(t, "an empty outbox", 80*time.Millisecond, func() bool {
		return rc.count() == 0 && r.count(outboxstore.InRoot()) == 0 && r.count(outboxstore.MsgTopicPrefix(noticeName)) == 0
	})
	if _, err := r.gothamKS.Get(context.Background(), []byte("case/batmobile")); !errIs(err, storage.ErrNotFound) {
		t.Fatalf("the discarded producer write is visible: %v", err)
	}
}

// Stage refuses what it must, and stages nothing when it does.
func TestOutbox_StageRefusals(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true})
	w := func(m string) outbox.Writer { return r.ob.Writer(m) }
	big := Dispatch{Villain: strings.Repeat("x", outbox.MaxPayloadBytes)}
	cases := []struct {
		name string
		do   func(b *storage.Batch) error
		want error
	}{
		{"undeclared topic", func(b *storage.Batch) error {
			return outbox.Stage(w("gotham"), b, outbox.NewTopic[Dispatch]("gotham.cape.stolen.v1"), "k", Dispatch{})
		}, outbox.ErrUnknownTopic},
		{"bus-only topic", func(b *storage.Batch) error {
			return outbox.Stage(w("gotham"), b, outbox.NewTopic[Dispatch](busOnly), "k", Dispatch{})
		}, outbox.ErrNotDurable},
		{"another module's topic", func(b *storage.Batch) error {
			return outbox.Stage(w("gotham"), b, notice(), "k", Dispatch{})
		}, outbox.ErrNotOwner},
		{"another payload type", func(b *storage.Batch) error {
			return outbox.Stage(w("core"), b, outbox.NewTopic[Alarm](noticeName), "k", Alarm{Vault: "Wayne"})
		}, outbox.ErrPayloadType},
		{"oversize payload", func(b *storage.Batch) error {
			return outbox.Stage(w("core"), b, notice(), "k", big)
		}, outbox.ErrPayloadTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := r.gothamKS.NewBatch()
			defer b.Close()
			if err := tc.do(b); !errIs(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			must(t, b.Commit(context.Background(), storage.Sync))
			if n := r.count(outboxstore.InRoot()); n != 0 {
				t.Fatalf("a refused Stage left %d intake rows", n)
			}
		})
	}
	var zero outbox.Writer
	b := r.gothamKS.NewBatch()
	defer b.Close()
	if err := outbox.Stage(zero, b, notice(), "k", Dispatch{}); err == nil {
		t.Fatal("a zero Writer staged")
	}
	if err := outbox.Stage(w("core"), nil, notice(), "k", Dispatch{}); err == nil {
		t.Fatal("Stage without a batch succeeded")
	}
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Many Stage calls of one key in one batch, inside one millisecond and across
// a backward clock step, are delivered in call order (COR4-14): the intake
// keys are strictly increasing.
func TestStageOrderWithinBatch(t *testing.T) {
	rc := &recorder{}
	clk := &fakeClock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN), noRelay: true, now: clk.now})
	b := r.gothamKS.NewBatch()
	defer b.Close()
	const n = 300
	for i := range n {
		switch {
		case i == 100:
			clk.add(-time.Hour) // the clock steps backward mid-batch
		case i == 200:
			clk.add(3 * time.Hour)
		case i%50 == 0:
			clk.add(time.Millisecond)
		}
		must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "same-key", Dispatch{Villain: "Riddler", Block: i}))
	}
	must(t, b.Commit(context.Background(), storage.Sync))

	var ids []string
	for kv, err := range r.outKS.Scan(context.Background(), storage.ScanOptions{Start: outboxstore.InPrefix(noticeName), End: prefixEnd(outboxstore.InPrefix(noticeName)), KeysOnly: true}) {
		must(t, err)
		_, id, perr := outboxstore.ParseInKey(kv.Key)
		must(t, perr)
		ids = append(ids, id)
	}
	if len(ids) != n || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != n {
		t.Fatalf("intake keys: %d rows, sorted=%v, distinct=%d", len(ids), slices.IsSorted(ids), len(slices.Compact(slices.Clone(ids))))
	}
	r.startRelay()
	eventually(t, "every delivery", func() bool { return rc.count() == n })
	for i, d := range rc.all() {
		if d.Block != i {
			t.Fatalf("delivery %d carries block %d: not in Stage order", i, d.Block)
		}
	}
}

// Two batches that commit out of stage order are both delivered; nothing is
// skipped (COR-10: only committed messages receive a seq).
func TestOutbox_IntakeOutOfOrderCommitsNoSkip(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN)})
	ctx := context.Background()
	first := r.gothamKS.NewBatch()
	defer first.Close()
	second := r.gothamKS.NewBatch()
	defer second.Close()
	must(t, outbox.Stage(r.ob.Writer("core"), first, notice(), "k1", Dispatch{Villain: "first-staged"}))
	must(t, outbox.Stage(r.ob.Writer("core"), second, notice(), "k2", Dispatch{Villain: "second-staged"}))
	must(t, second.Commit(ctx, storage.Sync))
	eventually(t, "the second batch's delivery", func() bool { return rc.count() == 1 })
	must(t, first.Commit(ctx, storage.Sync))
	eventually(t, "the first batch's delivery", func() bool { return rc.count() == 2 })
	if got := rc.villains(); got[0] != "second-staged" || got[1] != "first-staged" {
		t.Fatalf("deliveries = %v", got)
	}
	d := rc.all()
	if d[0].Seq != 1 || d[1].Seq != 2 {
		t.Fatalf("seqs = %d, %d, want 1, 2", d[0].Seq, d[1].Seq)
	}
}

// A batch that is never closed delays only its own messages: other messages
// and other topics flow.
func TestOutbox_LeakedBatchDoesNotStall(t *testing.T) {
	rcN := &recorder{}
	rcW := &recorder{}
	r := newRig(t, rigOpts{register: func(t testing.TB, r *rig) []registration {
		return []registration{
			{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN, rcN.handler(consumerN))}},
			{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(signalName, consumerN2, func(_ context.Context, m outbox.RawMessage) error {
				rcW.add(delivery{Consumer: consumerN2, Seq: m.Seq})
				return nil
			})}},
		}
	}})
	leaked := r.gothamKS.NewBatch() // never committed, never closed
	must(t, outbox.Stage(r.ob.Writer("core"), leaked, notice(), "k0", Dispatch{Villain: "Scarecrow"}))
	for i := range 5 {
		r.stage(fmt.Sprintf("k%d", i+1), Dispatch{Villain: fmt.Sprintf("v%d", i)})
		r.stageGotham("g", Dispatch{Villain: fmt.Sprintf("g%d", i)})
	}
	eventually(t, "the other messages and the other topic", func() bool { return rcN.count() == 5 && rcW.count() == 5 })
	_ = leaked.Close()
}

// An intake chunk and its payload bytes are bounded: a backlog far larger than
// one chunk is sequenced chunk by chunk, in order.
func TestOutbox_IntakeBackloggedInBoundedChunks(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN), noRelay: true})
	const n = 600
	b := r.gothamKS.NewBatch()
	defer b.Close()
	for i := range n {
		must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "one-key", Dispatch{Block: i}))
	}
	must(t, b.Commit(context.Background(), storage.Sync))
	rows, delayed, err := outboxstore.New(r.outKS).ScanIntake(context.Background(), noticeName, time.Now(), 256, 1<<20)
	must(t, err)
	if len(rows) != 256 || delayed != 0 {
		t.Fatalf("one scan returned %d rows (%d delayed), want the 256 chunk", len(rows), delayed)
	}
	r.startRelay()
	eventually(t, "every delivery", func() bool { return rc.count() == n })
	for i, d := range rc.all() {
		if d.Block != i {
			t.Fatalf("delivery %d is block %d", i, d.Block)
		}
	}
}

// The byte bound of one intake scan: rows past maxBytes wait for the next.
func TestOutbox_IntakeScanByteBound(t *testing.T) {
	r := newRig(t, rigOpts{noExec: true})
	b := r.gothamKS.NewBatch()
	defer b.Close()
	pad := strings.Repeat("z", 300<<10)
	for i := range 5 {
		must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "k", Dispatch{Villain: pad, Block: i}))
	}
	must(t, b.Commit(context.Background(), storage.Sync))
	rows, _, err := outboxstore.New(r.outKS).ScanIntake(context.Background(), noticeName, time.Now(), 256, 1<<20)
	must(t, err)
	if len(rows) != 3 {
		t.Fatalf("a 1 MiB scan of 300 KiB rows returned %d, want 3", len(rows))
	}
}

// NotBefore delays sequencing until the message is due.
func TestOutbox_NotBeforeDelaysIntake(t *testing.T) {
	rc := &recorder{}
	r := newRig(t, rigOpts{register: consumeInto(rc, consumerN)})
	b := r.gothamKS.NewBatch()
	defer b.Close()
	must(t, outbox.Stage(r.ob.Writer("core"), b, notice(), "k", Dispatch{Villain: "Bane"}, outbox.NotBefore(time.Now().Add(300*time.Millisecond))))
	must(t, b.Commit(context.Background(), storage.Sync))
	stays(t, "no delivery before NotBefore", 150*time.Millisecond, func() bool { return rc.count() == 0 })
	eventually(t, "the delayed delivery", func() bool { return rc.count() == 1 })
}

// Spec and NewTopic build a durable catalog entry.
func TestTopic_SpecSetsDurable(t *testing.T) {
	s := signal().Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}, 0)
	if s.Name != signalName || s.Durable == nil || s.Durable.Partitions != 0 || len(s.Schema) == 0 || s.PayloadType() == nil {
		t.Fatalf("Spec = %+v", s)
	}
	if signal().Name() != signalName {
		t.Fatal("Name")
	}
}
