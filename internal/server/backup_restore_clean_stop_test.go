// file: internal/server/backup_restore_clean_stop_test.go
// version: 1.0.1
// guid: a9d6cf17-ae83-4c1f-8e9c-20c081498477
// last-edited: 2026-11-09

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/stretchr/testify/require"
)

// R1-15 round-2 review (B1), on the staged restore (R1-25): POST
// /backup/restore stages an archive that the next boot swaps in for the
// Pebble files, behind the GatedDB the verdict watch counts writes through.
// Without the stage telling the HNSW store, a run that stages a restore and
// then stops (the restart that applies it included) writes a clean-stop
// marker, and the boot after the swap loads the old graph as a true copy of
// the restored rows.

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

	c := serviceregistry.NewContainerFrom().Override("chromemstore", hnsw)
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

// cleanStopStager stands in for books' restore stager: it runs the
// handler's divergence mark when the stage passes, as the real one does
// before it writes the intent, and refuses without it otherwise.
type cleanStopStager struct {
	err      error
	restarts int
}

func (c *cleanStopStager) StageRestore(_ context.Context, _, _ string, _ bool, _ string, diverged func()) (backup.StagedRestore, error) {
	if c.err != nil {
		return backup.StagedRestore{}, c.err
	}
	diverged()
	return backup.StagedRestore{ID: "20261109T000000Z-joker001"}, nil
}

func (c *cleanStopStager) RequestRestart(string) { c.restarts++ }

func TestRestoreBackup_NoCleanStopMarkerAfterARestore(t *testing.T) {
	// The controls: the same server and the same stop, with no restore or a
	// refused one, leave a marker. Without them the case below could pass on
	// a fixture that never writes one.
	t.Run("no restore", func(t *testing.T) {
		s, _ := restoreCleanStopServer(t)
		s.stopHNSWSnapshot(context.Background())
		s.markHNSWCleanStop(nil)
		require.True(t, cleanStopMarkerExists(t, s.hnswPersistDir), "a quiet stop with nothing restored must leave the marker")
	})

	t.Run("refused restore, then a quiet stop", func(t *testing.T) {
		s, _ := restoreCleanStopServer(t)
		st := &cleanStopStager{err: restorejournal.ErrSwapPending}
		s.SetRestoreStager(st)
		s.stopHNSWSnapshot(context.Background())
		w := serveJSON(t, s, http.MethodPost, "/api/v1/backup/restore", map[string]any{"backup_filename": "gotham.tar.gz"})
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		require.Zero(t, st.restarts)

		s.markHNSWCleanStop(nil)
		require.True(t, cleanStopMarkerExists(t, s.hnswPersistDir), "a refused restore changed nothing, so the stop may vouch for the graph")
	})

	t.Run("restore, then a quiet stop", func(t *testing.T) {
		s, hnsw := restoreCleanStopServer(t)
		st := &cleanStopStager{}
		s.SetRestoreStager(st)
		s.stopHNSWSnapshot(context.Background())
		w := serveJSON(t, s, http.MethodPost, "/api/v1/backup/restore", map[string]any{"backup_filename": "gotham.tar.gz"})
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		require.Equal(t, 1, st.restarts)

		s.markHNSWCleanStop(nil)
		require.False(t, cleanStopMarkerExists(t, s.hnswPersistDir), "a stop after a staged restore must not vouch for the graph")
		written, reason, err := hnsw.MarkCleanStop(s.hnswPersistDir)
		require.NoError(t, err)
		require.False(t, written)
		require.Contains(t, reason, "restore")
	})
}
