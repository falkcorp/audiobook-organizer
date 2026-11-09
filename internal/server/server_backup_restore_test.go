// file: internal/server/server_backup_restore_test.go
// version: 1.1.2
// guid: 3c2f1e0d-9a8b-7c6d-5e4f-3a2b1c0d9e8f
// last-edited: 2026-11-09

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
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// routeStager is a stub restore stager for the route's wiring test.
type routeStager struct{ staged, restarts []string }

func (r *routeStager) StageRestore(_ context.Context, _, file string, _ bool, _ string) (backup.StagedRestore, error) {
	r.staged = append(r.staged, file)
	return backup.StagedRestore{ID: "20261109T000000Z-robin001"}, nil
}

func (r *routeStager) RequestRestart(reason string) { r.restarts = append(r.restarts, reason) }

// TestRestoreBackupRoute_StagesThroughTheInstalledStager (R1-25): POST
// /api/v1/backup/restore reaches the stager books installs after wiring
// (Server.SetRestoreStager, read lazily by the system handler): before one
// is installed the route answers 503 and stages nothing; once installed a
// restore of a listed backup is staged, answered 202 with restart_required,
// and the restart is requested once; a target_path is refused 400 without
// reaching the stager.
func TestRestoreBackupRoute_StagesThroughTheInstalledStager(t *testing.T) {
	server, cleanup := setupTestServerRealFS(t)
	defer cleanup()

	post := func(body map[string]any) *httptest.ResponseRecorder {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.router.ServeHTTP(w, req)
		return w
	}

	w := post(map[string]any{"backup_filename": "gotham.tar.gz"})
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())

	st := &routeStager{}
	server.SetRestoreStager(st)
	w = post(map[string]any{"backup_filename": "gotham.tar.gz", "verify": false})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			RestartRequired bool   `json:"restart_required"`
			RestoreID       string `json:"restore_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Data.RestartRequired)
	require.Equal(t, "20261109T000000Z-robin001", resp.Data.RestoreID)
	require.Equal(t, []string{"gotham.tar.gz"}, st.staged)
	require.Len(t, st.restarts, 1)

	w = post(map[string]any{"backup_filename": "gotham.tar.gz", "target_path": filepath.Join(t.TempDir(), "arkham")})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Len(t, st.staged, 1, "a target_path request reached the stager")
}

func TestDeleteBackup_Success(t *testing.T) {
	server, cleanup := setupTestServerRealFS(t)
	defer cleanup()

	wd, err := os.Getwd()
	require.NoError(t, err)
	tmp := t.TempDir()
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// Create a backup first.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/create", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var wrapper struct {
		Data backup.BackupInfo `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &wrapper))
	info := wrapper.Data
	require.NotEmpty(t, info.Filename)

	// Backup dir is resolved relative to DatabasePath directory
	backupDir := backup.DefaultBackupConfig().BackupDir
	if dbPath := config.AppConfig.DatabasePath; dbPath != "" && !filepath.IsAbs(backupDir) {
		backupDir = filepath.Join(filepath.Dir(dbPath), backupDir)
	}
	backupPath := filepath.Join(backupDir, info.Filename)
	_, err = os.Stat(backupPath)
	require.NoError(t, err)

	// Delete it via endpoint.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/backup/"+info.Filename, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	_, err = os.Stat(backupPath)
	require.Error(t, err)
}
