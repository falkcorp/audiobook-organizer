// file: core/internal/modulehttp/metrics.go
// version: 1.0.0
// guid: 32893b2c-9f60-4a20-a0e3-a65343fa99f6
// last-edited: 2026-11-10

package modulehttp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// The outcome label of <ns>_ui_manifest_requests_total (07 §7.16). 07 names
// the label and no values, so the set is this package's and closed: every
// answer GET /modules can give is one of these four, and nothing a client
// sends reaches a label.
const (
	outcomeOK              = "ok"              // 200 without disabled_modules (the caller lacks core:admin)
	outcomeAdmin           = "admin"           // 200 with disabled_modules (the caller holds core:admin)
	outcomeUnauthenticated = "unauthenticated" // 401 from the handler's own check (stage 13 normally answers first)
	outcomeError           = "error"           // 503: the view could not produce a snapshot
)

// outcomes is the closed set, for seeding. A function, not a package-level
// slice (03 R5).
func outcomes() []string {
	return []string{outcomeOK, outcomeAdmin, outcomeUnauthenticated, outcomeError}
}

// uiMetrics is the one instrument this package records into: the manifest
// counter of the core "ui" scope (07 §7.16). The scope is made once per
// Provider (metrics.ErrScopeTaken), so the kernel creates it and hands the same
// *metrics.Scope to this package, to uihttp and, later, to ui.Handler. A nil
// *uiMetrics records nothing.
type uiMetrics struct {
	requests metric.Int64Counter
}

// newMetrics creates the counter on sc and exports every outcome from zero, so
// a dashboard sees the family before the first request. A nil scope is a nil
// result: the package then records nothing.
func newMetrics(sc *metrics.Scope) (*uiMetrics, error) {
	if sc == nil {
		return nil, nil
	}
	c, err := sc.Meter().Int64Counter(sc.Prefix()+".manifest_requests",
		metric.WithDescription("GET /api/core/v1/modules answers, by outcome (07 §7.16)."))
	if err != nil {
		return nil, fmt.Errorf("modulehttp: manifest requests counter: %w", err)
	}
	m := &uiMetrics{requests: c}
	for _, o := range outcomes() {
		c.Add(context.Background(), 0, metric.WithAttributes(attribute.String("outcome", o)))
	}
	return m, nil
}

// record counts one answer. outcome is always one of this file's constants.
func (m *uiMetrics) record(ctx context.Context, outcome string) {
	if m == nil {
		return
	}
	m.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
