// file: core/events/bus_test.go
// version: 1.0.1
// guid: 15316ef2-102c-47b6-b868-5dee13f7d746
// last-edited: 2026-11-11

package events_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// sample reads one series' value from p: a counter's value or a gauge's, or
// -1 when the series is absent.
func sample(t *testing.T, p *metrics.Provider, family string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			for k, v := range labels {
				if got[k] != v {
					continue next
				}
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return -1
}

// eventually polls cond for up to 10 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func dropLabels(sub, reason string) map[string]string {
	return map[string]string{"topic": "gotham.villain.escaped.v1", "subscriber": sub, "reason": reason}
}

// TestBus_SlowSubscriberDropsAndCounts: a slow subscriber's full queue drops
// the incoming events (DropNewest), every drop is counted under
// reason=queue_full, the warning is logged once, and a fast subscriber of the
// same topic receives everything (§5E.5.1).
func TestBus_SlowSubscriberDropsAndCounts(t *testing.T) {
	p, sc := provider(t)
	var lb logBuf
	b := newBus(t, events.BusOptions{Logger: lb.logger(), Metrics: sc})
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	var slowGot atomic.Int32
	slow := events.On(escaped(), "gotham.slow", func(context.Context, events.Envelope[Sighting]) {
		slowGot.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}, events.QueueSize(4))
	fast := newRecorder[Sighting]()
	must(t, b.Subscribe("gotham", slow, events.On(escaped(), "gotham.fast", fast.handle)))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)

	pub := b.Scope("gotham")
	events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Villain: "joker", Block: 1})
	<-entered // the first event is in the handler; the queue is empty
	for i := 2; i <= 10; i++ {
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Villain: "joker", Block: i})
	}
	fast.wait(t, 10)
	if got := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.slow", "queue_full")); got != 5 {
		t.Errorf("slow drops = %v, want 5 (1 in the handler, 4 queued, 5 refused)", got)
	}
	if got := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.fast", "queue_full")); got != -1 {
		t.Errorf("fast drops = %v, want none", got)
	}
	if n := lb.count("dropping events for a subscriber"); n != 1 {
		t.Errorf("drop warnings = %d, want 1 per subscriber per minute\n%s", n, lb.String())
	}
	if got := sample(t, p, "mediacore_events_queue_depth", map[string]string{"subscriber": "gotham.slow"}); got != 4 {
		t.Errorf("slow queue depth = %v, want 4", got)
	}
	close(release)
	eventually(t, "the slow subscriber's queued events", func() bool { return slowGot.Load() == 5 })
	var blocks []int
	for _, e := range fast.envelopes() {
		blocks = append(blocks, e.Payload.Block)
	}
	if !slices.Equal(blocks, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Errorf("fast received %v", blocks)
	}
}

// TestBus_DropOldestKeepsNewest: DropOldest evicts the oldest queued event
// for the incoming one, counted the same way.
func TestBus_DropOldestKeepsNewest(t *testing.T) {
	p, sc := provider(t)
	b := newBus(t, events.BusOptions{Metrics: sc})
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	var mu sync.Mutex
	var got []int
	h := func(_ context.Context, e events.Envelope[Sighting]) {
		mu.Lock()
		got = append(got, e.Payload.Block)
		mu.Unlock()
		select {
		case entered <- struct{}{}:
			<-release
		default:
		}
	}
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.newest", h, events.QueueSize(2), events.DropOldest())))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	pub := b.Scope("gotham")
	events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 1})
	<-entered
	for i := 2; i <= 6; i++ {
		events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: i})
	}
	close(release)
	eventually(t, "the newest events", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 3 })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, []int{1, 5, 6}) {
		t.Errorf("received %v, want [1 5 6]", got)
	}
	if n := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.newest", "queue_full")); n != 3 {
		t.Errorf("drops = %v, want 3", n)
	}
}

