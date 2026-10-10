// file: internal/aidispatch/metrics_test.go
// version: 1.0.0
// guid: 5d0c8f1e-3a47-4c52-9b6e-72a1f0e4c9d3
// last-edited: 2026-10-10

package aidispatch

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// useReader points the package instruments at a private provider backed by a
// manual reader (with the production views) and resets the lazy creation so
// the next Call builds them against it. A private provider is used instead of
// otel.SetMeterProvider because the global can only be bound once per process.
func useReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	metricsMP = mp
	metricsOnce = sync.Once{}
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		metricsMP = nil
		metricsOnce = sync.Once{}
	})
	return reader
}

func collect(t *testing.T, r *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// sumPoints returns attribute set (rendered k=v,...) -> value for a sum metric.
func sumPoints(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: data is %T, want Sum[int64]", m.Name, m.Data)
	}
	out := map[string]int64{}
	for _, dp := range s.DataPoints {
		out[renderAttrs(dp.Attributes)] = dp.Value
	}
	return out
}

func renderAttrs(s attribute.Set) string {
	var parts []string
	for _, kv := range s.ToSlice() {
		parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
	}
	return strings.Join(parts, ",")
}

func okFn(context.Context, Target) (string, error) { return "ok", nil }

func TestMetrics_SuccessfulCallRecordsRequestsAndSlotWait(t *testing.T) {
	r := useReader(t)
	cp := LLMFilenameParse
	d := isolated([]Endpoint{chatEP("ep-a", 1, cp.ID())})
	if _, err := Call(context.Background(), d, cp, okFn); err != nil {
		t.Fatalf("Call: %v", err)
	}
	got := collect(t, r)
	reqs := sumPoints(t, got["ai_dispatch.requests"])
	want := map[string]int64{"capability=" + cp.ID() + ",endpoint=ep-a,outcome=" + ClassOK.String(): 1}
	if len(reqs) != 1 || reqs[firstKey(want)] != 1 {
		t.Fatalf("requests = %v, want %v", reqs, want)
	}
	h, ok := got["ai_dispatch.slot_wait"].Data.(metricdata.Histogram[float64])
	if !ok || len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
		t.Fatalf("slot_wait = %+v, want one observation", got["ai_dispatch.slot_wait"].Data)
	}
	if v, _ := h.DataPoints[0].Attributes.Value("endpoint"); v.AsString() != "ep-a" {
		t.Fatalf("slot_wait endpoint = %q", v.AsString())
	}
}

func firstKey(m map[string]int64) string {
	for k := range m {
		return k
	}
	return ""
}

