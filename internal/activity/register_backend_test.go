// file: internal/activity/register_backend_test.go
// version: 2.0.0
// guid: 6d2e8b41-37a9-4c5f-9e0d-1b8a4f7c2e93
// last-edited: 2026-10-19

package activity

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// buildActivityStore runs the real activitystore Build for a config naming
// backend, against an in-memory Pebble activity store.
func buildActivityStore(t *testing.T, backend string) (any, error) {
	t.Helper()
	db, err := pebble.Open("test.pebble", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	pas := database.NewPebbleActivityStore(database.NewGatedDB(db))

	cfg := &config.Config{
		DatabasePath:    t.TempDir(),
		ActivityBackend: backend,
	}
	c := serviceregistry.NewContainer().
		Include(serviceregistry.KeyActivityStore).
		Override(serviceregistry.KeyConfig, cfg).
		Override("pebble-activitystore", pas)
	if err := c.Build(context.Background()); err != nil {
		return nil, err
	}
	return serviceregistry.Get[any](c, serviceregistry.KeyActivityStore), nil
}

// TestActivityStore_PebbleIsTheOnlyBackend: an unset value (a config that
// predates the setting) and "pebble" in any case or padding build the Pebble
// store, which is the only activity backend since 01-P75.
func TestActivityStore_PebbleIsTheOnlyBackend(t *testing.T) {
	for _, backend := range []string{"", "pebble", " PEBBLE "} {
		t.Run("backend="+backend, func(t *testing.T) {
			got, err := buildActivityStore(t, backend)
			require.NoError(t, err)
			require.IsType(t, &database.PebbleActivityStore{}, got, "ActivityBackend=%q built the wrong store", backend)
		})
	}
}

// TestActivityStore_SQLiteFailsLoudly: a conf that still says
// activity_backend: sqlite must stop the build with a message naming the
// removed backend and the fix. Serving it on Pebble would let the operator
// believe SQLite is still in use.
func TestActivityStore_SQLiteFailsLoudly(t *testing.T) {
	for _, backend := range []string{"sqlite", "SQLite"} {
		t.Run("backend="+backend, func(t *testing.T) {
			got, err := buildActivityStore(t, backend)
			require.Error(t, err)
			require.Nil(t, got)
			require.Contains(t, err.Error(), "activity_backend: sqlite is no longer supported")
			require.Contains(t, err.Error(), "set it to pebble")
		})
	}
}

// TestActivityStore_UnknownBackendFailsLoudly: a typo or a long-retired name
// (NutsDB) is refused too, not silently served by Pebble.
func TestActivityStore_UnknownBackendFailsLoudly(t *testing.T) {
	for _, backend := range []string{"pebbel", "nutsdb", "dual"} {
		t.Run("backend="+backend, func(t *testing.T) {
			_, err := buildActivityStore(t, backend)
			require.Error(t, err)
			require.Contains(t, err.Error(), "is not a supported value")
		})
	}
}