// TestBus_PanicIsolated_SubscriptionSurvives: a panicking handler is
// recovered and counted, its subscription keeps receiving, and another
// subscriber is untouched.
func TestBus_PanicIsolated_SubscriptionSurvives(t *testing.T) {
	p, sc := provider(t)
	var lb logBuf
	b := newBus(t, events.BusOptions{Logger: lb.logger(), Metrics: sc})
	survivor, other := newRecorder[Sighting](), newRecorder[Sighting]()
	must(t, b.Subscribe("gotham",
		events.On(escaped(), "gotham.fragile", func(ctx context.Context, e events.Envelope[Sighting]) {
			if e.Payload.Block == 1 {
				panic("the Joker was here")
			}
			survivor.handle(ctx, e)
		}),
		events.On(escaped(), "gotham.steady", other.handle)))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	pub := b.Scope("gotham")
	for i := 1; i <= 3; i++ {
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Villain: "joker", Block: i})
	}
	survivor.wait(t, 2)
	other.wait(t, 3)
	if n := sample(t, p, "mediacore_events_handler_panics_total", map[string]string{"subscriber": "gotham.fragile"}); n != 1 {
		t.Errorf("panics = %v, want 1", n)
	}
	if n := lb.count("subscriber panicked"); n != 1 {
		t.Errorf("panic logs = %d, want 1\n%s", n, lb.String())
	}
	if out := lb.String(); strings.Contains(out, "Joker was here") || !strings.Contains(out, "panic_type=string") {
		t.Errorf("the panic log must carry the panic's type, never its value:\n%s", out)
	}
	if got := survivor.envelopes(); got[0].Payload.Block != 2 || got[1].Payload.Block != 3 {
		t.Errorf("survivor received %+v", got)
	}
}

type ctxKey struct{}

// TestBus_HandlerContextIsBusNotPublisher: the handler's context is the bus
// lifecycle context: a publisher context canceled before delivery does not
// reach it, nor do its values, and Close cancels it (Q1 decision).
func TestBus_HandlerContextIsBusNotPublisher(t *testing.T) {
	b := newBus(t, events.BusOptions{})
	type seen struct {
		err   error
		value any
		ctx   context.Context
	}
	ch := make(chan seen, 1)
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.ctx", func(ctx context.Context, _ events.Envelope[Sighting]) {
		ch <- seen{err: ctx.Err(), value: ctx.Value(ctxKey{}), ctx: ctx}
	})))
	b.Freeze()
	pctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "request scoped"))
	events.Publish(b.Scope("gotham"), pctx, escaped(), "joker", Sighting{Villain: "joker"})
	cancel() // the request is over before the event is delivered
	must(t, b.Open())
	got := <-ch
	if got.err != nil {
		t.Errorf("handler ctx err = %v, want nil (the publisher's cancellation must not reach it)", got.err)
	}
	if got.value != nil {
		t.Errorf("handler ctx carries the publisher's value %v", got.value)
	}
	must(t, b.Close(context.Background()))
	if got.ctx.Err() == nil {
		t.Error("Close did not cancel the bus lifecycle context")
	}
}

// TestBus_NoDeliveryBeforeOpenNoneAfterClose (COR-8): no handler runs before
// Open (events published after Freeze wait in the queue), and an event
// published after Close is dropped and counted under reason=closed.
func TestBus_NoDeliveryBeforeOpenNoneAfterClose(t *testing.T) {
	p, sc := provider(t)
	synctest.Test(t, func(t *testing.T) {
		b := newBus(t, events.BusOptions{Metrics: sc})
		var got atomic.Int32
		must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.gate", func(context.Context, events.Envelope[Sighting]) { got.Add(1) })))
		pub := b.Scope("gotham")
		b.Freeze()
		events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 1})
		time.Sleep(time.Hour)
		synctest.Wait()
		if n := got.Load(); n != 0 {
			t.Fatalf("%d handler call(s) before Open", n)
		}
		must(t, b.Open())
		synctest.Wait()
		if n := got.Load(); n != 1 {
			t.Fatalf("after Open: %d deliveries, want the 1 queued since Freeze", n)
		}
		must(t, b.Close(context.Background()))
		events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 2})
		time.Sleep(time.Hour)
		synctest.Wait()
		if n := got.Load(); n != 1 {
			t.Fatalf("after Close: %d deliveries, want still 1", n)
		}
		if err := b.Open(); !errors.Is(err, events.ErrClosed) {
			t.Errorf("Open after Close: err = %v, want ErrClosed", err)
		}
	})
	if n := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.gate", "closed")); n != 1 {
		t.Errorf("closed drops = %v, want 1", n)
	}
	if n := sample(t, p, "mediacore_events_delivered_total", map[string]string{"subscriber": "gotham.gate"}); n != 1 {
		t.Errorf("delivered = %v, want 1", n)
	}
}

