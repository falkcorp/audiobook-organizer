// file: internal/operations/registry/metrics_otel_test.go
// version: 1.0.0
// guid: 4d7c1e92-a3b8-4f06-8c5d-9e2f0b61a7d3
// last-edited: 2026-10-10

package registry_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// otelRig is a registry wired to a private meter provider, so a test reads
// exactly what the registry recorded and nothing a sibling test did.
type otelRig struct {
	reader *sdkmetric.ManualReader
	reg    *registry.Registry
	store  *fakeStore
}

func newOtelRig(t *testing.T) *otelRig {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	store := newFakeStore()
	reg := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{OpsMetrics: opsmetrics.New(mp)})
	return &otelRig{reader: reader, reg: reg, store: store}
}

func (g *otelRig) collect(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := g.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func attrIs(set attribute.Set, key, want string) bool {
	v, ok := set.Value(attribute.Key(key))
	return ok && v.AsString() == want
}

// counterValue is the ops.runs / ops.items point for (defID, outcome); 0 when
// there is none.
func (g *otelRig) counterValue(t *testing.T, name, defID, outcome string) int64 {
	t.Helper()
	m, ok := g.collect(t)[name]
	if !ok {
		return 0
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s is %T, want Sum[int64]", name, m.Data)
	}
	for _, dp := range sum.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) && attrIs(dp.Attributes, "outcome", outcome) {
			return dp.Value
		}
	}
	return 0
}

func (g *otelRig) inflight(t *testing.T, defID string) int64 {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.inflight"]
	if !ok {
		return 0
	}
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("ops.inflight is %T, want Gauge[int64]", m.Data)
	}
	for _, dp := range gauge.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) {
			return dp.Value
		}
	}
	return 0
}

// durationCount is the number of ops.run.duration observations for (defID, outcome).
func (g *otelRig) durationCount(t *testing.T, defID, outcome string) uint64 {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.run.duration"]
	if !ok {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("ops.run.duration is %T, want Histogram[float64]", m.Data)
	}
	for _, dp := range h.DataPoints {
		if attrIs(dp.Attributes, "def_id", defID) && attrIs(dp.Attributes, "outcome", outcome) {
			return dp.Count
		}
	}
	return 0
}

func waitForOtel(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOpsMetrics_RunToCompletion(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.otel-complete"
	inRun, release := make(chan struct{}), make(chan struct{})
	def := makeValidDef(id)
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		_ = rep.UpdateProgress(10, 40, "")
		close(inRun)
		<-release
		return nil
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })

	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-inRun
	if got := g.inflight(t, id); got != 1 {
		t.Errorf("ops.inflight during the run = %d, want 1", got)
	}
	if got := g.counterValue(t, "audiobook_organizer.ops.runs", id, "started"); got != 1 {
		t.Errorf("runs{started} = %d, want 1", got)
	}
	close(release)
	awaitStatus(t, g.store, opID, "completed", 5*time.Second)
	waitForOtel(t, "runs{completed}", func() bool {
		return g.counterValue(t, "audiobook_organizer.ops.runs", id, "completed") == 1
	})
	if got := g.durationCount(t, id, "completed"); got != 1 {
		t.Errorf("run.duration{completed} observations = %d, want 1", got)
	}
	if got := g.counterValue(t, "audiobook_organizer.ops.items", id, "processed"); got != 10 {
		t.Errorf("items{processed} = %d, want 10", got)
	}
	waitForOtel(t, "ops.inflight back to 0", func() bool { return g.inflight(t, id) == 0 })
}

func TestOpsMetrics_RunToFailure(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.otel-failed"
	def := makeValidDef(id)
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { return errors.New("boom") }
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })

	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, g.store, opID, "failed", 5*time.Second)
	waitForOtel(t, "runs{failed}", func() bool {
		return g.counterValue(t, "audiobook_organizer.ops.runs", id, "failed") == 1
	})
	if got := g.counterValue(t, "audiobook_organizer.ops.runs", id, "started"); got != 1 {
		t.Errorf("runs{started} = %d, want 1", got)
	}
	if got := g.counterValue(t, "audiobook_organizer.ops.runs", id, "completed"); got != 0 {
		t.Errorf("runs{completed} = %d after a failed run, want 0", got)
	}
	if got := g.durationCount(t, id, "failed"); got != 1 {
		t.Errorf("run.duration{failed} observations = %d, want 1", got)
	}
}

// TestOpsMetrics_ProgressCountsHighWaterMark: progress 10, 25, 25, 20, 25 adds 25
// items in total; the decrease adds nothing and does not lower the mark, so climbing back to 25 adds nothing.
func TestOpsMetrics_ProgressCountsHighWaterMark(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.otel-items"
	def := makeValidDef(id)
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		for _, cur := range []int{10, 25, 25, 20, 25} {
			_ = rep.UpdateProgress(cur, 100, "")
		}
		return nil
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })

	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, g.store, opID, "completed", 5*time.Second)
	if got := g.counterValue(t, "audiobook_organizer.ops.items", id, "processed"); got != 25 {
		t.Errorf("items{processed} = %d, want 25", got)
	}
}

// TestShutdownUnregistersInflightCallback: a registry started and shut down 20
// times leaves no callback behind.
func TestShutdownUnregistersInflightCallback(t *testing.T) {
	before := opsmetrics.ActiveInflightCallbacks()
	for i := 0; i < 20; i++ {
		g := newOtelRig(t)
		g.reg.Start(context.Background())
		if got := opsmetrics.ActiveInflightCallbacks(); got != before+1 {
			t.Fatalf("round %d: %d callbacks after Start, want %d", i, got, before+1)
		}
		if err := g.reg.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := opsmetrics.ActiveInflightCallbacks(); got != before {
			t.Fatalf("round %d: %d callbacks after Shutdown, want %d", i, got, before)
		}
	}
}
