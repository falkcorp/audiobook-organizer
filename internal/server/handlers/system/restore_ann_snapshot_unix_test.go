// file: internal/server/handlers/system/restore_ann_snapshot_unix_test.go
// version: 1.0.0
// guid: f2f84852-8cbf-4ea5-a2a6-f2ca28667915
// last-edited: 2026-11-08

//go:build !windows

package system_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreBackup_RemovesTheRestoredMarkerBeforeTheFirstWrite (R1-15
// round-5 verification review, the B1r-5 order): a marker that was already
// beside a custom target's database used to be removed only after the
// extraction, so a crash or exit during the extraction left it vouching for
// a snapshot the half-restored database does not match. That is the window
// B1-r3 closed for the live store. The restore now removes it before its
// first write as well. A named pipe as the destination of the archive's
// first entry holds the extraction inside that entry (its write cannot pass
// the pipe buffer until the test reads), and the test looks for the marker
// at that moment.
func TestRestoreBackup_RemovesTheRestoredMarkerBeforeTheFirstWrite(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "audiobooks.pebble")
	require.NoError(t, os.MkdirAll(dbPath, 0o755))
	prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
	config.AppConfig.DatabasePath = dbPath
	config.AppConfig.BackupDir = filepath.Join(root, "backups")
	t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
	require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))

	target := filepath.Join(root, "gotham")
	restoredDir := filepath.Join(target, "hnsw")
	plantMarker(t, restoredDir)
	pipe := filepath.Join(target, "audiobooks.pebble", "pipe")
	require.NoError(t, os.MkdirAll(filepath.Dir(pipe), 0o755))
	require.NoError(t, syscall.Mkfifo(pipe, 0o600))

	writeTar(t, filepath.Join(config.AppConfig.BackupDir, "no-mans-land.tar"), []tarEntry{
		{"audiobooks.pebble/pipe", strings.Repeat("bane", 1<<18)},
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
	})

	// The reader opens the pipe (blocking until the extraction opens it to
	// write), looks for the marker while the extraction is held, then
	// drains the pipe so the extraction can go on.
	type sighting struct{ reached, present bool }
	seen := make(chan sighting, 1)
	go func() {
		f, err := os.Open(pipe)
		if err != nil {
			seen <- sighting{}
			return
		}
		_, statErr := os.Stat(filepath.Join(restoredDir, "hnsw.clean-stop"))
		_, _ = io.Copy(io.Discard, f)
		_ = f.Close()
		seen <- sighting{reached: true, present: statErr == nil}
	}()

	h, _ := newTestHandler(t)
	h.WithANNSnapshotDivergence(func(string) {})
	body, err := json.Marshal(map[string]any{"backup_filename": "no-mans-land.tar", "target_path": target})
	require.NoError(t, err)
	w := run(http.MethodPost, "/backup/restore", "/backup/restore", body, func(r *gin.Engine) {
		r.POST("/backup/restore", h.RestoreBackup)
	})

	var got sighting
	select {
	case got = <-seen:
	case <-time.After(10 * time.Second):
		// The extraction never opened the pipe; release the reader.
		if f, err := os.OpenFile(pipe, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	}
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, got.reached, "the extraction never reached the pipe")
	assert.False(t, got.present, "the marker beside the custom target's database was still there during the extraction")
	assert.False(t, markerPresent(t, restoredDir), "no marker may vouch for the restored database's snapshot")
}