// TestBus_PerPublisherFIFO: per subscription, the events of one publishing
// goroutine arrive in publish order.
func TestBus_PerPublisherFIFO(t *testing.T) {
	b := newBus(t, events.BusOptions{})
	rec := newRecorder[Sighting]()
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.order", rec.handle, events.QueueSize(65536))))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	const publishers, each = 4, 2000
	var wg sync.WaitGroup
	for p := range publishers {
		wg.Go(func() {
			for i := range each {
				events.Publish(b.Scope("gotham"), context.Background(), escaped(), fmt.Sprint(p), Sighting{Villain: fmt.Sprint(p), Block: i})
			}
		})
	}
	wg.Wait()
	rec.wait(t, publishers*each)
	last := map[string]int{}
	for _, e := range rec.envelopes() {
		prev, ok := last[e.Key]
		if ok && e.Payload.Block != prev+1 {
			t.Fatalf("publisher %s: %d after %d", e.Key, e.Payload.Block, prev)
		}
		last[e.Key] = e.Payload.Block
	}
}

// TestBus_CoalesceLatestPerKey: Coalesce delivers at most one event per key
// per window, the latest; the empty key is delivered at once.
func TestBus_CoalesceLatestPerKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBus(t, events.BusOptions{})
		var mu sync.Mutex
		var got []string
		must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.coalesced", func(_ context.Context, e events.Envelope[Sighting]) {
			mu.Lock()
			got = append(got, fmt.Sprintf("%s:%d", e.Key, e.Payload.Block))
			mu.Unlock()
		}, events.Coalesce(time.Second))))
		b.Freeze()
		must(t, b.Open())
		pub := b.Scope("gotham")
		snapshot := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(got) }

		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Block: 1})
		time.Sleep(300 * time.Millisecond)
		events.Publish(pub, context.Background(), escaped(), "penguin", Sighting{Block: 1})
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Block: 2})
		events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 9})
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Block: 3})
		synctest.Wait()
		if s := snapshot(); !slices.Equal(s, []string{":9"}) {
			t.Fatalf("before any window ended: %v, want only the keyless event", s)
		}
		time.Sleep(700 * time.Millisecond) // joker's window (opened at 0) ends
		synctest.Wait()
		if s := snapshot(); !slices.Equal(s, []string{":9", "joker:3"}) {
			t.Fatalf("at 1 s: %v, want joker's latest", s)
		}
		time.Sleep(300 * time.Millisecond) // penguin's window (opened at 300 ms) ends
		synctest.Wait()
		if s := snapshot(); !slices.Equal(s, []string{":9", "joker:3", "penguin:1"}) {
			t.Fatalf("at 1.3 s: %v", s)
		}
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Block: 4})
		time.Sleep(time.Second)
		synctest.Wait()
		if s := snapshot(); !slices.Equal(s, []string{":9", "joker:3", "penguin:1", "joker:4"}) {
			t.Fatalf("a new window: %v", s)
		}
		must(t, b.Close(context.Background()))
	})
}

