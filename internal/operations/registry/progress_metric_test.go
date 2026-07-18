// file: internal/operations/registry/progress_metric_test.go
// version: 1.0.0
// guid: 3c1d9e7a-4b6f-4a2c-8e0d-5f9a1b2c3d4e
// last-edited: 2026-07-18

package registry_test

// Tests for the OPS-5 op-progress Prometheus exporter (TODO #36):
// dbReporter.UpdateProgress sets audiobook_organizer_op_items_processed/
// _total (internal/metrics.SetOpProgress), and registry.publishOpTerminal
// clears both series on every terminal transition (internal/metrics.
// ClearOpProgress) so stale op_ids don't accumulate as label series. These
// tests exercise the real dbReporter/worker path end-to-end (not the metrics
// package in isolation — that's covered by internal/metrics/metrics_test.go)
// by reading back the global Prometheus registry, since the underlying
// gauges are unexported and this test lives in the external registry_test
// package.

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/prometheus/client_golang/prometheus"
)

// b2gaugeValue polls the global Prometheus DefaultGatherer for a gauge metric
// family named `name` carrying label op_id=opID, returning (value, found).
// Reading via Gather (rather than a package-level accessor) avoids the need
// for any test-only export on the metrics package and — critically — never
// creates a series as a side effect the way GaugeVec.WithLabelValues would,
// so it can truthfully assert a series is ABSENT after ClearOpProgress.
// Task-unique name (b2xxx) per the parallel-test-helper-collision rule.
func b2gaugeValue(t *testing.T, name, opID string) (value float64, found bool) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
		for _, m := range fam.GetMetric() {
			if b2metricHasLabel(m, "op_id", opID) {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func b2metricHasLabel(m *dto.Metric, key, want string) bool {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == key && lp.GetValue() == want {
			return true
		}
	}
	return false
}

// b2waitGaugeAbsent polls until the named gauge series for opID is gone (or
// fails the test after timeout). Used to assert ClearOpProgress ran — the
// deletion happens synchronously inside registry.publishOpTerminal, but that
// call itself happens on the worker goroutine racing this test goroutine.
func b2waitGaugeAbsent(t *testing.T, name, opID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, found := b2gaugeValue(t, name, opID); !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gauge %s{op_id=%q} still present after %v", name, opID, timeout)
}

// TestProgressMetric_SetOnUpdateAndClearedOnTerminal is the end-to-end proof
// for TODO #36: a running op's UpdateProgress call is visible as
// audiobook_organizer_op_items_processed/_total, and once the op reaches a
// terminal state ("completed") both series are gone.
func TestProgressMetric_SetOnUpdateAndClearedOnTerminal(t *testing.T) {
	metrics.Register()

	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	release := make(chan struct{})
	reported := make(chan struct{})

	def := makeValidDef("test.progress-metric-lifecycle")
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		if err := rep.UpdateProgress(3, 10, "a third of the way"); err != nil {
			return err
		}
		close(reported)
		<-release // hold the op "running" until the test has observed the gauge
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(context.Background())

	opID, err := r.EnqueueOp(context.Background(), "test.progress-metric-lifecycle", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	select {
	case <-reported:
	case <-time.After(5 * time.Second):
		t.Fatal("op never reported progress")
	}

	if got, found := b2gaugeValue(t, "audiobook_organizer_op_items_processed", opID); !found || got != 3 {
		t.Fatalf("op_items_processed{op_id=%q} = %v, found=%v; want 3, true", opID, got, found)
	}
	if got, found := b2gaugeValue(t, "audiobook_organizer_op_items_total", opID); !found || got != 10 {
		t.Fatalf("op_items_total{op_id=%q} = %v, found=%v; want 10, true", opID, got, found)
	}

	close(release)
	awaitStatus(t, store, opID, "completed", 5*time.Second)
	bus.waitTerminal(t, opID, 5*time.Second)

	b2waitGaugeAbsent(t, "audiobook_organizer_op_items_processed", opID, 2*time.Second)
	b2waitGaugeAbsent(t, "audiobook_organizer_op_items_total", opID, 2*time.Second)
}

// TestProgressMetric_ClearedOnCancel proves the cleanup path also fires on a
// cancel-while-running terminal transition, not just normal completion.
func TestProgressMetric_ClearedOnCancel(t *testing.T) {
	metrics.Register()

	store := newFakeStore()
	bus := &t06TermBus{}
	r := registry.New(store, slog.Default(), 1, bus)

	started := make(chan struct{})

	def := makeValidDef("test.progress-metric-cancel")
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		if err := rep.UpdateProgress(1, 0, "started"); err != nil {
			return err
		}
		close(started)
		<-runCtx.Done() // honor cancellation promptly (not abandoned)
		return runCtx.Err()
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(context.Background())

	opID, err := r.EnqueueOp(context.Background(), "test.progress-metric-cancel", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("op did not start")
	}

	if _, found := b2gaugeValue(t, "audiobook_organizer_op_items_processed", opID); !found {
		t.Fatalf("op_items_processed{op_id=%q} not set before cancel", opID)
	}

	if err := r.Cancel(opID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	awaitStatus(t, store, opID, "canceled", 5*time.Second)
	bus.waitTerminal(t, opID, 5*time.Second)

	b2waitGaugeAbsent(t, "audiobook_organizer_op_items_processed", opID, 2*time.Second)
	b2waitGaugeAbsent(t, "audiobook_organizer_op_items_total", opID, 2*time.Second)
}
