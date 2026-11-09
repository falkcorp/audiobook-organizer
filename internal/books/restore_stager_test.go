// file: internal/books/restore_stager_test.go
// version: 1.0.0
// guid: 6a90739a-abf6-4e9f-a011-b364985a81cf
// last-edited: 2026-11-09

package books

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestRestoreStagerRefusesAStoreThisBuildCannotOpen (05-backup §5B.3.4
// "VerifyOpen"): books' stager opens the extracted store with this build's
// guard before it stages (database.VerifyStoreOpens), so an archive whose
// store the boot after the swap would refuse is refused now, the slot left
// empty and nothing extracted left behind. The archive here holds a HEAD
// store with core keys and no install marker (the crash matrix's
// foreign-keys fixture), which core storage refuses.
func TestRestoreStagerRefusesAStoreThisBuildCannotOpen(t *testing.T) {
	h := newOfflineABO(t)
	src := filepath.Join(tempDir(t), "joker.pebble")
	headStoreAt(t, src, nil)
	db, err := pebble.Open(src, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("c/rogue"), []byte("x"), pebble.Sync))
	require.NoError(t, db.Close())
	backups := backup.ResolveDir("", h.path)
	info, err := backup.CreateBackup(src, "pebble", backup.BackupConfig{BackupDir: backups, MaxBackups: 5})
	require.NoError(t, err)

	restarts := 0
	_, err = h.liveStager(&restarts).StageRestore(context.Background(), backups, info.Filename, true, "user:alfred")
	require.ErrorIs(t, err, storage.ErrForeignKeys)
	require.False(t, h.slot().Busy(), "a refused restore was staged: %+v", h.slot())
	assert.Empty(t, h.stagingLeft(), "the refused restore left its extraction")
	assert.Zero(t, restarts)
}

// TestRestoreStagerMovesTheBooksSidecar (05-backup §5B.3.4 step 6; 05-storage
// §5S.3.15 "Stage"): the swap sets books' storage-format sidecar aside with
// the store (the kernel's swapper derives it from books' LegacyGuard), so
// books' stager pre-checks that rename too, and a sidecar the swap could not
// move refuses the restore at staging, before anything is extracted, rather
// than at the boot after the restart.
func TestRestoreStagerMovesTheBooksSidecar(t *testing.T) {
	h := newOfflineABO(t)
	sidecar := database.StorageFormatSidecarPath(h.path)
	require.FileExists(t, sidecar, "the booted store has no storage-format sidecar to move")
	info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
	require.NoError(t, err)

	restarts := 0
	st := h.liveStager(&restarts)
	st.probe = itemElsewhere{restorejournal.OSProbe(), sidecar}
	_, err = st.StageRestore(context.Background(), filepath.Dir(info.Path), info.Filename, true, "user:alfred")
	require.ErrorIs(t, err, restorejournal.ErrRenameRefused)
	require.False(t, h.slot().Busy(), "a refused restore was staged: %+v", h.slot())
	assert.Empty(t, h.stagingLeft(), "the refused restore left its extraction")
}

// itemElsewhere reports one item on another device than everything else, as
// a sidecar on another filesystem than its set-aside directory.
type itemElsewhere struct {
	restorejournal.Probe
	item string
}

func (p itemElsewhere) ItemDev(path string) (uint64, error) {
	d, err := p.Probe.ItemDev(path)
	if filepath.Clean(path) == p.item {
		d++
	}
	return d, err
}