// TestBus_CloseDrainsWithinBudget: Close drains the queues until its context
// ends, then cancels the handlers' context, drops and counts what is left
// and returns at the budget, naming a subscription still in its handler.
func TestBus_CloseDrainsWithinBudget(t *testing.T) {
	p, sc := provider(t)
	synctest.Test(t, func(t *testing.T) {
		run := func(name string, budget time.Duration) (delivered int32, elapsed time.Duration, err error) {
			b := newBus(t, events.BusOptions{Metrics: sc})
			var n atomic.Int32
			entered := make(chan struct{}, 1)
			must(t, b.Subscribe("gotham", events.On(escaped(), name, func(context.Context, events.Envelope[Sighting]) {
				select {
				case entered <- struct{}{}:
				default:
				}
				time.Sleep(100 * time.Millisecond) // ignores ctx: the worst case
				n.Add(1)
			})))
			b.Freeze()
			must(t, b.Open())
			for i := range 10 {
				events.Publish(b.Scope("gotham"), context.Background(), escaped(), "", Sighting{Block: i})
			}
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			start := time.Now()
			err = b.Close(ctx)
			elapsed = time.Since(start)
			time.Sleep(time.Second) // the in-flight handler finishes and its goroutine exits
			synctest.Wait()
			return n.Load(), elapsed, err
		}

		got, elapsed, err := run("gotham.drained", 5*time.Second)
		if err != nil || got != 10 || elapsed != time.Second {
			t.Errorf("ample budget: delivered %d in %v, err %v; want 10 in 1s, nil", got, elapsed, err)
		}

		got, elapsed, err = run("gotham.cut", 350*time.Millisecond)
		if elapsed != 350*time.Millisecond {
			t.Errorf("Close returned after %v, want the 350ms budget", elapsed)
		}
		if err == nil || !errors.Is(err, events.ErrClosed) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want ErrClosed and the deadline, naming gotham.cut", err)
		}
		if got != 4 {
			t.Errorf("delivered %d, want 4 (3 done and 1 in flight at the budget)", got)
		}
	})
	if n := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.cut", "closed")); n != 6 {
		t.Errorf("closed drops = %v, want 6", n)
	}
	if n := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.drained", "closed")); n != -1 {
		t.Errorf("drained subscriber drops = %v, want none", n)
	}
}

// TestBus_NoGoroutinePerEvent: the goroutine count stays flat under 1e5
// publishes (the fix of internal/plugin/events.go:90-101).
func TestBus_NoGoroutinePerEvent(t *testing.T) {
	b := newBus(t, events.BusOptions{})
	var n atomic.Int64
	h := func(context.Context, events.Envelope[Sighting]) { n.Add(1) }
	must(t, b.Subscribe("gotham",
		events.On(escaped(), "gotham.one", h, events.QueueSize(65536)),
		events.On(escaped(), "gotham.two", h, events.QueueSize(65536))))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	base := runtime.NumGoroutine()
	peak := base
	pub := b.Scope("gotham")
	for i := range 100_000 {
		events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: i})
		if i%1000 == 0 {
			peak = max(peak, runtime.NumGoroutine())
		}
	}
	if peak > base+2 {
		t.Errorf("goroutines grew from %d to %d during 1e5 publishes", base, peak)
	}
	eventually(t, "every delivery or drop", func() bool { return n.Load() > 0 })
}

// TestOnRaw_CrossModuleWithoutImport (R3): a module subscribes to another
// module's topic by name and receives its payload as JSON, validated against
// the declared schema, with the envelope's correlation fields; a typed
// subscription to another module's topic is refused, a core topic is fine.
func TestOnRaw_CrossModuleWithoutImport(t *testing.T) {
	b := newBus(t, events.BusOptions{Origin: func(context.Context) (string, string) { return "task:r-42", "r-42" }})
	raw := make(chan events.RawEnvelope, 1)
	must(t, b.Subscribe("wayne",
		events.OnRaw("gotham.villain.escaped.v1", "wayne.watch", func(_ context.Context, e events.RawEnvelope) { raw <- e }),
		events.On(signal(), "wayne.signal", func(context.Context, events.Envelope[Sighting]) {})))
	for name, c := range map[string]struct {
		sub  events.Subscription
		want error
	}{
		"typed foreign":  {events.On(escaped(), "wayne.typed", func(context.Context, events.Envelope[Sighting]) {}), events.ErrForeignTopic},
		"raw undeclared": {events.OnRaw("gotham.villain.vanished.v1", "wayne.ghost", func(context.Context, events.RawEnvelope) {}), events.ErrTopicUndeclared},
	} {
		if err := b.Subscribe("wayne", c.sub); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)

	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4}})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	events.Publish(b.Scope("gotham"), ctx, escaped(), "joker", Sighting{Villain: "joker", Block: 7})
	e := <-raw
	if string(e.Payload) != `{"villain":"joker","block":7}` {
		t.Errorf("payload = %s", e.Payload)
	}
	if e.Topic != "gotham.villain.escaped.v1" || e.Key != "joker" || e.Actor != "task:r-42" || e.RunID != "r-42" {
		t.Errorf("envelope = %+v", e)
	}
	if e.TraceID != sc.TraceID().String() {
		t.Errorf("TraceID = %q, want %q", e.TraceID, sc.TraceID())
	}
	if len(e.ID) != 26 || e.Time.IsZero() {
		t.Errorf("ID = %q (want a 26-character ULID), Time = %v", e.ID, e.Time)
	}
}

