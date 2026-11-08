// file: core/coretest/metrics.go
// version: 1.0.1
// guid: 9f2b6d18-3e7c-4a51-b0d9-6c4e8a2f1b73
// last-edited: 2026-11-07

package coretest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/metrics"
)

// MetricsProvider boots one zero-module App with opts (MetricNamespace,
// ServiceName) and returns that boot's metrics.Provider, so a series contract
// test scrapes Provider.Handler(), never promhttp.Handler() (06 §6.4.8,
// 05-metrics §6). It boots rather than only building a Provider because the
// kernel's own families (<MetricNamespace>_kernel_boot_phase_seconds at R1)
// are written by kernel code during a boot, and a family with no sample
// exports nothing (OPS8-10). The kernel applies its metrics.Options defaults
// exactly as in production. The R1 skeleton has no Harness yet (R1-37), so
// the boot is app.New with no module; Run is never called and no listener
// opens. The Provider's Shutdown is registered with t.Cleanup.
//
// opts.Env defaults to an empty environment, so the caller's process env
// cannot change what the test builds; a nil opts.LogOutput discards the
// boot's log lines unless opts.Logger is set.
func MetricsProvider(t testing.TB, opts app.Options) *metrics.Provider {
	t.Helper()
	p, err := NewMetricsProvider(context.Background(), opts)
	if err != nil {
		t.Fatalf("coretest.MetricsProvider: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Shutdown(ctx); err != nil {
			t.Errorf("coretest.MetricsProvider: Provider.Shutdown: %v", err)
		}
	})
	return p
}

// NewMetricsProvider is MetricsProvider without a testing.TB, for a Provider
// that outlives one test: a package's TestMain that seeds process-global
// instruments once (they bind to the first MeterProvider installed as the OTel
// global and never rebind). The caller shuts the Provider down. The defaults
// are MetricsProvider's.
func NewMetricsProvider(ctx context.Context, opts app.Options) (*metrics.Provider, error) {
	if opts.AppID == "" {
		opts.AppID = "coretest"
	}
	if opts.Env == nil {
		opts.Env = []string{}
	}
	if opts.Logger == nil && opts.LogOutput == nil {
		opts.LogOutput = discard{}
	}
	a, err := app.New(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("app.New: %w", err)
	}
	return a.Metrics(), nil
}

// discard is io.Discard without importing io for one value.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
