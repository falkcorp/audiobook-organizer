// file: internal/repairs/guards_gauge_test.go
// version: 1.1.0
// guid: 3e7a91c4-5b28-4d06-a1f3-9c84d2e07b15
// last-edited: 2026-10-22

package repairs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// TestResolverAbandonedIsExportedAsAGauge: the process-wide abandoned counter that gates the
// resolver is the one exported as
// audiobook_organizer_ops_abandoned_calls{source="repairs_resolver"}, so an
// alert sees the same number the cap does.
func TestResolverAbandonedIsExportedAsAGauge(t *testing.T) {
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
					if src, _ := dp.Attributes.Value(telemetry.Source); src.AsString() == "repairs_resolver" {
						return dp.Value, true
					}
				}
			}
		}
		return 0, false
	}
	base, ok := point()
	require.True(t, ok, "the series exists before anything is abandoned")
	resolverAbandoned.Add(5)
	t.Cleanup(func() { resolverAbandoned.Add(-5) })
	got, _ := point()
	require.Equal(t, base+5, got, "the gauge follows the counter the cap reads")
}

// TestAbandonedGaugeIsRegisteredLazily: with the process-wide provider that TestMain bound, the gauge is
// on the meter once the package's start-up hook has run (NewRegistry()), reading the
// counter the cap reads, and under the "operations" scope with the version
// telemetry.SetVersion supplied. Package init must not have registered it: that
// would have built the operations meter before SetVersion and stamped it
// "unknown", which this test fails on.
func TestAbandonedGaugeIsRegisteredLazily(t *testing.T) {
	NewRegistry()

	var rm metricdata.ResourceMetrics
	point := func() (int64, string, bool) {
		require.NoError(t, gaugeReader.Collect(context.Background(), &rm))
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != "audiobook_organizer.ops.abandoned_calls" {
					continue
				}
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					if s, _ := dp.Attributes.Value(telemetry.Source); s.AsString() == "repairs_resolver" {
						return dp.Value, sm.Scope.Version, true
					}
				}
			}
		}
		return 0, "", false
	}
	base, version, ok := point()
	require.True(t, ok, "the repairs_resolver series is on the global meter before anything is abandoned")
	require.Equal(t, telemetry.Version(), version, "the operations meter was built after telemetry.SetVersion")
	require.NotEqual(t, "unknown", version)

	resolverAbandoned.Add(3)
	t.Cleanup(func() { resolverAbandoned.Add(-3) })
	got, _, _ := point()
	require.Equal(t, base+3, got, "the series follows the counter the cap reads")
}