// TestOnRaw_DisabledModuleWarns (G1): a raw subscription to a disabled
// module's topic boots, is warned about and receives nothing.
func TestOnRaw_DisabledModuleWarns(t *testing.T) {
	var lb logBuf
	b := newBus(t, events.BusOptions{Logger: lb.logger()})
	b.DeclareDisabled("joker")
	var got atomic.Int32
	must(t, b.Subscribe("wayne", events.OnRaw("joker.prank.played.v1", "wayne.pranks", func(context.Context, events.RawEnvelope) { got.Add(1) })))
	if n := lb.count("disabled module"); n != 1 {
		t.Errorf("warnings = %d, want 1\n%s", n, lb.String())
	}
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	events.Publish(b.Scope("joker"), context.Background(), events.NewTopic[Sighting]("joker.prank.played.v1"), "", Sighting{})
	if _, ok := b.Catalog().Lookup("joker.prank.played.v1"); ok {
		t.Error("a disabled module's topic is in the catalog")
	}
	time.Sleep(20 * time.Millisecond)
	if got.Load() != 0 {
		t.Error("a disabled module's topic delivered an event")
	}
	// A typed subscription cannot name it: only OnRaw reaches another module.
	b2 := newBus(t, events.BusOptions{})
	b2.DeclareDisabled("joker")
	err := b2.Subscribe("wayne", events.On(events.NewTopic[Sighting]("joker.prank.played.v1"), "wayne.typed", func(context.Context, events.Envelope[Sighting]) {}))
	if !errors.Is(err, events.ErrTopicUndeclared) {
		t.Errorf("typed subscription to a disabled module's topic: err = %v, want ErrTopicUndeclared", err)
	}
}