func TestMetrics_FailoverRecordsFailoverAndTwoRequests(t *testing.T) {
	r := useReader(t)
	cp := LLMFilenameParse
	d := isolated([]Endpoint{chatEP("ep-a", 1, cp.ID()), chatEP("ep-b", 2, cp.ID())})
	_, err := Call(context.Background(), d, cp, func(_ context.Context, tg Target) (string, error) {
		if tg.Endpoint.ID == "ep-a" {
			return "", dialRefused
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	got := collect(t, r)
	fo := sumPoints(t, got["ai_dispatch.failover"])
	if len(fo) != 1 {
		t.Fatalf("failover = %v, want one series", fo)
	}
	for k, v := range fo {
		if v != 1 || !strings.Contains(k, "endpoint=ep-a") || !strings.Contains(k, "capability="+cp.ID()) || !strings.Contains(k, "class="+ClassFailover.String()) {
			t.Fatalf("failover = %v, want ep-a class=%s", fo, ClassFailover)
		}
	}
	var total int64
	for _, v := range sumPoints(t, got["ai_dispatch.requests"]) {
		total += v
	}
	if total != 2 {
		t.Fatalf("requests total = %d, want 2", total)
	}
}

func TestMetrics_NoCapableEndpoint(t *testing.T) {
	r := useReader(t)
	cp := LLMFilenameParse
	d := isolated(nil)
	if _, err := Call(context.Background(), d, cp, okFn); err == nil {
		t.Fatal("Call: want no-capable error")
	}
	got := collect(t, r)
	nc := sumPoints(t, got["ai_dispatch.no_capable"])
	if len(nc) != 1 || nc["capability="+cp.ID()] != 1 {
		t.Fatalf("no_capable = %v", nc)
	}
	if _, ok := got["ai_dispatch.requests"]; ok {
		t.Fatalf("requests recorded for a refused call: %+v", got["ai_dispatch.requests"])
	}
}

func TestMetrics_InflightReturnsToZero(t *testing.T) {
	cp := LLMFilenameParse
	run := func(t *testing.T, fn func(context.Context, Target) (string, error), recoverPanic bool) {
		r := useReader(t)
		d := isolated([]Endpoint{chatEP("ep-a", 1, cp.ID())})
		func() {
			if recoverPanic {
				defer func() { _ = recover() }()
			}
			_, _ = Call(context.Background(), d, cp, fn)
		}()
		in := sumPoints(t, collect(t, r)["ai_dispatch.inflight"])
		if in["endpoint=ep-a"] != 0 {
			t.Fatalf("inflight after call = %v, want 0", in)
		}
	}
	t.Run("during fn the sum is 1", func(t *testing.T) {
		r := useReader(t)
		d := isolated([]Endpoint{chatEP("ep-a", 1, cp.ID())})
		var during int64 = -1
		_, err := Call(context.Background(), d, cp, func(context.Context, Target) (string, error) {
			during = sumPoints(t, collect(t, r)["ai_dispatch.inflight"])["endpoint=ep-a"]
			return "ok", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if during != 1 {
			t.Fatalf("inflight during fn = %d, want 1", during)
		}
		if after := sumPoints(t, collect(t, r)["ai_dispatch.inflight"])["endpoint=ep-a"]; after != 0 {
			t.Fatalf("inflight after fn = %d, want 0", after)
		}
	})
	t.Run("after success", func(t *testing.T) { run(t, okFn, false) })
	t.Run("after panic", func(t *testing.T) {
		run(t, func(context.Context, Target) (string, error) { panic("boom") }, true)
	})
	t.Run("after cancelled ctx", func(t *testing.T) {
		r := useReader(t)
		d := isolated([]Endpoint{chatEP("ep-a", 1, cp.ID())})
		ctx, cancel := context.WithCancel(context.Background())
		_, _ = Call(ctx, d, cp, func(context.Context, Target) (string, error) {
			cancel()
			return "", ctx.Err()
		})
		if in := sumPoints(t, collect(t, r)["ai_dispatch.inflight"])["endpoint=ep-a"]; in != 0 {
			t.Fatalf("inflight after cancel = %d, want 0", in)
		}
	})
}

func TestMetrics_AttributeKeysAreTheLegacyLabelNames(t *testing.T) {
	r := useReader(t)
	cp := LLMFilenameParse
	d := isolated([]Endpoint{chatEP("ep-a", 1, cp.ID()), chatEP("ep-b", 2, cp.ID())})
	_, _ = Call(context.Background(), d, cp, func(_ context.Context, tg Target) (string, error) {
		if tg.Endpoint.ID == "ep-a" {
			return "", dialRefused
		}
		return "ok", nil
	})
	_, _ = Call(context.Background(), isolated(nil), cp, okFn)
	got := collect(t, r)
	want := map[string][]string{
		"ai_dispatch.requests":   {"capability", "endpoint", "outcome"},
		"ai_dispatch.inflight":   {"endpoint"},
		"ai_dispatch.failover":   {"capability", "class", "endpoint"},
		"ai_dispatch.no_capable": {"capability"},
		"ai_dispatch.slot_wait":  {"endpoint"},
	}
	for name, keys := range want {
		m, ok := got[name]
		if !ok {
			t.Fatalf("%s not recorded", name)
		}
		var sets []attribute.Set
		switch d := m.Data.(type) {
		case metricdata.Sum[int64]:
			for _, dp := range d.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		case metricdata.Histogram[float64]:
			for _, dp := range d.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		}
		if len(sets) == 0 {
			t.Fatalf("%s: no data points", name)
		}
		for _, s := range sets {
			var have []string
			for _, kv := range s.ToSlice() {
				have = append(have, string(kv.Key))
			}
			slices.Sort(have)
			if !slices.Equal(have, keys) {
				t.Errorf("%s attribute keys = %v, want %v", name, have, keys)
			}
		}
	}
}

func TestSlotWaitBucketsEqualLegacy(t *testing.T) {
	want := prometheus.ExponentialBuckets(0.001, 4, 10)
	got := telemetry.HistogramBuckets()["ai_dispatch.slot_wait"]
	if !slices.Equal(got, want) {
		t.Fatalf("view boundaries = %v, want %v", got, want)
	}
	// And the view is what the SDK applies to the recorded instrument.
	r := useReader(t)
	cp := LLMFilenameParse
	if _, err := Call(context.Background(), isolated([]Endpoint{chatEP("ep-a", 1, cp.ID())}), cp, okFn); err != nil {
		t.Fatal(err)
	}
	h := collect(t, r)["ai_dispatch.slot_wait"].Data.(metricdata.Histogram[float64])
	if !slices.Equal(h.DataPoints[0].Bounds, want) {
		t.Fatalf("exported bounds = %v, want %v", h.DataPoints[0].Bounds, want)
	}
}

func TestMetrics_NoSeriesBeforeAnyCall(t *testing.T) {
	r := useReader(t)
	RegisterMetrics()
	if got := collect(t, r); len(got) != 0 {
		t.Fatalf("instruments created but never recorded exported %d metrics: %v", len(got), got)
	}
}
