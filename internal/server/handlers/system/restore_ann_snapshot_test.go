// file: internal/server/handlers/system/restore_ann_snapshot_test.go
// version: 1.0.1
// guid: b3587e2b-0397-4f38-8a01-ae6bdd068607
// last-edited: 2026-11-08

package system_test

import (
	"archive/tar"
	"context"
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

// R1-15 round-2 review (B1): a restore writes Pebble files behind the GatedDB
// the HNSW store counts writes through, so the handler marks the live store
// diverged and removes every clean-stop marker that could vouch for the old
// graph. Round 3 (B1-r3) moved the mark before the first write:
// TestRestoreBackup_MarksTheStoreBeforeTheFirstWrite. The server half (the
// wiring, and a quiet stop writing no marker) is
// TestRestoreBackup_NoCleanStopMarkerAfterARestore in package server.

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
		// noBackupDir points the backup directory at one that does not exist.
		noBackupDir bool
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
			name:        "refused: the backup directory cannot be read",
			entries:     []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			noBackupDir: true,
			plantLive:   true,
			refused:     true,
			wantStatus:  http.StatusInternalServerError,
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
			if tc.noBackupDir {
				config.AppConfig.BackupDir = filepath.Join(root, "arkham")
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

// stopReadyStore returns an HNSW store whose MarkCleanStop would write a
// marker in dir: complete, exported to dir, and bound to a database of its own
// that no write has touched.
func stopReadyStore(t *testing.T, dir string) *database.HNSWEmbeddingStore {
	t.Helper()
	s := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, s.Upsert(context.Background(), "book", "the-long-halloween", []float32{1, 0, 0, 0}, nil))
	s.SetComplete(true)
	ps, err := database.NewPebbleStoreInMemory("restore-truth")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	s.BindTruthSource(ps.DB(), "")
	require.NoError(t, s.Export(dir))
	return s
}

// TestRestoreBackup_MarksTheStoreBeforeTheFirstWrite (R1-15 round-3 review,
// B1-r3): the kernel's drain does not wait for handler goroutines, so a stop
// can run MarkCleanStop while a restore is extracting, and the process can
// exit before the handler returns. The live store must therefore be marked
// diverged before the restore writes anything, not after: a stop that lands
// at that point, or anywhere after it, refuses the marker.
func TestRestoreBackup_MarksTheStoreBeforeTheFirstWrite(t *testing.T) {
	entries := []tarEntry{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"audiobooks.pebble/OPTIONS-000002", "alfred"},
	}

	// The control: with no restore, the same store writes its marker, so the
	// refusal below is the restore's doing and not the fixture's.
	t.Run("no restore", func(t *testing.T) {
		snap := t.TempDir()
		written, reason, err := stopReadyStore(t, snap).MarkCleanStop(snap)
		require.NoError(t, err)
		require.True(t, written, "the fixture must be a store a stop lets write a marker: %s", reason)
	})

	t.Run("a stop during the restore", func(t *testing.T) {
		root := t.TempDir()
		dbPath := filepath.Join(root, "audiobooks.pebble")
		require.NoError(t, os.MkdirAll(dbPath, 0o755))
		prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
		config.AppConfig.DatabasePath = dbPath
		config.AppConfig.BackupDir = filepath.Join(root, "backups")
		t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
		require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
		writeTar(t, filepath.Join(config.AppConfig.BackupDir, "a-death-in-the-family.tar"), entries)

		snap := filepath.Join(root, "stop-snapshot")
		store := stopReadyStore(t, snap)

		var onDiskAtMark []string
		var written bool
		var reason string
		fired := 0
		h, _ := newTestHandler(t)
		h.WithANNSnapshotDivergence(func(r string) {
			fired++
			for _, e := range entries {
				if _, err := os.Stat(filepath.Join(root, e.name)); err == nil {
					onDiskAtMark = append(onDiskAtMark, e.name)
				}
			}
			store.MarkDiverged(r)
			// A stop that lands now, while the extraction is still to run.
			var err error
			written, reason, err = store.MarkCleanStop(snap)
			require.NoError(t, err)
		})
		body, err := json.Marshal(map[string]any{"backup_filename": "a-death-in-the-family.tar"})
		require.NoError(t, err)
		w := run(http.MethodPost, "/backup/restore", "/backup/restore", body, func(r *gin.Engine) {
			r.POST("/backup/restore", h.RestoreBackup)
		})

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, fired, "the live store must be marked diverged once")
		assert.Empty(t, onDiskAtMark, "the store must be marked diverged before the restore writes its first file")
		assert.False(t, written, "a stop after the mark must refuse the marker")
		assert.Contains(t, reason, "restore")
		assert.False(t, markerPresent(t, snap), "no marker may vouch for the graph while the restore writes")
	})
}
