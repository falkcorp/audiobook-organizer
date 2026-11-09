// file: internal/books/claims_test.go
// version: 1.0.1
// guid: 7b2e4c96-1f3a-4d85-9c60-5e8a2d7f1b34
// last-edited: 2026-11-09

package books

import (
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// TestBooksWritesEveryFamily (05-storage §5S.10, §5S.3.4; COR5-1): on ABO's
// boot, under the strict claims in force, books writes a key of every
// registered family through its store (the family's own key for a bare-key
// family, a key under the prefix otherwise) and deletes it, saves the
// settings (config_blob and the secret rows, Serialized claims) and writes a
// scan's import-root statistics, with no write refused: no family books
// writes is outside its claims. A family left unclaimed would fail here with
// storage.ErrOutsideKeyspace, and the fix is to claim it, never to widen a
// claim over a foreign range.
func TestBooksWritesEveryFamily(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	db := c.store.DB()

	exactKey := map[string]bool{}
	for _, cl := range database.BooksClaims() {
		if cl.Key != "" {
			exactKey[cl.Key] = true
		}
	}
	written := 0
	for _, f := range database.KeyFamilies() {
		if f.Prefix == "c/" || f.Prefix == "m/" {
			continue // core's reserved roots, never books'
		}
		key := f.Prefix + "batman-probe"
		if exactKey[f.Prefix] {
			key = f.Prefix
		}
		prior, closer, err := db.Get([]byte(key))
		var keep []byte
		if err == nil {
			keep = append([]byte(nil), prior...)
			require.NoError(t, closer.Close())
		}
		if !assert.NoError(t, db.Set([]byte(key), []byte("{}"), pebble.Sync), "family %q: write of %q refused", f.Prefix, key) {
			continue
		}
		written++
		if keep != nil {
			require.NoError(t, db.Set([]byte(key), keep, pebble.Sync))
		} else {
			require.NoError(t, db.Delete([]byte(key), pebble.Sync), "family %q: delete of %q refused", f.Prefix, key)
		}
	}
	assert.Greater(t, written, 200, "the family registry is far smaller than 05-storage §5S.9.1's table: the test proves little")

	// A settings save: the whole config struct as config_blob plus the
	// secret rows (row 7, Serialized).
	config.Mutate(func(cfg *config.Config) { cfg.OpenAIAPIKey = "sk-test-alfred" })
	require.NoError(t, config.SaveConfigToDatabase(c.store), "a settings save was refused")

	// A scan's import-root statistics write.
	ip, err := c.store.CreateImportPath(t.TempDir(), "Batcave")
	require.NoError(t, err)
	now := time.Now().UTC()
	ip.BookCount, ip.LastScan = 42, &now
	require.NoError(t, c.store.UpdateImportPath(ip.ID, ip), "a scan's import-root statistics write was refused")
	got, err := c.store.GetImportPathByID(ip.ID)
	require.NoError(t, err)
	assert.Equal(t, 42, got.BookCount)
}

// pebbleMetricNames is every pebble_* family internal/metrics exports for a
// store (05-metrics), a snapshot of the names: a rename fails here.
var pebbleMetricNames = []string{
	"block_cache_bytes", "block_cache_blocks", "read_amplification", "l0_files", "l0_sublevels",
	"compaction_debt_bytes", "compactions_in_progress", "compaction_in_progress_bytes", "memtable_bytes",
	"memtables", "wal_bytes", "wal_physical_bytes", "wal_files", "disk_usage_bytes",
	"block_cache_hits_total", "block_cache_misses_total", "filter_hits_total", "filter_misses_total", "compactions_total",
	"level_bytes", "level_files", "level_write_amplification",
	"level_bytes_in_total", "level_bytes_flushed_total", "level_bytes_compacted_total",
}

// TestPebbleMetricNamesKeptOnABOsBoot (R1-23 STOP condition: a Pebble metric
// family changes name): on the store core opened, the main store's pebble_*
// families keep their names and their store="main" series, sampled from
// core's engine through the admin surface Attach hands books.
func TestPebbleMetricNamesKeptOnABOsBoot(t *testing.T) {
	bootComposed(t, false, nil, nil)
	reg := prometheus.NewRegistry()
	require.NoError(t, metrics.Register(reg))
	fams, err := reg.Gather()
	require.NoError(t, err)
	mainSeries := map[string]int{}
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "store" && l.GetValue() == metrics.PebbleStoreMain {
					mainSeries[f.GetName()]++
				}
			}
		}
	}
	for _, n := range pebbleMetricNames {
		assert.Positive(t, mainSeries["audiobook_organizer_pebble_"+n], "no store=main series of audiobook_organizer_pebble_%s", n)
	}
}
