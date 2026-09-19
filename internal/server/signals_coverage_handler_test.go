// file: internal/server/signals_coverage_handler_test.go
// version: 1.2.0
// guid: 9dc1c17f-e225-48b7-a3c4-79f0246d7c9c
// last-edited: 2026-09-19

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
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

// TestHandleGetSignalCoverage_ConcurrentDeepScanIs409: a deep request that
// arrives while another deep scan holds the store's slot gets 409 Conflict
// (ErrDeepCoverageBusy), not a second multi-minute Pebble scan and not a 500.
func TestHandleGetSignalCoverage_ConcurrentDeepScanIs409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	srv := &Server{store: store}

	getDeep := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/signals/coverage?deep=true", nil)
		srv.handleGetSignalCoverage(c)
		return w
	}

	// Hold the slot the way an in-flight deep scan does.
	require.True(t, store.TryAcquireDeepCoverageScan())
	w := getDeep()
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "already running")

	// With the slot free the same request succeeds, so the 409 came from the
	// guard and the guard was not left held by the refused request.
	store.ReleaseDeepCoverageScan()
	w = getDeep()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// windows=true adds the fpwin census from the Pebble store: keys only by
// default, currency with deep=true. The criteria come from internal/fingerprint,
// and a server with no tool registry says currency is pipeline-only rather
// than calling every window stale.
func TestHandleGetSignalCoverage_WindowsCensus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	book, err := st.CreateBook(&database.Book{Title: "Win", FilePath: "/test/win"})
	require.NoError(t, err)
	withWin := &database.BookFile{BookID: book.ID, FilePath: "/test/win/a.m4b", Format: "m4b"}
	bare := &database.BookFile{BookID: book.ID, FilePath: "/test/win/b.m4b", Format: "m4b"}
	require.NoError(t, st.CreateBookFile(withWin))
	require.NoError(t, st.CreateBookFile(bare))
	require.NoError(t, st.PutFingerprintWindow(&database.FingerprintWindow{
		Ref: database.FileWindowRef(withWin.ID), Kind: database.WindowKindWindow, SlotBP: 5000,
		WindowSet: fingerprint.WindowSetWS1, Pipeline: fingerprint.WindowPipelineID,
		Raw: []byte{1, 2, 3, 4}, FpcalcVersion: "1.6.0", FFmpegVersion: "8.0.1",
	}))
	st.WaitForWarmup()
	require.True(t, st.IsMemReady())
	srv := &Server{store: st}

	get := func(query string) SignalCoverageResponse {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodGet, "/api/v1/signals/coverage?"+query, nil)
		srv.handleGetSignalCoverage(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body struct {
			Data SignalCoverageResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body.Data
	}

	assert.Nil(t, get("").Windows, "the window census is opt-in")

	fast := get("windows=true").Windows
	require.NotNil(t, fast)
	assert.False(t, fast.CurrencyEvaluated)
	assert.EqualValues(t, 2, fast.PresentFiles)
	assert.EqualValues(t, 1, fast.WithWindows)
	assert.EqualValues(t, 1, fast.NoWindows)

	deep := get("windows=true&deep=true").Windows
	require.NotNil(t, deep)
	assert.True(t, deep.CurrencyEvaluated)
	assert.EqualValues(t, 1, deep.WithCurrentWindows)
	require.NotNil(t, deep.Criteria)
	assert.Equal(t, fingerprint.WindowPipelineID, deep.Criteria.Pipeline)
	assert.Contains(t, deep.Unavailable["tool_version_currency"], "tool registry not configured")
}
