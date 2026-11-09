// file: internal/server/handlers/system/restore_round8_test.go
// version: 1.0.0
// guid: 46c7f994-13e9-42cc-afa7-78f58699cb26
// last-edited: 2026-11-09

package system_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The R1-15 round-8 verification review's restore findings, through the
// handler: B1-r8 (a symlink into a subdirectory of the snapshot directory)
// and N1 (the status and body of a refusal inside the extraction).

// liveRestore points the configuration at a database directory under a new
// temporary root, with its backup directory, and returns the root and the
// database path.
func liveRestore(t *testing.T) (root, dbPath string) {
	t.Helper()
	root = t.TempDir()
	dbPath = filepath.Join(root, "audiobooks.pebble")
	require.NoError(t, os.MkdirAll(dbPath, 0o755))
	prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
	config.AppConfig.DatabasePath = dbPath
	config.AppConfig.BackupDir = filepath.Join(root, "backups")
	t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
	require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
	return root, dbPath
}

// postRestore runs a restore of the backup named name into target (the
// default when empty) and returns the response's status, its body and how
// many times the handler marked the live HNSW store diverged.
func postRestore(t *testing.T, name, target string) (status int, body string, marks int) {
	t.Helper()
	h, _ := newTestHandler(t)
	h.WithANNSnapshotDivergence(func(string) { marks++ })
	req := map[string]any{"backup_filename": name}
	if target != "" {
		req["target_path"] = target
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	w := run(http.MethodPost, "/backup/restore", "/backup/restore", raw, func(r *gin.Engine) {
		r.POST("/backup/restore", h.RestoreBackup)
	})
	return w.Code, w.Body.String(), marks
}

// extractRefusalNote is what a refusal inside the extraction says about the
// state it leaves.
var extractRefusalNote = []string{"may have been written", "marked diverged"}

// TestRestoreBackup_RefusesASymlinkIntoTheSnapshotSubdirectory (B1-r8): a
// symlink in the live database directory to a directory under the live
// snapshot directory let an entry through it, into the snapshot directory:
// the restore returned 200 with a vouching marker at hnsw/robin. The
// extraction now compares the entry's physical path, refuses it before it
// writes it, and says so with the prepare step's 400.
func TestRestoreBackup_RefusesASymlinkIntoTheSnapshotSubdirectory(t *testing.T) {
	_, dbPath := liveRestore(t)
	live := database.HNSWSnapshotDir(dbPath)
	require.NoError(t, os.MkdirAll(filepath.Join(live, "robin"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("..", "hnsw", "robin"), filepath.Join(dbPath, "nightwing")))
	writeTar(t, filepath.Join(config.AppConfig.BackupDir, "hush.tar"), []tarEntry{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"audiobooks.pebble/nightwing/hnsw.clean-stop", testCleanStopMarker},
	})

	status, body, marks := postRestore(t, "hush.tar", "")
	require.Equal(t, http.StatusBadRequest, status, body)
	for _, want := range append([]string{"HNSW snapshot directory", "nightwing/hnsw.clean-stop"}, extractRefusalNote...) {
		assert.Contains(t, body, want)
	}
	assert.Equal(t, 1, marks, "the restore was marked before the extraction")
	_, err := os.Lstat(filepath.Join(live, "robin", "hnsw.clean-stop"))
	assert.True(t, os.IsNotExist(err), "the entry was written inside the snapshot directory (lstat: %v)", err)
	assert.False(t, markerPresent(t, live))
}

// TestRestoreBackup_AnExtractionRefusalIsABadRequest (N1): a refusal inside
// the extraction was a bare 500, while the same refusal in the prepare step
// is a 400 with its reason. It is now the 400 with the reason, and the body
// says what the prepare step's refusal never has to: entries before the
// refused one may have been written, and the live store was marked
// diverged, so its snapshot is reconciled rather than trusted. The case: an
// entry that reaches the snapshot directory through a symlink (the
// reviewer's).
func TestRestoreBackup_AnExtractionRefusalIsABadRequest(t *testing.T) {
	for name, tc := range map[string]struct {
		setup     func(t *testing.T, dbPath, archive string)
		wantError string
	}{
		"through a symlink to the snapshot directory": {
			setup: func(t *testing.T, dbPath, archive string) {
				require.NoError(t, os.Symlink(filepath.Join("..", "hnsw"), filepath.Join(dbPath, "alfred")))
				writeTar(t, archive, []tarEntry{
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
					{"audiobooks.pebble/alfred/hnsw.clean-stop", testCleanStopMarker},
				})
			},
			wantError: "HNSW snapshot directory",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dbPath := liveRestore(t)
			live := database.HNSWSnapshotDir(dbPath)
			plantMarker(t, live)
			tc.setup(t, dbPath, filepath.Join(config.AppConfig.BackupDir, "hush.tar"))

			status, body, marks := postRestore(t, "hush.tar", "")
			require.Equal(t, http.StatusBadRequest, status, body)
			for _, want := range append([]string{"backup restore stopped", tc.wantError}, extractRefusalNote...) {
				assert.Contains(t, body, want)
			}
			assert.Equal(t, 1, marks)
			assert.False(t, markerPresent(t, live), "a marker vouches after the refusal")
			// What the body warns of: the entry before the refused one.
			got, err := os.ReadFile(filepath.Join(dbPath, "MANIFEST-000001"))
			require.NoError(t, err)
			assert.Equal(t, "batcave", string(got))
			_, err = os.Lstat(filepath.Join(filepath.Dir(root), "joker"))
			assert.True(t, os.IsNotExist(err), "an entry climbed out of the target (lstat: %v)", err)
		})
	}
}

