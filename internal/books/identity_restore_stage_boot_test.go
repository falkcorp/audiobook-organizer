// file: internal/books/identity_restore_stage_boot_test.go
// version: 0.1.0
// guid: 729ff74e-2073-4be3-b71a-e5a4401e549c
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestIdentityBoot_ABooksStagedRestoreServesTheRestoredStore (F4-08 B10g): a
// restore staged by books' own stager (POST /backup/restore's), from a
// legacy-abo/0 archive a HEAD build wrote before the flip, is read by the
// next boot under core identity. P4d derives the restore's source through
// core's ReadStagedManifest, which refused an intent that names no staging
// directory or manifest hash ("the restore source cannot be read"), so the
// boot refused to serve. The stager now writes core's legacy manifest beside
// the staged store and the intent names it.
//
// The cell is a boot, not a parse: the archive holds a book the live store
// no longer has, and the boot after the stage must serve it.
func TestIdentityBoot_ABooksStagedRestoreServesTheRestoredStore(t *testing.T) {
	h := newOfflineABO(t)
	openStore := func() *database.PebbleStore {
		store, err := database.OpenStandalone(h.path, StandaloneOptions(database.StandaloneOptions{}))
		require.NoError(t, err)
		return store
	}
	store := openStore()
	_, err := store.CreateBook(&database.Book{ID: "joker", Title: "Hush", FilePath: filepath.Join(filepath.Dir(h.path), "library", "Hush.m4b")})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
	require.NoError(t, err)
	store = openStore()
	require.NoError(t, store.DeleteBook("joker"))
	require.NoError(t, store.Close())

	restarts := 0
	_, err = h.liveStager(&restarts).StageRestore(context.Background(), filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
	require.NoError(t, err)
	require.True(t, h.slot().Busy(), "nothing staged")
	// What the boot reads: the staged intent names the staging directory and
	// the manifest beside the store, never inside it.
	in := stagedIntent(t, h.state)
	require.NotEmpty(t, in.Staging)
	require.NotEmpty(t, in.ManifestSHA256)
	require.FileExists(t, filepath.Join(in.Staging, "legacy-manifest.json"))
	require.NoFileExists(t, filepath.Join(in.Staging, "db", "legacy-manifest.json"))

	c := bootComposedWith(t, false, h.state, func(cfg *config.Config) {
		h.edit(cfg)
		cfg.Embedding.Dimensions = 4
	}, nil, h.disk)
	require.False(t, h.slot().Busy(), "the boot did not apply the staged restore")
	got, err := c.store.GetBookByID("joker")
	require.NoError(t, err, "the restored store does not serve the archive's book")
	require.NotNil(t, got)
	require.Equal(t, "Hush", got.Title)
}

// stagedIntent is the intent the swap slot holds.
func stagedIntent(t *testing.T, state string) restorejournal.Intent {
	t.Helper()
	raw, err := os.ReadFile(restorejournal.PendingPath(state))
	require.NoError(t, err)
	var intent restorejournal.Intent
	require.NoError(t, json.Unmarshal(raw, &intent))
	return intent
}
