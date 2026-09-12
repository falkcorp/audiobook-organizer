// file: internal/server/signals_coverage_handler_test.go
// version: 1.0.0
// guid: 9dc1c17f-e225-48b7-a3c4-79f0246d7c9c
// last-edited: 2026-09-12

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGetSignalCoverage_CountsCoreRows(t *testing.T) {
	gin.SetMode(gin.TestMode)

	failed := time.Now()
	cores := []database.BookFileCore{
		{ID: "f1", FileHash: "h1", Duration: 60, AcoustIDFingerprintDurationSec: 60, RawTags: map[string]string{"title": "x"}},
		{ID: "f2", FileHash: "h2", Missing: true},
		{ID: "f3", FingerprintFailedAt: &failed},
		{ID: "f4", Missing: true, SkipScan: true},
	}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return cores, nil },
		CountPrimaryBooksFunc:   func() (int, error) { return 3, nil },
	}
	srv := &Server{store: store}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/signals/coverage", nil)
	srv.handleGetSignalCoverage(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Data SignalCoverageResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	f := body.Data.Files
	require.NotNil(t, f)
	assert.Equal(t, "core", f.Source)
	assert.False(t, f.ExactFingerprintFields)
	assert.EqualValues(t, 4, f.TotalBookFiles)
	assert.EqualValues(t, 2, f.FileMissingRows)
	assert.EqualValues(t, 2, f.PresentOnDiskRows)
	assert.EqualValues(t, 1, f.SkipScanRows)
	assert.EqualValues(t, 1, f.FingerprintFailures)

	hash := f.Signals[database.SignalFileHash]
	assert.Equal(t, database.SignalCount{Have: 2, Missing: 2, HaveFileMissing: 1, MissingPresentOnDisk: 1, MissingFileMissing: 1}, hash)
	assert.EqualValues(t, 1, f.Signals[database.SignalFingerprintDuration].Have)
	assert.EqualValues(t, 1, f.Signals[database.SignalRawTags].Have)

	// Proxy mode must not pretend to know the stripped fields.
	_, hasRaw := f.Signals[database.SignalRawFingerprint]
	assert.False(t, hasRaw, "raw_fingerprint must not be reported from memdb-shaped rows")
	assert.Contains(t, f.Unavailable, "acoustid_seg3")
	assert.Contains(t, f.Unavailable, "cover_hash")

	assert.Equal(t, 3, body.Data.Books.PrimaryBooks)
	assert.NotEmpty(t, body.Data.Books.EmbeddingError, "a nil embedding store must be reported, not read as zero coverage")
}

func TestHandleGetSignalCoverage_NilStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := &Server{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/signals/coverage", nil)
	srv.handleGetSignalCoverage(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
