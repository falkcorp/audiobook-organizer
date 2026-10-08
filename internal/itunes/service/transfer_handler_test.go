// file: internal/itunes/service/transfer_handler_test.go
// version: 1.1.2
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-02

package itunesservice

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
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
	return r
}

// setITLPath sets config.AppConfig.ITunes.LibraryITLPath for the duration
// of a test and restores it on cleanup.
func setITLPath(t *testing.T, path string) {
	t.Helper()
	orig := config.AppConfig.ITunes.LibraryITLPath
	config.AppConfig.ITunes.LibraryITLPath = path
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryITLPath = orig })
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

// ---------------------------------------------------------------------------
// HandleUpload
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleRestore
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
