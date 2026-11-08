// file: internal/server/backup_restore_clean_stop_test.go
// version: 1.0.0
// guid: a9d6cf17-ae83-4c1f-8e9c-20c081498477
// last-edited: 2026-11-08

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/stretchr/testify/require"
)

// R1-15 round-2 review (B1): POST /backup/restore extracts an archive into the
// database directory with plain file writes, so the GatedDB verdict watch
// never sees them. Without the restore telling the HNSW store, a run that
// restores and then stops quietly writes a clean-stop marker, and the next
// boot loads the old graph as a true copy of the restored rows.

// restoreCleanStopServer returns a full test server (real routes and wiring)
// whose vector store is a complete HNSW store bound to the server's own
// Pebble database, with its snapshot on disk: a stop with nothing else
// happening writes the clean-stop marker.
func restoreCleanStopServer(t *testing.T) (*Server, *database.HNSWEmbeddingStore) {
	t.Helper()
	server, cleanup := setupTestServerRealFS(t)
	t.Cleanup(cleanup)
	require.NotEmpty(t, server.hnswPersistDir, "NewServer sets the snapshot directory beside the database")

	ps := database.AsPebbleStore(server.storeForWiring())
	require.NotNil(t, ps)
	hnsw := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, hnsw.Upsert(context.Background(), "book", "the-long-halloween", []float32{1, 0, 0, 0}, nil))
	hnsw.SetComplete(true)
	hnsw.BindTruthSource(ps.DB(), "")

	c := serviceregistry.NewContainer().Override("chromemstore", hnsw)
	require.NoError(t, c.Build(context.Background()))
	orig := server.container
	server.container = c
	// Registered after cleanup, so it runs first: the server's own release
	// sees the container it built.
	t.Cleanup(func() { server.container = orig })
	return server, hnsw
}

func serveJSON(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func TestRestoreBackup_NoCleanStopMarkerAfterARestore(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	tmp := t.TempDir()
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// The control: the same server and the same stop, with no restore,
	// leaves a marker. Without it the case below could pass on a fixture
	// that never writes one.
	t.Run("no restore", func(t *testing.T) {
		s, _ := restoreCleanStopServer(t)
		s.stopHNSWSnapshot(context.Background())
		s.markHNSWCleanStop(nil)
		require.True(t, cleanStopMarkerExists(t, s.hnswPersistDir), "a quiet stop with nothing restored must leave the marker")
	})

	t.Run("restore, then a quiet stop", func(t *testing.T) {
		s, hnsw := restoreCleanStopServer(t)
		w := serveJSON(t, s, http.MethodPost, "/api/v1/backup/create", nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var created struct {
			Data backup.BackupInfo `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
		require.NotEmpty(t, created.Data.Filename)

		s.stopHNSWSnapshot(context.Background())
		w = serveJSON(t, s, http.MethodPost, "/api/v1/backup/restore", map[string]any{
			"backup_filename": created.Data.Filename,
			"target_path":     filepath.Join(tmp, "arkham"),
		})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		s.markHNSWCleanStop(nil)
		require.False(t, cleanStopMarkerExists(t, s.hnswPersistDir), "a stop after a restore must not vouch for the graph")
		written, reason, err := hnsw.MarkCleanStop(s.hnswPersistDir)
		require.NoError(t, err)
		require.False(t, written)
		require.Contains(t, reason, "restore")
	})
}