// TestRestoreBackup_ThroughSymlinkedDataAndSnapshotDirectories: what B1-r8's
// physical walk must keep working, the reviewer's legitimate layout. The
// data directory is reached through a symlink, the database directory is a
// symlink to another disk, and then the snapshot directory is one too; each
// restore, over the live database and into a custom target, succeeds and
// leaves no marker.
func TestRestoreBackup_ThroughSymlinkedDataAndSnapshotDirectories(t *testing.T) {
	disk := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(disk, "wayne-manor"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(disk, "batcave-db"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(disk, "batcave-db"), filepath.Join(disk, "wayne-manor", "audiobooks.pebble")))
	link := filepath.Join(t.TempDir(), "wayne-manor")
	require.NoError(t, os.Symlink(filepath.Join(disk, "wayne-manor"), link))
	prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
	config.AppConfig.DatabasePath = filepath.Join(link, "audiobooks.pebble")
	config.AppConfig.BackupDir = filepath.Join(disk, "backups")
	t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
	require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
	writeTar(t, filepath.Join(config.AppConfig.BackupDir, "hush.tar"), []tarEntry{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"audiobooks.pebble/archive/000002.sst", "arkham"},
	})

	live := filepath.Join(link, "hnsw")
	plantMarker(t, live)
	status, body, _ := postRestore(t, "hush.tar", "")
	require.Equal(t, http.StatusOK, status, body)
	got, err := os.ReadFile(filepath.Join(disk, "batcave-db", "archive", "000002.sst"))
	require.NoError(t, err)
	assert.Equal(t, "arkham", string(got))
	assert.False(t, markerPresent(t, live))
	status, body, _ = postRestore(t, "hush.tar", filepath.Join(disk, "restore-test"))
	require.Equal(t, http.StatusOK, status, body)

	// The snapshot directory a symlink to another disk as well.
	require.NoError(t, os.RemoveAll(filepath.Join(disk, "wayne-manor", "hnsw")))
	require.NoError(t, os.MkdirAll(filepath.Join(disk, "hnsw-disk"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(disk, "hnsw-disk"), filepath.Join(disk, "wayne-manor", "hnsw")))
	plantMarker(t, filepath.Join(disk, "hnsw-disk"))
	status, body, _ = postRestore(t, "hush.tar", "")
	require.Equal(t, http.StatusOK, status, body)
	assert.False(t, markerPresent(t, filepath.Join(disk, "hnsw-disk")))
}
