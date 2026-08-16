// file: internal/server/server_backup_restore_test.go
// version: 1.1.0
// guid: 3c2f1e0d-9a8b-7c6d-5e4f-3a2b1c0d9e8f
// last-edited: 2026-08-15

package server

import (
	"bytes"
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

func TestRestoreBackup_Success(t *testing.T) {
	server, cleanup := setupTestServerRealFS(t)
	defer cleanup()

	wd, err := os.Getwd()
	require.NoError(t, err)
	tmp := t.TempDir()
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// Create a backup in temp working directory (backups/...).
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

	// Restore into a new target directory.
	target := filepath.Join(tmp, "restore-target")
	payload, err := json.Marshal(map[string]interface{}{
		"backup_filename": info.Filename,
		"target_path":     target,
		"verify":          false,
	})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/backup/restore", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Ensure the database file was restored.
	restored := filepath.Join(target, filepath.Base(config.AppConfig.DatabasePath))
	_, err = os.Stat(restored)
	require.NoError(t, err)
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