// TestSubscribe_Rules: names, uniqueness, options, the payload type, Durable
// targets and the lifecycle order are checked at registration.
func TestSubscribe_Rules(t *testing.T) {
	b := newBus(t, events.BusOptions{})
	must(t, b.Declare("gotham", events.NewTopic[Sighting]("gotham.villain.jailed.v1").Spec(events.TopicSpec{
		Permission: rbac.Need("gotham:view"), Durable: &events.Durable{}})))
	nop := func(context.Context, events.Envelope[Sighting]) {}
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.taken", nop)))
	for name, c := range map[string]struct {
		sub  events.Subscription
		want error
	}{
		"no module prefix":   {events.On(escaped(), "taken", nop), events.ErrSubscriptionName},
		"another module":     {events.On(escaped(), "wayne.taken", nop), events.ErrSubscriptionName},
		"bad segment":        {events.On(escaped(), "gotham.Taken", nop), events.ErrSubscriptionName},
		"duplicate":          {events.On(escaped(), "gotham.taken", nop), events.ErrDuplicateSubscription},
		"queue size 0":       {events.On(escaped(), "gotham.q0", nop, events.QueueSize(0)), events.ErrInvalidSubscription},
		"queue size too big": {events.On(escaped(), "gotham.q1", nop, events.QueueSize(65537)), events.ErrInvalidSubscription},
		"zero window":        {events.On(escaped(), "gotham.c0", nop, events.Coalesce(0)), events.ErrInvalidSubscription},
		"nil handler":        {events.On[Sighting](escaped(), "gotham.nil", nil), events.ErrInvalidSubscription},
		"zero subscription":  {events.Subscription{}, events.ErrInvalidSubscription},
		"payload type":       {events.On(events.NewTopic[Alarm]("gotham.villain.escaped.v1"), "gotham.alarm", func(context.Context, events.Envelope[Alarm]) {}), events.ErrPayloadType},
		"durable":            {events.OnRaw("gotham.villain.jailed.v1", "gotham.jail", func(context.Context, events.RawEnvelope) {}), events.ErrDurableTopic},
	} {
		if err := b.Subscribe("gotham", c.sub); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if err := b.Open(); !errors.Is(err, events.ErrNotFrozen) {
		t.Errorf("Open before Freeze: err = %v, want ErrNotFrozen", err)
	}
	b.Freeze()
	if err := b.Subscribe("gotham", events.On(escaped(), "gotham.late", nop)); !errors.Is(err, events.ErrFrozen) {
		t.Errorf("Subscribe after Freeze: err = %v, want ErrFrozen", err)
	}
	must(t, b.Open())
	if err := b.Open(); !errors.Is(err, events.ErrOpen) {
		t.Errorf("second Open: err = %v, want ErrOpen", err)
	}
	must(t, b.Close(context.Background()))
	must(t, b.Close(context.Background()))
	if _, err := events.NewBus(events.BusOptions{QueueSize: 65537}); err == nil {
		t.Error("NewBus accepted a queue size above the maximum")
	}
}

// TestPublish_RejectedCountedLoggedOnce: an undeclared topic, another
// module's topic, a core topic through a Scope, a module topic through the
// Bus, a Durable topic, a payload of another type and a publish before
// Freeze are each counted under publish_rejected and logged once per topic.
func TestPublish_RejectedCountedLoggedOnce(t *testing.T) {
	p, sc := provider(t)
	var lb logBuf
	b := newBus(t, events.BusOptions{Logger: lb.logger(), Metrics: sc})
	jailed := events.NewTopic[Sighting]("gotham.villain.jailed.v1")
	must(t, b.Declare("gotham", jailed.Spec(events.TopicSpec{Permission: rbac.Need("gotham:view"), Durable: &events.Durable{}})))
	ctx := context.Background()
	events.Publish(b.Scope("gotham"), ctx, escaped(), "", Sighting{}) // before Freeze
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	rejected := func(topic string) float64 {
		return sample(t, p, "mediacore_events_publish_rejected_total", map[string]string{"topic": topic})
	}
	if n := rejected("gotham.villain.escaped.v1"); n != 1 {
		t.Errorf("before Freeze: rejected = %v, want 1", n)
	}
	for range 2 {
		events.Publish(b.Scope("gotham"), ctx, events.NewTopic[Sighting]("gotham.villain.vanished.v1"), "", Sighting{})
		events.Publish(b.Scope("wayne"), ctx, escaped(), "", Sighting{})
		events.Publish(b.Scope("gotham"), ctx, signal(), "", Sighting{})
		events.Publish(b, ctx, tripped(), "", Alarm{})
		events.Publish(b.Scope("gotham"), ctx, jailed, "", Sighting{})
		events.Publish(b.Scope("wayne"), ctx, events.NewTopic[Sighting]("wayne.vault.tripped.v1"), "", Sighting{})
	}
	for topic, want := range map[string]float64{
		"gotham.villain.vanished.v1": 2, "gotham.villain.escaped.v1": 3, "core.signal.lit.v1": 2,
		"wayne.vault.tripped.v1": 4, "gotham.villain.jailed.v1": 2,
	} {
		if n := rejected(topic); n != want {
			t.Errorf("%s: rejected = %v, want %v", topic, n, want)
		}
	}
	if n := lb.count("publish rejected"); n != 5 {
		t.Errorf("rejection logs = %d, want one per topic (5)\n%s", n, lb.String())
	}
	var zero events.Scope
	events.Publish(zero, ctx, escaped(), "", Sighting{}) // a zero Scope is inert
	var nilBus *events.Bus
	events.Publish(nilBus, ctx, signal(), "", Sighting{})

	// The accepted path: core publishes its own topic through the Bus.
	events.Publish(b, ctx, signal(), "", Sighting{})
	if n := sample(t, p, "mediacore_events_published_total", map[string]string{"topic": "core.signal.lit.v1"}); n != 1 {
		t.Errorf("published = %v, want 1", n)
	}
}

// TestKeyFilter delivers only the keys the filter accepts.
func TestKeyFilter(t *testing.T) {
	b := newBus(t, events.BusOptions{})
	rec := newRecorder[Sighting]()
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.joker", rec.handle, events.KeyFilter(func(k string) bool { return k == "joker" }))))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	for _, k := range []string{"penguin", "joker", "riddler", "joker"} {
		events.Publish(b.Scope("gotham"), context.Background(), escaped(), k, Sighting{Villain: k})
	}
	rec.wait(t, 2)
	time.Sleep(20 * time.Millisecond)
	if got := rec.envelopes(); len(got) != 2 || got[0].Key != "joker" || got[1].Key != "joker" {
		t.Errorf("received %+v", got)
	}
}

