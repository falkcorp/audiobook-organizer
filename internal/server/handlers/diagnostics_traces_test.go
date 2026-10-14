// file: internal/server/handlers/diagnostics_traces_test.go
// version: 1.0.0
// guid: 22bc0bf7-bcbc-4384-892c-2156b00b3718
// last-edited: 2026-10-14

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	diagflight "github.com/falkcorp/audiobook-organizer/internal/diag/flight"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newTracesRouter serves the two traces endpoints over a recorder that is not
// recording (Disabled), holding the given snapshot files. List and Open work
// on such a recorder, which is what keeps old traces downloadable after
// ABK_FLIGHT_RECORDER=off.
func newTracesRouter(t *testing.T, files map[string]string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	h := NewDiagnosticsTracesHandler(diagflight.New(diagflight.Config{Dir: dir, Disabled: true}))
	r := gin.New()
	r.GET("/diagnostics/traces", h.List)
	r.GET("/diagnostics/traces/:name", h.Download)
	return r
}

func serve(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestDiagnosticsTraces_ListNewestFirst(t *testing.T) {
	r := newTracesRouter(t, map[string]string{
		"20261012T000000Z-search-index-stall.trace":      "a",
		"20261014T000000Z-watchdog-stuck.trace":          "bbb",
		"20261013T000000Z-watchdog-never-reported.trace": "cc",
		"notes.txt": "not a trace",
	})
	w := serve(r, "/diagnostics/traces")
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Traces []diagflight.FileInfo `json:"traces"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	var names []string
	for _, f := range body.Traces {
		names = append(names, f.Name)
	}
	require.Equal(t, []string{
		"20261014T000000Z-watchdog-stuck.trace",
		"20261013T000000Z-watchdog-never-reported.trace",
		"20261012T000000Z-search-index-stall.trace",
	}, names)
	require.EqualValues(t, 3, body.Traces[0].Size)
	require.Contains(t, w.Body.String(), `"modified"`)
}

func TestDiagnosticsTraces_ListEmptyIsArray(t *testing.T) {
	w := serve(newTracesRouter(t, nil), "/diagnostics/traces")
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"traces":[]}`, w.Body.String())
}

func TestDiagnosticsTraces_DownloadKnownFile(t *testing.T) {
	const name = "20261014T000000Z-watchdog-stuck.trace"
	r := newTracesRouter(t, map[string]string{name: "go 1.27 trace"})
	w := serve(r, "/diagnostics/traces/"+name)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "go 1.27 trace", w.Body.String())
	require.Equal(t, "application/octet-stream", w.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename=`+name, w.Header().Get("Content-Disposition"))
}

func TestDiagnosticsTraces_DownloadRejectsBadNames(t *testing.T) {
	r := newTracesRouter(t, map[string]string{
		"20261014T000000Z-watchdog-stuck.trace": "x",
		"notes.txt":                             "not a trace",
	})
	for _, name := range []string{
		"..%2Fx", "notes.txt", "20261014T000000Z-missing.trace",
		"..%2F..%2Fetc%2Fpasswd", "20261014T000000Z-WATCHDOG.trace", "%2Fetc%2Fpasswd",
	} {
		w := serve(r, "/diagnostics/traces/"+name)
		require.Equal(t, http.StatusNotFound, w.Code, "name %q", name)
		require.NotContains(t, strings.ToLower(w.Body.String()), "passwd", "the 404 must not echo the input")
		require.NotContains(t, w.Body.String(), "notes", "the 404 must not echo the input")
	}
}

func TestDiagnosticsTraces_NilStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewDiagnosticsTracesHandler(nil)
	r := gin.New()
	r.GET("/diagnostics/traces", h.List)
	r.GET("/diagnostics/traces/:name", h.Download)
	require.JSONEq(t, `{"traces":[]}`, serve(r, "/diagnostics/traces").Body.String())
	require.Equal(t, http.StatusNotFound, serve(r, "/diagnostics/traces/20261014T000000Z-x.trace").Code)
}
