// file: internal/opsmetrics/retire_test.go
// version: 0.1.0
// guid: b3e8c1d4-6a27-4f95-9d0e-7c52a1f8e639
// last-edited: 2026-11-22

package opsmetrics

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// globalRig installs a real SDK provider as the process's global one for the
// test (Default reads it on first use) and gives back what it has collected.
func globalRig(t *testing.T) func() int {
	t.Helper()
	prev := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	resetDefaultForTest()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		resetDefaultForTest()
	})
	return func() int {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("Collect: %v", err)
		}
		n := 0
		for _, sm := range rm.ScopeMetrics {
			n += len(sm.Metrics)
		}
		return n
	}
}

// TestRetire_DefaultClaimsNothing: after Retire the recorder Default hands out
// records nothing on the global provider, so the audiobook_organizer_ops_*
// families stay free for core's <ns>.ops scope (the tasks flip). The control is
// the same calls without Retire, which do reach the provider.
func TestRetire_DefaultClaimsNothing(t *testing.T) {
	t.Run("control: a live Default records", func(t *testing.T) {
		collect := globalRig(t)
		Default().Started("library.scan")
		if collect() == 0 {
			t.Fatal("a live Default recorded no series: the retired case below proves nothing")
		}
	})
	t.Run("retired", func(t *testing.T) {
		collect := globalRig(t)
		if err := Retire(); err != nil {
			t.Fatalf("Retire before the first Default: %v", err)
		}
		Default().Started("library.scan")
		Default().Finished("library.scan", OutcomeCompleted, 0)
		if n := collect(); n != 0 {
			t.Fatalf("a retired Default put %d series on the global provider, want 0", n)
		}
	})
}

// TestRetire_AfterDefaultIsLoud: Default is created once per process, so a
// Retire after it cannot take the claim back. It must say so (the books
// Register turns it into a boot error), never succeed quietly; and once retired
// a second Retire is a no-op, since every boot under the flip calls it.
func TestRetire_AfterDefaultIsLoud(t *testing.T) {
	globalRig(t)
	Default()
	if err := Retire(); !errors.Is(err, ErrRetireTooLate) {
		t.Fatalf("Retire after Default = %v, want ErrRetireTooLate", err)
	}
	// A fresh process: Retire first, then Default, then Retire again.
	resetDefaultForTest()
	if err := Retire(); err != nil {
		t.Fatalf("Retire on a fresh process: %v", err)
	}
	if err := Retire(); err != nil {
		t.Fatalf("a second Retire = %v, want nil", err)
	}
	Default()
	if err := Retire(); err != nil {
		t.Fatalf("Retire after Default on an already retired process = %v, want nil", err)
	}
}
