// file: internal/books/flip_blob_values_test.go
// version: 1.0.0
// guid: 7d3b1e96-52a8-4c07-b9f4-1e60a85c3d27
// last-edited: 2026-11-20

package books

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestFlipBlobOnlyValuesAreEffectiveAndSurviveAPut (06 section 6.10; COR4-1,
// R4-6; converts the loader-era TestR1AppConfigFromBlob): a value HEAD kept only
// in the settings blob is effective after the flipped boot, in the projected
// Config and in the Facade's view (the cutover moved it into core's store), and
// a save that changes one other key leaves it, and every other key, exactly as
// it was, through two saves in a row.
func TestFlipBlobOnlyValuesAreEffectiveAndSurviveAPut(t *testing.T) {
	const rate, jsonMB = 37, 9
	fx := newFlipFixture(t, func(s *database.PebbleStore) {
		blob := fmt.Sprintf(`{"api_rate_limit_per_minute":%d,"json_body_limit_mb":%d}`, rate, jsonMB)
		require.NoError(t, s.SetSetting("config_blob", blob, "json", false))
	}, "")

	fx.runFlipped(t, func(*composed) {
		snap := config.Snapshot()
		assert.Equal(t, rate, snap.APIRateLimitPerMinute, "a blob-only value is not effective after the flip")
		assert.Equal(t, jsonMB, snap.JSONBodyLimitMB, "a blob-only value is not effective after the flip")

		f := config.Current()
		require.NotNil(t, f)
		view := func() map[string]string {
			out := map[string]string{}
			for _, k := range f.View().Keys {
				out[k.Name] = string(k.Value)
			}
			return out
		}
		assert.Equal(t, fmt.Sprint(rate), view()["api_rate_limit_per_minute"])
		assert.Equal(t, fmt.Sprint(jsonMB), view()["json_body_limit_mb"])

		_, err, _ := putUpdate(t, f, map[string]any{"concurrent_scans": 3})
		require.NoError(t, err)
		first := view()
		assert.Equal(t, "3", first["concurrent_scans"])
		assert.Equal(t, fmt.Sprint(rate), first["api_rate_limit_per_minute"], "a one-key save changed a blob-only value")
		assert.Equal(t, fmt.Sprint(jsonMB), first["json_body_limit_mb"], "a one-key save changed a blob-only value")

		// A second one-key save leaves every other key's value as the first left it.
		_, err, _ = putUpdate(t, f, map[string]any{"concurrent_scans": 4})
		require.NoError(t, err)
		second := view()
		require.Greater(t, len(first), 50, "the view after a save is not the whole config")
		for k, before := range first {
			if k == "concurrent_scans" {
				continue
			}
			assert.Equal(t, before, second[k], "a one-key save changed %s", k)
		}
		assert.Equal(t, "4", second["concurrent_scans"])
	})
}