// TestOnRaw_EncodeDropLogsNoPayload: a payload that fails the declared schema
// is dropped for raw subscribers (reason=encode, counted) and the warning
// names the topic, the subscriber, the reason and the fixed cause only: the
// validation error, which can quote the failing value, never reaches the log.
func TestOnRaw_EncodeDropLogsNoPayload(t *testing.T) {
	const sentinel = "ALFRED-SECRET-7731"
	p, sc := provider(t)
	var lb logBuf
	b, err := events.NewBus(events.BusOptions{Logger: lb.logger(), Metrics: sc})
	must(t, err)
	// A hand-built entry carries no payload type, so a Sighting publishes; its
	// schema refuses the villain field's value.
	must(t, b.Declare("gotham", events.TopicSpec{Name: "gotham.villain.escaped.v1", Permission: rbac.Need("gotham:view"),
		Schema: []byte(`{"type":"object","properties":{"villain":{"type":"string","pattern":"^[a-z]+$"}}}`)}))
	var got atomic.Int32
	typed := newRecorder[Sighting]()
	must(t, b.Subscribe("wayne", events.OnRaw("gotham.villain.escaped.v1", "wayne.watch", func(context.Context, events.RawEnvelope) { got.Add(1) })))
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.typed", typed.handle)))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	events.Publish(b.Scope("gotham"), context.Background(), escaped(), "joker", Sighting{Villain: sentinel, Block: 7})
	typed.wait(t, 1) // typed subscribers are unaffected
	eventually(t, "the encode drop", func() bool {
		return sample(t, p, "mediacore_events_dropped_total", map[string]string{"subscriber": "wayne.watch", "reason": "encode"}) == 1
	})
	if got.Load() != 0 {
		t.Error("the raw subscriber received a payload that fails its schema")
	}
	// The drop counter is bumped before the warning is written (sub.drop), so
	// the counter above does not order the log line: wait for the warning
	// itself, and only then assert what it does and does not contain.
	eventually(t, "the drop warning", func() bool { return lb.count("subscriber=wayne.watch") > 0 })
	out := lb.String()
	if !strings.Contains(out, "cause=schema") || !strings.Contains(out, "subscriber=wayne.watch") {
		t.Errorf("the drop warning lacks its cause or subscriber:\n%s", out)
	}
	if strings.Contains(out, sentinel) || strings.Contains(out, "ALFRED") {
		t.Errorf("the payload value reached the log:\n%s", out)
	}
}

// TestKeyFilter_SlowOrPanickingNeverBlocksPublish: a KeyFilter runs on its
// subscription's goroutine under the handler's recover, so a filter that
// blocks never holds up Publish, a filter that panics never breaks it (the
// panic is counted and the subscription keeps running), and other
// subscribers of the topic are delivered to either way.
func TestKeyFilter_SlowOrPanickingNeverBlocksPublish(t *testing.T) {
	p, sc := provider(t)
	b := newBus(t, events.BusOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Metrics: sc})
	release := make(chan struct{})
	slowEntered := make(chan struct{}, 1)
	slow := events.KeyFilter(func(string) bool {
		select {
		case slowEntered <- struct{}{}:
		default:
		}
		<-release
		return true
	})
	panicky := events.KeyFilter(func(k string) bool {
		if k == "joker" {
			panic("the filter broke")
		}
		return true
	})
	survivor, healthy := newRecorder[Sighting](), newRecorder[Sighting]()
	must(t, b.Subscribe("gotham",
		events.On(escaped(), "gotham.slow", func(context.Context, events.Envelope[Sighting]) {}, slow),
		events.On(escaped(), "gotham.panicky", survivor.handle, panicky),
		events.On(escaped(), "gotham.healthy", healthy.handle),
	))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	t.Cleanup(func() { close(release) }) // cleanups run last-in first: release, then Close

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i, k := range []string{"joker", "penguin", "riddler"} {
			events.Publish(b.Scope("gotham"), context.Background(), escaped(), k, Sighting{Villain: k, Block: i})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow or panicking KeyFilter")
	}
	<-slowEntered // the slow filter is running, on its own goroutine
	healthy.wait(t, 3)
	survivor.wait(t, 2) // joker skipped by the panic; penguin and riddler delivered
	if got := survivor.envelopes(); got[0].Key != "penguin" || got[1].Key != "riddler" {
		t.Errorf("after the filter panic the subscription received %+v", got)
	}
	if n := sample(t, p, "mediacore_events_handler_panics_total", map[string]string{"subscriber": "gotham.panicky"}); n != 1 {
		t.Errorf("filter panics counted = %v, want 1", n)
	}
}

