// file: internal/plugins/maintenance/series_writes_test.go
// version: 1.0.0
// guid: 5d2e8c41-9a7b-4f36-b0e2-6c1f3a8d9e57
// last-edited: 2026-09-12

package maintenance

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.series-normalize renames series, so it declares the series
// write-set; the dispatcher's write-set gate then never runs it alongside
// entities.series-rename or dedup.series-merge.
func TestSeriesNormalizeDef_DeclaresSeriesWriteSet(t *testing.T) {
	def := (&Plugin{}).seriesNormalizeDef()
	if !slices.Contains(def.Writes, sdk.ResSeries) {
		t.Fatalf("maintenance.series-normalize Writes = %v, want it to include %q", def.Writes, sdk.ResSeries)
	}
}
