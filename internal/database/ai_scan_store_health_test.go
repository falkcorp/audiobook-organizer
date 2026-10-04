// file: internal/database/ai_scan_store_health_test.go
// version: 1.0.0
// guid: 32eddfe1-cb1f-4009-9b7c-a3e3c626c050
// last-edited: 2026-10-04

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

func TestAIScanHealthStats_SharedReportsPrefixEstimate(t *testing.T) {
	p := newCensusRawStore(t)
	b := p.db.NewBatch()
	pad := make([]byte, 200)
	for i := 0; i < 10000; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book:%06d", i)), pad, nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))

	s, err := NewAIScanStoreFromDB(p.db)
	require.NoError(t, err)
	require.NoError(t, p.db.Flush())

	stats, err := s.HealthStats()
	require.NoError(t, err)
	require.Equal(t, "aiscan_prefix_estimate", stats.SizeSource)
	require.Less(t, stats.SizeBytes, p.db.Metrics().DiskSpaceUsage())
}

func TestAIScanHealthStats_OwnedReportsStoreUsage(t *testing.T) {
	s, err := NewAIScanStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	stats, err := s.HealthStats()
	require.NoError(t, err)
	require.Equal(t, "store_disk_usage", stats.SizeSource)
}
