// file: internal/activity/register_backend_test.go
// version: 1.0.1
// guid: 6d2e8b41-37a9-4c5f-9e0d-1b8a4f7c2e93
// last-edited: 2026-10-10

package activity

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// TestActivityStore_BackendSelection pins which store the activitystore Build
// wires for each ActivityBackend value. An unset value must be Pebble-only, so
// an install that loses its conf file cannot silently engage SQLite; the
// explicit "sqlite" opt-in must keep reaching the migration wrapper until P75
// removes it.
func TestActivityStore_BackendSelection(t *testing.T) {
	cases := []struct {
		backend string
		want    any
	}{
		{backend: "", want: &database.PebbleActivityStore{}},
		{backend: "pebble", want: &database.PebbleActivityStore{}},
		{backend: "sqlite", want: &database.MigratingActivityStore{}},
	}
	for _, tc := range cases {
		t.Run("backend="+tc.backend, func(t *testing.T) {
			db, err := pebble.Open("test.pebble", &pebble.Options{FS: vfs.NewMem()})
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			pas := database.NewPebbleActivityStore(db)

			cfg := &config.Config{
				DatabasePath:    t.TempDir(),
				ActivityBackend: tc.backend,
				ActivityDBPath:  filepath.Join(t.TempDir(), "activity.sqlite"),
			}
			c := serviceregistry.NewContainer().
				Include(serviceregistry.KeyActivityStore).
				Override(serviceregistry.KeyConfig, cfg).
				Override("pebble-activitystore", pas)
			require.NoError(t, c.Build(context.Background()))

			got := serviceregistry.Get[any](c, serviceregistry.KeyActivityStore)
			if mig, ok := got.(*database.MigratingActivityStore); ok {
				t.Cleanup(func() { _ = mig.Close() })
			}
			require.IsType(t, tc.want, got, "ActivityBackend=%q built the wrong store", tc.backend)
		})
	}
}

func TestRecognisedActivityBackend(t *testing.T) {
	for _, v := range []string{"", "pebble", "sqlite"} {
		require.True(t, recognisedActivityBackend(v), "%q", v)
	}
	for _, v := range []string{"nuts", "nutsdb", "dual", "pebbel"} {
		require.False(t, recognisedActivityBackend(v), "%q", v)
	}
}
