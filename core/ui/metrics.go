// file: core/ui/metrics.go
// version: 0.1.0
// guid: 0c6b2e94-7f13-4a58-b2d7-9e4a1c5f3d86
// last-edited: 2026-11-20

package ui

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// uiMetrics holds the two families of 07 §7.16 that Handler owns:
// <ns>_ui_build_info (gauge 1, label build_id) and <ns>_ui_responses_total
// (counter, labels kind and status). The scope is the core "ui" scope the
// kernel creates once and shares with internal/uihttp and
// internal/modulehttp. A nil *uiMetrics records nothing.
type uiMetrics struct {
	responses metric.Int64Counter
}

// statusLabels is the closed set of the status label: every status the
// handler or a page middleware is expected to write. Anything else is
// counted as "other", so no response can add a series.
func statusLabels() []string {
	return []string{"200", "206", "302", "304", "404", "405", "416"}
}

// seededPairs are the kind/status pairs exported from zero: the ones the
// handler itself can write.
func seededPairs() [][2]string {
	return [][2]string{
		{kindAsset, "200"}, {kindAsset, "206"}, {kindAsset, "304"}, {kindAsset, "416"},
		{kindAsset404, "404"}, {kindIndex, "200"}, {kindNonSPA404, "404"},
		{kindMethod404, "405"}, {kindPlaceholder, "200"},
	}
}

// statusLabel is code's label, "other" outside the closed set.
func statusLabel(code int) string {
	s := strconv.Itoa(code)
	for _, l := range statusLabels() {
		if l == s {
			return s
		}
	}
	return "other"
}

// newUIMetrics registers both families on sc. A nil scope, or an instrument
// the scope refuses, records nothing: metrics never stop the UI from serving.
func newUIMetrics(sc *metrics.Scope, b Bundle) *uiMetrics {
	if sc == nil {
		return nil
	}
	m := sc.Meter()
	info, err := m.Int64Gauge(sc.Prefix()+".build_info",
		metric.WithDescription("The UI build ID of the served bundle, always 1; \"dev\" without an embedded bundle (07 §7.16)."))
	if err != nil {
		return nil
	}
	resp, err := m.Int64Counter(sc.Prefix()+".responses",
		metric.WithDescription("Responses of the SPA handler, by kind and status (07 §7.16)."))
	if err != nil {
		return nil
	}
	id := b.BuildID()
	if id == "" {
		id = "dev"
	}
	ctx := context.Background()
	info.Record(ctx, 1, metric.WithAttributes(attribute.String("build_id", id)))
	for _, p := range seededPairs() {
		resp.Add(ctx, 0, responseAttrs(p[0], p[1]))
	}
	return &uiMetrics{responses: resp}
}

func responseAttrs(kind, status string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("kind", kind), attribute.String("status", status))
}

// record counts one response.
func (m *uiMetrics) record(ctx context.Context, kind string, code int) {
	if m != nil {
		m.responses.Add(ctx, 1, responseAttrs(kind, statusLabel(code)))
	}
}