// TestBus_CoalesceCloseDeliversNewestLast: an event taken while Close drains
// replaces its key's held event instead of overtaking it, so the last
// delivery for a key is its newest event.
func TestBus_CoalesceCloseDeliversNewestLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBus(t, events.BusOptions{})
		release := make(chan struct{})
		var mu sync.Mutex
		var joker []int
		must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.latest", func(_ context.Context, e events.Envelope[Sighting]) {
			if e.Key == "" {
				<-release // an uncoalesced event holds the goroutine
				return
			}
			mu.Lock()
			joker = append(joker, e.Payload.Block)
			mu.Unlock()
		}, events.Coalesce(time.Hour))))
		b.Freeze()
		must(t, b.Open())
		pub := b.Scope("gotham")
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Block: 1})
		synctest.Wait() // block 1 is held for its window
		events.Publish(pub, context.Background(), escaped(), "", Sighting{})
		synctest.Wait() // the handler is blocked
		events.Publish(pub, context.Background(), escaped(), "joker", Sighting{Block: 2})
		closed := make(chan error, 1)
		go func() { closed <- b.Close(context.Background()) }()
		synctest.Wait() // Close began; block 2 is queued
		close(release)
		must(t, <-closed)
		mu.Lock()
		defer mu.Unlock()
		if len(joker) != 1 || joker[0] != 2 {
			t.Errorf("joker deliveries = %v, want [2] (the newest, never an older one after it)", joker)
		}
	})
}

// TestBus_CloseNilMeansNoLaterHandler: publishers race Close under -race; once
// Close returns nil, no handler starts.
func TestBus_CloseNilMeansNoLaterHandler(t *testing.T) {
	for range 20 {
		b := newBus(t, events.BusOptions{})
		var closedNil atomic.Bool
		var late atomic.Int32
		must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.race", func(context.Context, events.Envelope[Sighting]) {
			if closedNil.Load() {
				late.Add(1)
			}
		}, events.QueueSize(8))))
		b.Freeze()
		must(t, b.Open())
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
						events.Publish(b.Scope("gotham"), context.Background(), escaped(), "", Sighting{})
					}
				}
			})
		}
		time.Sleep(time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		err := b.Close(ctx)
		cancel()
		if err == nil {
			closedNil.Store(true)
		}
		time.Sleep(5 * time.Millisecond)
		close(stop)
		wg.Wait()
		if n := late.Load(); n != 0 {
			t.Fatalf("%d handler(s) started after Close returned nil", n)
		}
	}
}

// TestPublish_RawEncodedOnPublisher: the JSON form is made inside Publish, so
// a publisher that changes its payload after Publish returns neither races
// the raw subscriber (-race) nor changes what it receives.
func TestPublish_RawEncodedOnPublisher(t *testing.T) {
	b, err := events.NewBus(events.BusOptions{})
	must(t, err)
	// A hand-built entry carries no payload type, so a map publishes.
	must(t, b.Declare("gotham", events.TopicSpec{Name: "gotham.villain.escaped.v1", Permission: rbac.Need("gotham:view")}))
	got := make(chan string, 1)
	release := make(chan struct{})
	must(t, b.Subscribe("wayne", events.OnRaw("gotham.villain.escaped.v1", "wayne.watch", func(_ context.Context, e events.RawEnvelope) {
		<-release
		got <- string(e.Payload)
	})))
	b.Freeze()
	must(t, b.Open())
	closeBus(t, b)
	payload := map[string]string{"villain": "joker"}
	events.Publish(b.Scope("gotham"), context.Background(), events.NewTopic[map[string]string]("gotham.villain.escaped.v1"), "joker", payload)
	payload["villain"] = "penguin" // the publisher reuses its map
	close(release)
	select {
	case p := <-got:
		if p != `{"villain":"joker"}` {
			t.Errorf("raw payload = %s, want the value at Publish", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
}
