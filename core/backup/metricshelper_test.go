// file: core/backup/metricshelper_test.go
// version: 1.0.0
// guid: c4a8e2d6-9f13-4b70-a5d1-3e7b0f6c8a29
// last-edited: 2026-11-12

package backup_test

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
)

// seriesValue is the value of family's series whose labels include want.
func seriesValue(t *testing.T, p *metrics.Provider, family string, want map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			return sampleOf(m), true
		}
	}
	return 0, false
}

func sampleOf(m *dto.Metric) float64 {
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	return m.GetGauge().GetValue()
}
