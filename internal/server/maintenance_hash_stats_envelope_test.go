// file: internal/server/maintenance_hash_stats_envelope_test.go
// version: 1.0.0
// guid: c7e19ddf-806f-4991-94bc-8e9cf695a001
// last-edited: 2026-09-10

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// httputil.RespondWithOK already wraps its payload in {"data": ...}. A handler
// that hands it a second `struct{ Data any }` therefore answers
// {"data":{"data":{...}}}, and the web client -- which unwraps exactly one
// level -- sees every field as undefined. That is what crashed the System ->
// Maintenance tab on `stats.total_book_files.toLocaleString()`.
//
// The assertions below are deliberately structural rather than value-based: a
// fresh in-memory store reports zero for every count, so a typed decode into
// `TotalBookFiles int` reads 0 whether the envelope is single or double and
// proves nothing. Key PRESENCE at exactly one level is what discriminates.
func TestBookFileHashStatsEnvelopeIsNotDoubleWrapped(t *testing.T) {
	srv := setupMaintenanceTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/maintenance/book-file-hash-stats", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "expected 200 OK: %s", w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "body: %s", w.Body.String())

	payload, ok := body["data"].(map[string]any)
	require.True(t, ok, `top-level "data" should be an object: %s`, w.Body.String())

	assert.NotContains(t, payload, "data",
		`data.data is the double envelope -- the client unwraps one level and gets undefined fields: %s`,
		w.Body.String())
	assert.Contains(t, payload, "total_book_files",
		`total_book_files must sit directly under "data": %s`, w.Body.String())
	assert.Contains(t, payload, "missing_file_hash",
		`missing_file_hash must sit directly under "data": %s`, w.Body.String())
}

// The sibling metadata handler always used the single envelope. It is the
// reference shape, and pinning it here stops a future edit from "fixing" the
// two handlers into agreement the wrong way round.
func TestBookMetadataHashStatsEnvelopeIsNotDoubleWrapped(t *testing.T) {
	srv := setupMaintenanceTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/maintenance/book-metadata-hash-stats", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "expected 200 OK: %s", w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "body: %s", w.Body.String())

	payload, ok := body["data"].(map[string]any)
	require.True(t, ok, `top-level "data" should be an object: %s`, w.Body.String())
	assert.NotContains(t, payload, "data", "body: %s", w.Body.String())
	assert.NotEmpty(t, payload, "payload should carry the stats fields: %s", w.Body.String())
}
