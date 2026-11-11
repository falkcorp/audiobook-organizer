// file: core/integrations/providerhttp/metrics.go
// version: 1.0.0
// guid: 9ac78000-2f21-4fac-a376-94653cead7fa
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
)

// The core "providerhttp" scope (05-metadata M2.8), labels kind and instance
// (plus class, reason). F7-02 records the three counters below; the
// histograms and the hold, breaker and quota gauges are not built (recorded in
// seams.d/F7-02.md). A label is never a scope name, a URL or a header: a
// Scope may be derived from an API key.
//
//	<ns>_providerhttp_requests_total{kind,instance,class}
//	<ns>_providerhttp_refused_total{kind,instance,reason}
//	<ns>_providerhttp_retries_total{kind,instance}
type instruments struct {
	requests metric.Int64Counter
	refusals metric.Int64Counter
	retried  metric.Int64Counter
}

// instrument creates the instruments on sc; nil sc gives nil, whose methods
// record nothing.
func instrument(sc *metrics.Scope) (*instruments, error) {
	if sc == nil {
		return nil, nil
	}
	m, prefix := sc.Meter(), sc.Prefix()
	var in instruments
	var err error
	if in.requests, err = m.Int64Counter(prefix+".requests",
		metric.WithDescription("Outbound attempts by kind, instance and the class they ended in (05-metadata M2.8).")); err != nil {
		return nil, fmt.Errorf("providerhttp: requests counter: %w", err)
	}
	if in.refusals, err = m.Int64Counter(prefix+".refused",
		metric.WithDescription("Attempts the gate refused before sending, by kind, instance and reason.")); err != nil {
		return nil, fmt.Errorf("providerhttp: refused counter: %w", err)
	}
	if in.retried, err = m.Int64Counter(prefix+".retries",
		metric.WithDescription("Retried attempts by kind and instance.")); err != nil {
		return nil, fmt.Errorf("providerhttp: retries counter: %w", err)
	}
	return &in, nil
}

func (m *instruments) request(ref integrations.Ref, c integrations.Class) {
	if m == nil {
		return
	}
	m.requests.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", string(ref.Kind)), attribute.String("instance", string(ref.Instance)),
		attribute.String("class", c.String())))
}

func (m *instruments) refused(ref integrations.Ref, why Reason) {
	if m == nil {
		return
	}
	m.refusals.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", string(ref.Kind)), attribute.String("instance", string(ref.Instance)),
		attribute.String("reason", string(why))))
}

func (m *instruments) retry(ref integrations.Ref) {
	if m == nil {
		return
	}
	m.retried.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("kind", string(ref.Kind)), attribute.String("instance", string(ref.Instance))))
}
