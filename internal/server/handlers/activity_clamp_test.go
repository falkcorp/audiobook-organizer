// file: internal/server/handlers/activity_clamp_test.go
// version: 1.0.2
// guid: 3b9e6f12-7c4a-4d85-9a10-e2f5c8d71b46
// last-edited: 2026-10-04

package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func postClamp(t *testing.T, svc handlers.ActivityService, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/activity/clamp-summaries", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handlers.NewActivityHandler(svc, nil).ClampActivitySummaries(c)
	var resp struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp.Data
}

// A clamp that COMMITTED but whose vacuum or WAL reset failed is a successful
// clamp with the freed space still held: 200, the real counts, and
// space_still_held + vacuum_error so the caller knows the space is not back on
// disk. It used to be a generic 500, which told the caller nothing was done.
func TestClampActivitySummaries_VacuumFailureAfterCommitIs200WithSpaceStillHeld(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)
	res := database.ClampSummariesResult{Scanned: 40, Clamped: 12, BytesBefore: 9000, BytesAfter: 3000}
	verr := fmt.Errorf("%w: %w", activity.ErrClampVacuumFailed,
		errors.New("sql_activity: vacuum succeeded but WAL truncate failed (space still held): checkpoint still busy"))
	svc.EXPECT().ClampSummaries(mock.Anything, 0, false, true).Return(res, verr)

	w, data := postClamp(t, svc, `{"apply":true,"vacuum":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.EqualValues(t, 12, data["clamped"])
	assert.EqualValues(t, 40, data["scanned"])
	assert.EqualValues(t, 6000, data["bytes_reclaimed"])
	assert.Equal(t, true, data["space_still_held"])
	assert.Contains(t, data["vacuum_error"], "space is still held")
	// The raw error (which can name files) must not reach the client.
	assert.NotContains(t, data["vacuum_error"], "checkpoint still busy")
	assert.NotContains(t, data["vacuum_error"], "sql_activity")
}

// A clean run reports space_still_held=false and no vacuum_error.
func TestClampActivitySummaries_CleanRunReportsSpaceReleased(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)
	svc.EXPECT().ClampSummaries(mock.Anything, 0, false, true).
		Return(database.ClampSummariesResult{Scanned: 5, Clamped: 1}, nil)

	w, data := postClamp(t, svc, `{"apply":true,"vacuum":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, false, data["space_still_held"])
	assert.NotContains(t, data, "vacuum_error")
}

// Any other error is still a 500: only a committed-then-vacuum failure is
// downgraded.
func TestClampActivitySummaries_ClampFailureIsStill500(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)
	svc.EXPECT().ClampSummaries(mock.Anything, 0, false, true).
		Return(database.ClampSummariesResult{}, errors.New("sql_activity: clamp: disk I/O error"))

	w, _ := postClamp(t, svc, `{"apply":true,"vacuum":true}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
}

// The 200 space_still_held path must still leave a warning in the log, with
// the real error, sanitized: the response carries only a fixed message, so the
// log is the only place the reason survives.
func TestClampActivitySummaries_VacuumFailureAfterCommitLogsAWarning(t *testing.T) {
	logs := captureSlog(t)
	svc := handlersmocks.NewMockActivityService(t)
	res := database.ClampSummariesResult{Scanned: 40, Clamped: 12}
	// A newline in the cause stands in for a forged log line from a file name.
	verr := fmt.Errorf("%w: %w", activity.ErrClampVacuumFailed,
		errors.New("WAL not reset after 8 attempts\nforged-line level=ERROR"))
	svc.EXPECT().ClampSummaries(mock.Anything, 0, false, true).Return(res, verr)

	w, _ := postClamp(t, svc, `{"apply":true,"vacuum":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	out := logs.String()
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "space still held")
	assert.Contains(t, out, "scanned=40 clamped=12")
	assert.Contains(t, out, "WAL not reset after 8 attempts")
	assert.NotContains(t, out, "\nforged-line", "the error must be sanitized before it is logged")
}
