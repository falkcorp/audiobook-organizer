// file: core/updater/metrics.go
// version: 1.0.0
// guid: 4e9a1c37-6d02-48b5-b3f1-0a7d5c28e964
// last-edited: 2026-11-18

package updater

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// ScopeAttributes are the label keys the updater scope declares: pass them to
// metrics.Provider.Core("updater", ScopeAttributes()...). A function, not a
// variable: core forbids package-level mutable state (03 R5).
func ScopeAttributes() []attribute.Key { return []attribute.Key{"outcome"} }

// Metrics are 05-updater §5U.9's series. A nil *Metrics records nothing, so an
// Updater built without a scope runs unchanged (HEAD's updater emits no series).
type Metrics struct {
	checks, apply, rollbacks, download metric.Int64Counter
	available                          metric.Int64Gauge
}

// NewMetrics registers the families on sc (the core "updater" scope).
func NewMetrics(sc *metrics.Scope) (*Metrics, error) {
	if sc == nil {
		return nil, fmt.Errorf("updater: NewMetrics needs the core updater scope")
	}
	m, p := sc.Meter(), sc.Prefix()
	var (
		out Metrics
		err error
	)
	counter := func(dst *metric.Int64Counter, name, unit, desc string) {
		if err == nil {
			*dst, err = m.Int64Counter(p+"."+name, metric.WithUnit(unit), metric.WithDescription(desc))
		}
	}
	counter(&out.checks, "checks", "", "Update checks, by outcome (ok, failed, no_asset).")
	counter(&out.apply, "apply", "", "Update applies, by outcome (applied, download_failed, selftest_failed, swap_failed, refused, binary_not_writable).")
	counter(&out.rollbacks, "rollbacks", "", "Manual rollbacks, by outcome (done, refused).")
	counter(&out.download, "download", "By", "Release bytes downloaded by an apply or a fetch.")
	if err == nil {
		out.available, err = m.Int64Gauge(p+".update_available", metric.WithDescription("1 while a newer release than the running one is offered."))
	}
	if err != nil {
		return nil, fmt.Errorf("updater: metrics: %w", err)
	}
	return &out, nil
}

// Check counts one check by outcome ("ok", "failed", "no_asset").
func (m *Metrics) Check(ctx context.Context, outcome string) {
	if m != nil {
		m.checks.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// Available sets the update_available gauge.
func (m *Metrics) Available(ctx context.Context, yes bool) {
	if m == nil {
		return
	}
	var v int64
	if yes {
		v = 1
	}
	m.available.Record(ctx, v)
}

// Apply counts one apply by outcome.
func (m *Metrics) Apply(ctx context.Context, outcome string) {
	if m != nil {
		m.apply.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// Rollback counts one rollback by outcome ("done", "refused").
func (m *Metrics) Rollback(ctx context.Context, outcome string) {
	if m != nil {
		m.rollbacks.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// Downloaded adds n downloaded bytes.
func (m *Metrics) Downloaded(ctx context.Context, n int64) {
	if m != nil && n > 0 {
		m.download.Add(ctx, n)
	}
}
