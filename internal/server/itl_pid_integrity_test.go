// file: internal/server/itl_pid_integrity_test.go
// version: 1.1.0
// guid: 5a3c8e17-d204-4b69-8f15-c7e92b0a6d34
// last-edited: 2026-10-18

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// singleJSONDoc decodes w's body as exactly one JSON document: it fails when
// the body is not JSON or when a second document follows the first. The
// pid-integrity handler used to write its 400 and then carry on and write a
// second document after it.
func singleJSONDoc(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	var doc map[string]any
	require.NoError(t, dec.Decode(&doc), w.Body.String())
	var extra json.RawMessage
	require.ErrorIs(t, dec.Decode(&extra), io.EOF, "a second response was written after the first: %s", w.Body.String())
	return doc
}

// TestPIDIntegrity_UnsetITLPath_CensusWithoutITL: the ITL path is optional.
// With none configured GET /itunes/pid-integrity answers one 200 carrying the
// census, with tracks_in_itl 0, and writes nothing after it.
func TestPIDIntegrity_UnsetITLPath_CensusWithoutITL(t *testing.T) {
	f := setupCredGuardServer(t)
	config.AppConfig.ITunes.LibraryITLPath = ""

	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/itunes/pid-integrity", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	doc := singleJSONDoc(t, w)
	data, ok := doc["data"].(map[string]any)
	require.True(t, ok, "the 200 must carry a data envelope: %s", w.Body.String())
	report, ok := data["report"].(map[string]any)
	require.True(t, ok, "the 200 must carry the report: %s", w.Body.String())
	assert.EqualValues(t, 0, report["tracks_in_itl"])
}

// TestPIDIntegrity_InvalidITLPath_SingleInternalError: a path that is set but
// not usable answers one 500 and stops; the census is not written after it.
func TestPIDIntegrity_InvalidITLPath_SingleInternalError(t *testing.T) {
	f := setupCredGuardServer(t)
	config.AppConfig.ITunes.LibraryITLPath = "relative/not-absolute.itl"

	w := f.do(t, f.sessionToken, http.MethodGet, "/api/v1/itunes/pid-integrity", nil)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	doc := singleJSONDoc(t, w)
	assert.NotContains(t, doc, "report")
	assert.Contains(t, doc["error"], "invalid iTunes library ITL path")
}
