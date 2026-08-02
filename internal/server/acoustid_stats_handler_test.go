// file: internal/server/acoustid_stats_handler_test.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fabc-345678901234
// last-edited: 2026-05-31

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGetAcoustIDStats_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)

	want := &database.AcoustIDStats{
		TotalFiles:      10,
		WithFingerprint: 7,
		ByLibrary: []database.AcoustIDStatsByLibrary{
			{LibraryRoot: "/lib/audiobooks", TotalFiles: 10, WithFingerprint: 7},
		},
	}

	store := &database.MockStore{
		GetAcoustIDStatsFunc: func() (*database.AcoustIDStats, error) { return want, nil },
	}

	srv := &Server{store: store}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/maintenance/acoustid-stats", nil)

	srv.handleGetAcoustIDStats(c)

	require.Equal(t, http.StatusOK, w.Code)

	// Handler calls httputil.RespondWithOK(c, stats) which wraps in {"data": <stats>}.
	var body struct {
		Data database.AcoustIDStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, want.TotalFiles, body.Data.TotalFiles)
	assert.Equal(t, want.WithFingerprint, body.Data.WithFingerprint)
	assert.Len(t, body.Data.ByLibrary, 1)
}

func TestHandleGetAcoustIDStats_NilStore(t *testing.T) {
	gin.SetMode(gin.TestMode)

	srv := &Server{} // store field is nil by default

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/maintenance/acoustid-stats", nil)

	srv.handleGetAcoustIDStats(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
