// file: internal/itunes/service/transfer_handler_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-06-16

package itunesservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTransferRouter returns a gin router with all TransferService routes registered.
func newTransferRouter(ts *TransferService) *gin.Engine {
	r := gin.New()
	r.GET("/library/download", ts.HandleDownload)
	r.POST("/library/upload", ts.HandleUpload)
	r.GET("/library/backups", ts.HandleBackupList)
	r.POST("/library/restore", ts.HandleRestore)
	return r
}

// setITLPath sets config.AppConfig.ITunes.LibraryWritePath for the duration
// of a test and restores it on cleanup.
func setITLPath(t *testing.T, path string) {
	t.Helper()
	orig := config.AppConfig.ITunes.LibraryWritePath
	config.AppConfig.ITunes.LibraryWritePath = path
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryWritePath = orig })
}

// ---------------------------------------------------------------------------
// HandleDownload
// ---------------------------------------------------------------------------

func TestHandleDownload_NotConfigured(t *testing.T) {
	setITLPath(t, "")
	ts := newTransferService()
	r := newTransferRouter(ts)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library/download", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "ITunesLibraryWritePath is not configured")
}

func TestHandleDownload_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	setITLPath(t, filepath.Join(dir, "nonexistent.itl"))
	ts := newTransferService()
	r := newTransferRouter(ts)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library/download", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "ITL file not found")
}

func TestHandleDownload_ServesFile(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "iTunes Library.itl")
	content := []byte("fake-itl-binary-data")
	require.NoError(t, os.WriteFile(itlPath, content, 0o644))

	setITLPath(t, itlPath)
	ts := newTransferService()
	r := newTransferRouter(ts)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library/download", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, content, w.Body.Bytes())
	assert.Contains(t, w.Header().Get("Content-Disposition"), "iTunes Library.itl")
	assert.Equal(t, fmt.Sprintf("%d", len(content)), w.Header().Get("Content-Length"))
}

// ---------------------------------------------------------------------------
// HandleBackupList
// ---------------------------------------------------------------------------

func TestHandleBackupList_NotConfigured(t *testing.T) {
	setITLPath(t, "")
	ts := newTransferService()
	r := newTransferRouter(ts)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library/backups", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleBackupList_Empty(t *testing.T) {
	dir := t.TempDir()
	setITLPath(t, filepath.Join(dir, "iTunes Library.itl"))
	ts := newTransferService()
	r := newTransferRouter(ts)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library/backups", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"count":0`)
}

