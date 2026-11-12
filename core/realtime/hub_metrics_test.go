// file: core/realtime/hub_metrics_test.go
// version: 1.0.0
// guid: 6d2f8b41-9c35-4a7e-b0d9-3e1a5c7f2846
// last-edited: 2026-11-11

package realtime_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/realtime"
)

func realtimeScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("realtime", attribute.Key("topic"), "reason")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// series is the value of family's series whose labels include want, or -1.
func series(t *testing.T, g prometheus.Gatherer, family string, want map[string]string) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return -1
}

// TestRealtimeSeries_Golden (05-realtime §9, 05-metrics §5 item 5): the
// realtime scope serves exactly testdata/series.golden, and each family moves
// with what it counts.
func TestRealtimeSeries_Golden(t *testing.T) {
	p, sc := realtimeScope(t)
	c := newCast(t, hubOptions{
		opts: realtime.Options{RingSize: 4, ClientBuffer: 2, MaxClients: 3, MaxPerBucket: 2, MaxPayload: 128},
		deps: func(d *realtime.Deps) { d.Metrics = sc },
	})
	g := p.Gatherer()
	rd := c.open(t, "dick", "/events?topic="+tSignal+"&topic="+tAlarm+"&topic="+tRetired)
	rd.expectOpened()
	c.open(t, "dick", "/events?topic="+tSignal).expectOpened()
	if _, resp := open(t, c.srv, "dick", "/events"); resp.StatusCode != 429 {
		t.Fatalf("third stream for one user: %d", resp.StatusCode)
	}
	c.open(t, "lucius", "/events?topic="+tSignal).expectOpened()
	if _, resp := open(t, c.srv, "bruce", "/events"); resp.StatusCode != 503 {
		t.Fatalf("fourth stream: %d", resp.StatusCode)
	}
	if _, resp := open(t, c.srv, "dick", "/events?topic="+tCell); resp.StatusCode != 403 {
		t.Fatalf("a topic dick may not stream: %d", resp.StatusCode)
	}

	c.signal(topicSignal, "", "lit", "public")
	c.signal(topicAlarm, "", "quiet", "private")                   // dick: not visible
	c.signal(topicSignal, "", string(make([]byte, 300)), "public") // oversize
	for range 12 {                                                 // far more than the ring: a resume below gets a gap
		c.signal(topicSignal, "", "again", "public")
	}
	waitFor(t, func() bool {
		return series(t, g, "mediacore_realtime_events_published_total", map[string]string{"topic": tSignal}) >= 13
	})
	if got := series(t, g, "mediacore_realtime_events_delivered_total", map[string]string{"topic": tSignal}); got < 1 {
		t.Errorf("delivered = %v", got)
	}
	waitFor(t, func() bool {
		return series(t, g, "mediacore_realtime_events_filtered_total", map[string]string{"topic": tAlarm, "reason": "visible"}) == 1
	})
	waitFor(t, func() bool {
		return series(t, g, "mediacore_realtime_events_dropped_total", map[string]string{"reason": "oversize"}) == 1
	})
	if got := series(t, g, "mediacore_realtime_stream_refusals_total", map[string]string{"reason": "bucket_cap"}); got != 1 {
		t.Errorf("bucket_cap refusals = %v", got)
	}
	if got := series(t, g, "mediacore_realtime_stream_refusals_total", map[string]string{"reason": "global_cap"}); got != 1 {
		t.Errorf("global_cap refusals = %v", got)
	}
	if got := series(t, g, "mediacore_realtime_stream_refusals_total", map[string]string{"reason": "denied"}); got != 1 {
		t.Errorf("denied refusals = %v", got)
	}
	rd.close()
	waitFor(t, func() bool {
		return series(t, g, "mediacore_realtime_streams_closed_total", map[string]string{"reason": "client"}) >= 1
	})
	// A resume from another process and one from before the ring.
	c.open(t, "dick", "/events?topic="+tSignal, "Last-Event-ID", "otherboot-1").expectOpened()
	waitFor(t, func() bool {
		return series(t, g, "mediacore_realtime_resets_total", map[string]string{"reason": "restart"}) == 1
	})
	if got := series(t, g, "mediacore_realtime_subscribers", nil); got != float64(c.hub.Subscribers()) {
		t.Errorf("subscribers gauge = %v, hub says %d", got, c.hub.Subscribers())
	}
	if got := series(t, g, "mediacore_realtime_ring_oldest_age_seconds", nil); got < 0 || got > float64(time.Minute/time.Second) {
		t.Errorf("ring oldest age = %v", got)
	}
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)
}

func TestNoGlobals(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"./realtime/...", "./internal/sse/...", "./internal/eventhttp/..."} {
		archtest.NoInit(t, root, p)
	}
}
