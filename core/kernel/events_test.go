// file: core/kernel/events_test.go
// version: 1.0.0
// guid: b74915ac-bb38-4b1a-bec8-368ff1365993
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only: gotham declares a topic, wayne watches it.
type kernelSighting struct {
	Villain string `json:"villain"`
}

var kernelEscaped = events.NewTopic[kernelSighting]("gotham.villain.escaped.v1")

func newTestKernelEvents(t *testing.T, queueSize int, closeDrain time.Duration) *kernelEvents {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	ke, err := newKernelEvents(p, slog.New(slog.NewTextHandler(io.Discard, nil)), queueSize, closeDrain, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ke
}

// TestKernelEvents_Lifecycle walks the order F4-08 follows: declare core and
// modules, subscribe, freeze, scope, open, publish, close.
func TestKernelEvents_Lifecycle(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	if ke.closeDrain != defaultCloseDrain {
		t.Errorf("closeDrain = %v, want the default %v", ke.closeDrain, defaultCloseDrain)
	}
	if probs := ke.declareCore(coreTopics()); len(probs) != 0 {
		t.Fatalf("declareCore: %v", probs)
	}
	if probs := ke.declare("gotham", []events.TopicSpec{kernelEscaped.Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")})}); len(probs) != 0 {
		t.Fatalf("declare: %v", probs)
	}
	ke.declareDisabled("arkham")
	got := make(chan string, 1)
	if probs := ke.subscribe("wayne", []events.Subscription{events.OnRaw(kernelEscaped.Name(), "wayne.watch",
		func(_ context.Context, e events.RawEnvelope) { got <- string(e.Payload) })}); len(probs) != 0 {
		t.Fatalf("subscribe: %v", probs)
	}
	var coreSeen atomic.Int32
	if probs := ke.subscribe("", []events.Subscription{events.OnRaw(kernelEscaped.Name(), "core.audit",
		func(context.Context, events.RawEnvelope) { coreSeen.Add(1) })}); len(probs) != 0 {
		t.Fatalf("core subscribe (id \"\" is core): %v", probs)
	}
	ke.freeze()
	if _, ok := ke.catalog().Lookup(kernelEscaped.Name()); !ok {
		t.Errorf("the catalog lacks %s", kernelEscaped.Name())
	}
	if err := ke.open(); err != nil {
		t.Fatal(err)
	}
	events.Publish(ke.scope("gotham"), context.Background(), kernelEscaped, "joker", kernelSighting{Villain: "joker"})
	select {
	case p := <-got:
		if p != `{"villain":"joker"}` {
			t.Errorf("payload = %s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	if err := ke.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := coreSeen.Load(); n != 1 {
		t.Errorf("core subscriber saw %d events, want 1", n)
	}
	if err := ke.open(); !errors.Is(err, events.ErrClosed) {
		t.Errorf("open after close = %v, want ErrClosed", err)
	}
}

// TestKernelEvents_ProblemKinds: each registration refusal becomes one module
// problem per leaf, with the kind naming the same rule and the events
// sentinel still reachable through errors.Is.
func TestKernelEvents_ProblemKinds(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	durable := events.NewTopic[kernelSighting]("gotham.villain.jailed.v1").
		Spec(events.TopicSpec{Permission: rbac.Need("gotham:view"), Durable: &events.Durable{}})
	probs := ke.declare("gotham", []events.TopicSpec{
		kernelEscaped.Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}),
		durable,
		events.NewTopic[kernelSighting]("wayne.vault.opened.v1").Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}),
		events.NewTopic[kernelSighting]("gotham.villain.caught.v1").Spec(events.TopicSpec{}),
		events.NewTopic[kernelSighting]("gotham.villain.fled.v1").Spec(events.TopicSpec{Permission: rbac.Need("wayne:view")}),
	})
	want := []struct {
		kind module.ProblemKind
		err  error
	}{
		{module.ProblemTopicNamespace, events.ErrTopicNamespace},
		{module.ProblemInvalidRegistration, events.ErrNoPermission},
		{module.ProblemPermissionNamespace, events.ErrForeignScope},
	}
	check := func(stage string, probs []module.Problem, owner module.ID, want []struct {
		kind module.ProblemKind
		err  error
	}) {
		t.Helper()
		if len(probs) != len(want) {
			t.Fatalf("%s: %d problems, want %d: %v", stage, len(probs), len(want), probs)
		}
		for i, w := range want {
			p := probs[i]
			if p.Module != owner || p.Kind != w.kind || !errors.Is(p, w.err) {
				t.Errorf("%s problem %d = {%q %v %v}, want {%q %v %v}", stage, i, p.Module, p.Kind, p, owner, w.kind, w.err)
			}
		}
	}
	check("declare", probs, "gotham", want)

	subs := ke.subscribe("wayne", []events.Subscription{
		events.OnRaw("gotham.villain.unknown.v1", "wayne.unknown", func(context.Context, events.RawEnvelope) {}),
		events.OnRaw(durable.Name, "wayne.durable", func(context.Context, events.RawEnvelope) {}),
		events.On(kernelEscaped, "wayne.typed", func(context.Context, events.Envelope[kernelSighting]) {}),
		events.OnRaw(kernelEscaped.Name(), "wayne.bad", func(context.Context, events.RawEnvelope) {}, events.QueueSize(0)),
	})
	check("subscribe", subs, "wayne", []struct {
		kind module.ProblemKind
		err  error
	}{
		{module.ProblemTopicUndeclared, events.ErrTopicUndeclared},
		{module.ProblemTopicUndeclared, events.ErrDurableTopic},
		{module.ProblemTopicNamespace, events.ErrForeignTopic},
		{module.ProblemInvalidRegistration, events.ErrInvalidSubscription},
	})
	if probs := ke.declareCore(nil); probs != nil {
		t.Errorf("declareCore(nil) = %v, want none", probs)
	}
}

// TestKernelEvents_CloseBoundedByCloseDrain: close gives up after
// core.events.close_drain even when the caller's budget is longer.
func TestKernelEvents_CloseBoundedByCloseDrain(t *testing.T) {
	ke := newTestKernelEvents(t, 4, 50*time.Millisecond)
	if probs := ke.declare("gotham", []events.TopicSpec{kernelEscaped.Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")})}); len(probs) != 0 {
		t.Fatal(probs)
	}
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	if probs := ke.subscribe("gotham", []events.Subscription{events.On(kernelEscaped, "gotham.stuck",
		func(context.Context, events.Envelope[kernelSighting]) {
			entered <- struct{}{}
			<-release
		})}); len(probs) != 0 {
		t.Fatal(probs)
	}
	defer close(release)
	ke.freeze()
	if err := ke.open(); err != nil {
		t.Fatal(err)
	}
	events.Publish(ke.scope("gotham"), context.Background(), kernelEscaped, "penguin", kernelSighting{Villain: "penguin"})
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	err := ke.close(ctx)
	if !errors.Is(err, events.ErrClosed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("close = %v, want ErrClosed wrapping the drain deadline", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("close took %v; close_drain is 50ms", d)
	}
}