func TestHandleBackupList_WithBackups(t *testing.T) {
	dir := t.TempDir()
	base := "iTunes Library.itl"
	itlPath := filepath.Join(dir, base)

	// Create main file + two backups
	require.NoError(t, os.WriteFile(itlPath, []byte("main"), 0o644))

	bak1 := filepath.Join(dir, base+".bak-20260101T000000Z")
	bak2 := filepath.Join(dir, base+".bak-20260102T000000Z")
	require.NoError(t, os.WriteFile(bak1, []byte("backup1"), 0o644))
	// Ensure bak2 has a later mtime
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, os.WriteFile(bak2, []byte("backup2"), 0o644))

	setITLPath(t, itlPath)
	ts := newTransferService()
	r := newTransferRouter(ts)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/library/backups", nil))

	assert.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data struct {
			Count   int `json:"count"`
			Backups []struct {
				Name string `json:"name"`
			} `json:"backups"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, 2, envelope.Data.Count)
	require.NotEmpty(t, envelope.Data.Backups, "should have backups in response")
	// Newest-first: bak2 should come first
	assert.Equal(t, filepath.Base(bak2), envelope.Data.Backups[0].Name)
}

// ---------------------------------------------------------------------------
// HandleUpload
// ---------------------------------------------------------------------------

func TestHandleUpload_NotConfigured(t *testing.T) {
	setITLPath(t, "")
	ts := newTransferService()
	r := newTransferRouter(ts)

	body, ct := makeMultipartITL(t, []byte("data"))
	req := httptest.NewRequest(http.MethodPost, "/library/upload", body)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ITunesLibraryWritePath is not configured")
}

func TestHandleUpload_MissingFormField(t *testing.T) {
	dir := t.TempDir()
	setITLPath(t, filepath.Join(dir, "iTunes Library.itl"))
	ts := newTransferService()
	r := newTransferRouter(ts)

	// Send an empty body (no form file)
	req := httptest.NewRequest(http.MethodPost, "/library/upload", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xxxboundary")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "library")
}

func TestHandleUpload_InvalidITL(t *testing.T) {
	dir := t.TempDir()
	setITLPath(t, filepath.Join(dir, "iTunes Library.itl"))
	ts := newTransferService()
	r := newTransferRouter(ts)

	// Upload garbage bytes that won't parse as a valid ITL
	body, ct := makeMultipartITL(t, []byte("this-is-not-an-itl-file"))
	req := httptest.NewRequest(http.MethodPost, "/library/upload?install=false", body)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid ITL")
}

func TestHandleUpload_ValidITL_NoInstall(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "iTunes Library.itl")
	setITLPath(t, itlPath)
	ts := newTransferService()
	r := newTransferRouter(ts)

	itlData := readTestITL(t)
	body, ct := makeMultipartITL(t, itlData)
	req := httptest.NewRequest(http.MethodPost, "/library/upload?install=false", body)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data ITLUploadResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.True(t, envelope.Data.Valid)
	assert.False(t, envelope.Data.Installed)

	// Target file must NOT have been created (install=false)
	_, err := os.Stat(itlPath)
	assert.True(t, os.IsNotExist(err), "ITL file must not be installed when install=false")
}

func TestHandleUpload_ValidITL_WithInstall(t *testing.T) {
	dir := t.TempDir()
	itlPath := filepath.Join(dir, "iTunes Library.itl")
	setITLPath(t, itlPath)
	ts := newTransferService()
	r := newTransferRouter(ts)

	itlData := readTestITL(t)
	body, ct := makeMultipartITL(t, itlData)
	req := httptest.NewRequest(http.MethodPost, "/library/upload?install=true", body)
	req.Header.Set("Content-Type", ct)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data ITLUploadResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.True(t, envelope.Data.Valid)
	assert.True(t, envelope.Data.Installed)

	// File must now exist at the configured path
	_, err := os.Stat(itlPath)
	require.NoError(t, err, "ITL file must be installed")
}

// ---------------------------------------------------------------------------
// HandleRestore
// ---------------------------------------------------------------------------

func TestHandleRestore_NotConfigured(t *testing.T) {
	setITLPath(t, "")
	ts := newTransferService()
	r := newTransferRouter(ts)

	body := strings.NewReader(`{"backup_name":"iTunes Library.itl.bak-20260101T000000Z"}`)
	req := httptest.NewRequest(http.MethodPost, "/library/restore", body)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRestore_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	setITLPath(t, filepath.Join(dir, "iTunes Library.itl"))
	ts := newTransferService()
	r := newTransferRouter(ts)

	body := strings.NewReader(`{"backup_name":"../evil.itl"}`)
	req := httptest.NewRequest(http.MethodPost, "/library/restore", body)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "filename")
}

func TestHandleRestore_UnrecognizedBackup(t *testing.T) {
	dir := t.TempDir()
	setITLPath(t, filepath.Join(dir, "iTunes Library.itl"))
	ts := newTransferService()
	r := newTransferRouter(ts)

	// Valid filename but doesn't have the right .bak- prefix pattern
	body := strings.NewReader(`{"backup_name":"something-else.itl"}`)
	req := httptest.NewRequest(http.MethodPost, "/library/restore", body)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "recognized ITL backup")
}

func TestHandleRestore_ValidBackup(t *testing.T) {
	dir := t.TempDir()
	base := "iTunes Library.itl"
	itlPath := filepath.Join(dir, base)
	bakName := base + ".bak-20260101T000000Z"
	bakPath := filepath.Join(dir, bakName)

	// Write current ITL
	require.NoError(t, os.WriteFile(itlPath, []byte("old"), 0o644))
	// Write backup with real ITL data
	itlData := readTestITL(t)
	require.NoError(t, os.WriteFile(bakPath, itlData, 0o644))

	setITLPath(t, itlPath)
	ts := newTransferService()
	r := newTransferRouter(ts)

	body, _ := json.Marshal(ITLRestoreRequest{BackupName: bakName})
	req := httptest.NewRequest(http.MethodPost, "/library/restore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var envelope struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.Equal(t, true, envelope.Data["restored"])

	// Current ITL should now contain the backup's data
	got, err := os.ReadFile(itlPath)
	require.NoError(t, err)
	assert.Equal(t, itlData, got)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeMultipartITL builds a multipart/form-data body with a "library" field.
func makeMultipartITL(t *testing.T, data []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("library", "iTunes Library.itl")
	require.NoError(t, err)
	_, err = fw.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

// readTestITL reads the shared ITL fixture from the itunes package testdata.
func readTestITL(t *testing.T) []byte {
	t.Helper()
	// Navigate from internal/itunes/service → internal/itunes/testdata
	fixture := filepath.Join("..", "testdata", "test_library.itl")
	data, err := os.ReadFile(fixture)
	require.NoError(t, err, "test ITL fixture missing at %s", fixture)
	return data
}
