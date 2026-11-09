// file: internal/books/store/compaction_stats_test.go
// version: 1.0.2
// guid: 747d69ee-99f8-4907-9b97-47bdd0b4b7ac
// last-edited: 2026-11-09

package store

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CollectCompactionStats reads real field names from the pinned Pebble
// version; this pins that the mapping compiles and produces sane numbers on a
// real DB. It moved here from internal/compactprogress's tests when the
// sampler moved into core's storage, which has its own copy of the mapping.
func TestCollectCompactionStats_RealPebble(t *testing.T) {
	// Automatic compactions are off so the only compaction in this test is
	// the explicit db.Compact below. With them on, the flushes can schedule a
	// background compaction that finishes before the `before` snapshot (or
	// leaves db.Compact nothing to merge), making the written-bytes delta zero
	// and the test flaky under load. Same fix as core storage's
	// TestCompactionStats_RealPebble.
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem(), DisableAutomaticCompactions: true})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for i := range 2000 {
		require.NoError(t, db.Set([]byte{byte(i >> 8), byte(i)}, make([]byte, 512), pebble.NoSync))
	}
	// Two overlapping L0 tables, so the compaction must merge (rewrite)
	// rather than move a lone file down by metadata alone.
	require.NoError(t, db.Flush())
	for i := range 1000 {
		require.NoError(t, db.Delete([]byte{byte(i >> 8), byte(i)}, pebble.NoSync))
	}
	require.NoError(t, db.Flush())
	gated := NewGatedDB(db)
	before := CollectCompactionStats(gated)
	require.Zero(t, before.Count, "no compaction may run before the snapshot")
	require.NoError(t, db.Compact(context.Background(), nil, []byte{0xff}, false))
	after := CollectCompactionStats(gated)
	assert.Greater(t, after.Count, before.Count, "the explicit compaction must be counted")
	assert.Greater(t, after.LiveTableSize, int64(0))
	assert.Greater(t, after.WrittenBytes(), before.WrittenBytes(), "a compaction must show written bytes")
	assert.Greater(t, after.BottomLevelSize, int64(0), "a full compaction lands data in the bottom level")
	assert.Equal(t, CompactionStats{Absent: true}, CollectCompactionStats(nil))
}
