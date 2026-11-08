// file: internal/server/handlers/system/restore_ann_snapshot_test.go
// version: 1.0.0
// guid: b3587e2b-0397-4f38-8a01-ae6bdd068607
// last-edited: 2026-11-08

package system_test

import (
	"archive/tar"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R1-15 round-2 review (B1): a restore writes Pebble files behind the GatedDB
// the HNSW store counts writes through, so after one the handler marks the
// live store diverged and removes every clean-stop marker that could vouch
// for the old graph. The server half (the wiring, and a quiet stop writing no
// marker) is TestRestoreBackup_NoCleanStopMarkerAfterARestore in package
// server.

const testCleanStopMarker = `{"version":1,"model":"bge-m3","at":"2026-11-08T00:00:00Z"}`

type tarEntry struct{ name, body string }

// writeTar writes an uncompressed tar archive: the restore sniffs the codec
// from the bytes and falls back to plain tar.
func writeTar(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
}

func plantMarker(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hnsw.clean-stop"), []byte(testCleanStopMarker), 0o644))
}

func markerPresent(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "hnsw.clean-stop"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
	return err == nil
}

func TestRestoreBackup_DistrustsTheHNSWSnapshot(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		// request names another backup than the one written, when set.
		request string
		verify  bool
		sidecar string
		// custom restores into root/gotham instead of the live database's
		// directory.
		custom    bool
		plantLive bool
		// refused: the restore wrote nothing, so the snapshot keeps its trust
		// and a marker planted beside the live database stays.
		refused    bool
		wantStatus int
	}{
		{
			// The production default: the restore overwrites the live database.
			// The archive also carries a marker, which only a removal after the
			// extraction catches.
			name: "default target, the archive carries a marker",
			entries: []tarEntry{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{"hnsw/hnsw.clean-stop", testCleanStopMarker},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "custom target",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			custom:     true,
			plantLive:  true,
			wantStatus: http.StatusOK,
		},
		{
			// The extraction writes the marker, then fails on an entry that
			// escapes the target: files are on disk although the restore
			// failed.
			name: "the extraction fails partway",
			entries: []tarEntry{
				{"hnsw/hnsw.clean-stop", testCleanStopMarker},
				{"../joker", "hahaha"},
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "refused: the backup is not there",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			request:    "year-one.tar",
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "refused: no checksum to verify against",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			verify:     true,
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "refused: the checksum does not match",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			verify:     true,
			sidecar:    "00  the-killing-joke.tar\n",
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "audiobooks.pebble")
			require.NoError(t, os.MkdirAll(dbPath, 0o755))
			prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
			config.AppConfig.DatabasePath = dbPath
			config.AppConfig.BackupDir = filepath.Join(root, "backups")
			t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
			require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
			archive := filepath.Join(config.AppConfig.BackupDir, "the-killing-joke.tar")
			writeTar(t, archive, tc.entries)
			if tc.sidecar != "" {
				require.NoError(t, os.WriteFile(archive+".sha256", []byte(tc.sidecar), 0o644))
			}

			req := map[string]any{"backup_filename": "the-killing-joke.tar", "verify": tc.verify}
			if tc.request != "" {
				req["backup_filename"] = tc.request
			}
			liveDir := filepath.Join(root, "hnsw")
			restoredDir := liveDir
			if tc.custom {
				target := filepath.Join(root, "gotham")
				restoredDir = filepath.Join(target, "hnsw")
				plantMarker(t, restoredDir)
				req["target_path"] = target
			}
			if tc.plantLive {
				plantMarker(t, liveDir)
			}
			body, err := json.Marshal(req)
			require.NoError(t, err)

			h, _ := newTestHandler(t)
			var reasons []string
			h.WithANNSnapshotDivergence(func(reason string) { reasons = append(reasons, reason) })
			w := run(http.MethodPost, "/backup/restore", "/backup/restore", body, func(r *gin.Engine) {
				r.POST("/backup/restore", h.RestoreBackup)
			})

			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			if tc.refused {
				assert.Empty(t, reasons, "a restore that wrote nothing must not mark the store diverged")
				assert.True(t, markerPresent(t, liveDir), "a restore that wrote nothing must leave the marker")
				return
			}
			require.Len(t, reasons, 1, "the live store must be marked diverged once")
			assert.Contains(t, reasons[0], "restore")
			assert.False(t, markerPresent(t, liveDir), "no marker may vouch for the live database's snapshot")
			assert.False(t, markerPresent(t, restoredDir), "no marker may vouch for the restored database's snapshot")
		})
	}
}
