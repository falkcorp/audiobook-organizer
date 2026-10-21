// file: internal/decisions/backup_test.go
// version: 1.0.0
// guid: d6a41f93-58e2-4b07-9c1d-3e8f720b5a64
// last-edited: 2026-10-21

package decisions_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
)

// A decision made today must come back from tomorrow's backup: the backup
// codec is not prefix-filtered, and this pins that with a real on-disk store,
// the production checkpoint path and a verified restore.
func TestBackupRoundTrip_DecisionsSurvive(t *testing.T) {
	parent := t.TempDir()
	dbPath := filepath.Join(parent, "audiobooks.pebble")
	kv, err := database.NewPebbleStore(dbPath)
	require.NoError(t, err)

	before := seedDecisions(t, kv)
	require.NotEmpty(t, before)
	s := decisions.New(kv)
	rejected := 0
	require.NoError(t, s.Iterate(t.Context(), decisions.Filter{}, func(r decisions.Record) error {
		if r.Decision == decisions.DecisionReject {
			rejected++
		}
		return nil
	}))
	require.Equal(t, 3, rejected)

	backupDir := filepath.Join(t.TempDir(), "backups")
	info, err := backup.CreateBackupWithCheckpoint(kv, dbPath, "pebble", backup.BackupConfig{BackupDir: backupDir, MaxBackups: 5})
	require.NoError(t, err)
	require.NoError(t, kv.Close())

	restoreRoot := t.TempDir()
	require.NoError(t, backup.RestoreBackupIn(backupDir, info.Filename, restoreRoot, true))

	restored, err := database.NewPebbleStore(filepath.Join(restoreRoot, "audiobooks.pebble"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })

	requireIntact(t, restored, before, "backup + restore")

	// And the restored store still enforces the vetoes it was backed up with.
	rs := decisions.New(restored)
	got, err := rs.PairVetoed("book", "b1", "b3", decisions.FamilySameWork)
	require.NoError(t, err)
	require.True(t, got)
	got, err = rs.PairVetoed("book", "big000", "big054", decisions.FamilySameWork)
	require.NoError(t, err)
	require.True(t, got, "membership rows of a large rejected set survive too")
}
