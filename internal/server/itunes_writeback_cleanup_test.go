// file: internal/server/itunes_writeback_cleanup_test.go
// version: 1.0.0
// guid: 7c7579b1-af5d-495e-93cf-b375de7d2837
// last-edited: 2026-10-07

package server

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// The startup cleanup deletes every key the removed iTunes write-back left
// (queue, held removes, status, outbox prefs), touches nothing else, and is a
// no-op on a second run.
func TestPurgeLegacyITunesWriteBackKeys(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var legacy []string
	// More than one page under the queue prefix.
	for i := 0; i < legacyITunesWriteBackPageSize+7; i++ {
		legacy = append(legacy, fmt.Sprintf("itunes_writeback:q:book:%04d", i))
	}
	legacy = append(legacy,
		"itunes_writeback:q:remove:abcdef0123456789",
		"itunes_writeback:held:remove:0123456789abcdef",
		"itunes_writeback:status",
	)
	for _, k := range legacy {
		require.NoError(t, store.SetRaw(k, []byte("x")))
	}
	require.NoError(t, store.SetUserPreferenceForUser("_system", "outbox:writeback:book-1", "2026-10-01T00:00:00Z"))
	// Neighbours that must survive.
	require.NoError(t, store.SetRaw("itunes_writebackX", []byte("keep")))
	require.NoError(t, store.SetUserPreferenceForUser("_system", "pipeline_checkpoint:b1:tags", "keep"))

	got := purgeLegacyITunesWriteBackKeys(store)
	require.Equal(t, len(legacy)+1, got)

	for _, prefix := range legacyITunesWriteBackPrefixes {
		n, err := store.CountPrefix(prefix)
		require.NoError(t, err)
		require.Zero(t, n, "prefix %s", prefix)
	}
	v, err := store.GetRaw("itunes_writebackX")
	require.NoError(t, err)
	require.Equal(t, []byte("keep"), v)
	pref, err := store.GetUserPreferenceForUser("_system", "pipeline_checkpoint:b1:tags")
	require.NoError(t, err)
	require.NotNil(t, pref)

	require.Zero(t, purgeLegacyITunesWriteBackKeys(store), "second run finds nothing")
}
