// file: core/events/metrics_test.go
// version: 1.0.0
// guid: 520b0232-33e4-4bcb-a374-1bf9c6ea34ef
// last-edited: 2026-11-10

package events_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// TestEventsMetrics_SeriesGolden (05-metrics §5 item 5, §12): the core events
// scope serves exactly the bus families of testdata/series.golden. A family
// with no sample exports nothing, so the test publishes, delivers, rejects,
// drops and panics once each, with a subscription live for the gauge.
func TestEventsMetrics_SeriesGolden(t *testing.T) {
	p, sc := provider(t)
	b := newBus(t, events.BusOptions{Metrics: sc})
	done := newRecorder[Sighting]()
	must(t, b.Subscribe("gotham", events.On(escaped(), "gotham.golden", func(ctx context.Context, e events.Envelope[Sighting]) {
		defer done.handle(ctx, e)
		if e.Payload.Block == 1 {
			panic("a Joker card")
		}
	})))
	b.Freeze()
	pub := b.Scope("gotham")
	events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 1})
	events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 2})
	events.Publish(pub, context.Background(), events.NewTopic[Sighting]("gotham.villain.vanished.v1"), "", Sighting{})
	must(t, b.Open())
	done.wait(t, 2)
	must(t, b.Close(context.Background()))
	events.Publish(pub, context.Background(), escaped(), "", Sighting{Block: 3}) // a closed drop
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)
	if n := sample(t, p, "mediacore_events_dropped_total", dropLabels("gotham.golden", "closed")); n != 1 {
		t.Errorf("closed drops = %v, want 1", n)
	}
}

// TestNoGlobals_EventsOutbox is the events half of 05-events §5E.12's R5
// test: no init and no package-level mutable state in events (the AST scan
// of archtest.NoInit over ./events/...), and two buses share nothing: a
// subscription, a catalog entry or a lifecycle of one never reaches the other.
func TestNoGlobals_EventsOutbox(t *testing.T) {
	wd, err := os.Getwd()
	must(t, err)
	root, err := archtest.ModuleRoot(wd)
	must(t, err)
	archtest.NoInit(t, root, "./events/...")

	one := newBus(t, events.BusOptions{})
	two, err := events.NewBus(events.BusOptions{})
	must(t, err)
	got := newRecorder[Sighting]()
	must(t, one.Subscribe("gotham", events.On(escaped(), "gotham.one", got.handle)))
	if _, ok := two.Catalog().Lookup("gotham.villain.escaped.v1"); ok {
		t.Error("a topic declared on one bus is in another bus's catalog")
	}
	// The same subscription name is free on the second bus.
	must(t, two.Declare("gotham", spec(escaped(), "gotham:view")))
	must(t, two.Subscribe("gotham", events.On(escaped(), "gotham.one", got.handle)))
	one.Freeze()
	two.Freeze()
	must(t, one.Open())
	must(t, two.Close(context.Background()))
	if err := two.Open(); !errors.Is(err, events.ErrClosed) {
		t.Errorf("closed bus Open: err = %v", err)
	}
	events.Publish(one.Scope("gotham"), context.Background(), escaped(), "", Sighting{Block: 1})
	got.wait(t, 1)
	must(t, one.Close(context.Background()))
}
