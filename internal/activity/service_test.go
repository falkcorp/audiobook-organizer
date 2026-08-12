// file: internal/activity/service_test.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-08-11

// NOTE(fable5 T022): Ported from SQLite ActivityStore to NutsActivityStore.
// Tier names updated to match NutsActivityStore's supported tiers
// (change/debug/audit/info/batch/system/digest).

package activity

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_RecordAndQuery(t *testing.T) {
	dir := t.TempDir()

	store, err := database.NewNutsActivityStore(dir)
	require.NoError(t, err)
	defer store.Close()

	svc := NewService(store)
	require.NotNil(t, svc)
	assert.Equal(t, store, svc.Store())

	// Record two entries with different tiers.
	err = svc.Record(database.ActivityEntry{
		Tier:    "change",
		Type:    "tag_write",
		Level:   "info",
		Source:  "test",
		Summary: "wrote tags for book A",
	})
	require.NoError(t, err)

	err = svc.Record(database.ActivityEntry{
		Tier:    "debug",
		Type:    "isbn_lookup",
		Level:   "info",
		Source:  "test",
		Summary: "ISBN lookup for book B",
	})
	require.NoError(t, err)

	// Query all entries.
	entries, total, err := svc.Query(context.Background(), database.ActivityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, entries, 2)

	// Query by tier=change — should return only 1.
	entries, total, err = svc.Query(context.Background(), database.ActivityFilter{Tier: "change", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, entries, 1)
	assert.Equal(t, "tag_write", entries[0].Type)

	// Summarize change entries older than 1 hour in the future (captures all).
	future := time.Now().UTC().Add(time.Hour)
	deleted, err := svc.Summarize(context.Background(), future, "change")
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	// Prune debug entries.
	deleted, err = svc.Prune(future, "debug")
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
}
