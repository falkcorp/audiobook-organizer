// file: internal/server/handlers/system/handler_file_intro_stats_test.go
// version: 1.0.0
// guid: c58e1f07-3a94-4d62-b0e7-94f2d6a8b315
// last-edited: 2026-10-23

package system_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	systemmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileIntroStatsStore is a SystemStore that also holds the per-file intro
// counters, the way *database.PebbleStore does.
type fileIntroStatsStore struct {
	*systemmocks.MockSystemStore
	stats *database.FileIntroStats
	err   error
}

func (s *fileIntroStatsStore) PutFileIntroStats(*database.FileIntroStats) error { return nil }
func (s *fileIntroStatsStore) GetFileIntroStats() (*database.FileIntroStats, error) {
	return s.stats, s.err
}

func serveFileIntroStats(t *testing.T, store system.SystemStore) (map[string]json.RawMessage, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := system.New(func() system.SystemStore { return store }, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	w := run(http.MethodGet, "/stats", "/stats", nil, func(r *gin.Engine) {
		r.GET("/stats", h.GetFileIntroStats)
	})
	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp, w.Code
}

func TestGetFileIntroStats(t *testing.T) {
	t.Run("returns the counters", func(t *testing.T) {
		resp, code := serveFileIntroStats(t, &fileIntroStatsStore{
			MockSystemStore: &systemmocks.MockSystemStore{},
			stats:           &database.FileIntroStats{RunOpID: "run-1", Eligible: 10, TranscribedNow: 4},
		})
		require.Equal(t, http.StatusOK, code)
		var got database.FileIntroStats
		require.NoError(t, json.Unmarshal(resp["data"], &got))
		assert.Equal(t, "run-1", got.RunOpID)
		assert.Equal(t, 10, got.Eligible)
		assert.Equal(t, 4, got.TranscribedNow)
	})
	t.Run("null when no run has written them", func(t *testing.T) {
		resp, code := serveFileIntroStats(t, &fileIntroStatsStore{MockSystemStore: &systemmocks.MockSystemStore{}})
		require.Equal(t, http.StatusOK, code)
		assert.JSONEq(t, "null", string(resp["data"]))
	})
	t.Run("null when the store has no such capability", func(t *testing.T) {
		resp, code := serveFileIntroStats(t, &systemmocks.MockSystemStore{})
		require.Equal(t, http.StatusOK, code)
		assert.JSONEq(t, "null", string(resp["data"]))
	})
	t.Run("500 on a read error", func(t *testing.T) {
		_, code := serveFileIntroStats(t, &fileIntroStatsStore{MockSystemStore: &systemmocks.MockSystemStore{}, err: errors.New("disk")})
		assert.Equal(t, http.StatusInternalServerError, code)
	})
}
