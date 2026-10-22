// file: internal/plugins/maintenance/retired_versions_gauge_test.go
// version: 1.0.0
// guid: 8b14c6e2-7d39-4a51-b0e8-2f65a9d13c47
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// TestRetiredStatsAbandonedIsExportedAsAGauge: the process-wide abandoned counter that gates the
// retired-versions file stat is the one exported as
// audiobook_organizer_ops_abandoned_calls{source="retired_versions_stat"}, so an
// alert sees the same number the cap does.
func TestRetiredStatsAbandonedIsExportedAsAGauge(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	registerAbandonedGauge(opsmetrics.New(mp))

	point := func() (int64, bool) {
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != "audiobook_organizer.ops.abandoned_calls" {
					continue
				}
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					if src, _ := dp.Attributes.Value(telemetry.Source); src.AsString() == "retired_versions_stat" {
						return dp.Value, true
					}
				}
			}
		}
		return 0, false
	}
	base, ok := point()
	require.True(t, ok, "the series exists before anything is abandoned")
	retiredStatsAbandoned.Add(5)
	t.Cleanup(func() { retiredStatsAbandoned.Add(-5) })
	got, _ := point()
	require.Equal(t, base+5, got, "the gauge follows the counter the cap reads")
}
