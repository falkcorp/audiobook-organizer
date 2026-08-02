// file: internal/server/server_coverage_phase2_test.go
// version: 1.3.0
// guid: d5e6f7a8-b9c0-1d2e-3f4a-5b6c7d8e9f0a
// last-edited: 2026-07-03

package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// pinEmptyRootDir pins config.AppConfig.RootDir to "" for the duration of a
// MockStore-based test. The itunes/deluge plugin Build guards go LIVE whenever
// the ambient RootDir is non-empty and then call UpsertOpDefinitionV2 on the
// mock (no such expectation → testify FailNow). RootDir can be non-empty here
// through cross-test — and, under `go test -count=N`, cross-iteration —
// config pollution, so these tests must not trust ambient state.
func pinEmptyRootDir(t *testing.T) {
	t.Helper()
	origCfg := config.AppConfig
	config.AppConfig.RootDir = ""
	t.Cleanup(func() { config.AppConfig = origCfg })
}

// TestGetAudiobookTagsErrors tests error scenarios for getAudiobookTags (59.3% → target 70%+)
func TestGetAudiobookTagsErrors(t *testing.T) {
	pinEmptyRootDir(t)
	tests := []struct {
		name       string
		bookID     string
		mockSetup  func(*mocks.MockStore)
		statusCode int
	}{
		{
			name:   "database error",
			bookID: "01HQWKV1234567890ABCDEFGHJK",
			mockSetup: func(m *mocks.MockStore) {
				m.EXPECT().SetRootDir(mock.Anything).Return()
				m.EXPECT().GetBookByID("01HQWKV1234567890ABCDEFGHJK").Return(nil, errors.New("database connection error")).Once()
			},
			statusCode: http.StatusInternalServerError,
		},
		{
			name:   "book not found",
			bookID: "01HQWKV9999999999999999999",
			mockSetup: func(m *mocks.MockStore) {
				m.EXPECT().SetRootDir(mock.Anything).Return()
				m.EXPECT().GetBookByID("01HQWKV9999999999999999999").Return(nil, nil).Once()
			},
			statusCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			mockStore := mocks.NewMockStore(t)
			tt.mockSetup(mockStore)

			oldStore := database.GetGlobalStore()
			database.SetGlobalStore(mockStore)
			defer func() { database.SetGlobalStore(oldStore) }()

			srv := NewServer(mockStore)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+tt.bookID+"/tags", nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)

			assert.Equal(t, tt.statusCode, w.Code)
			mockStore.AssertExpectations(t)
		})
	}
}

// TestAddBlockedHashErrors tests error scenarios for addBlockedHash (60.0% → target 70%+)
func TestAddBlockedHashErrors(t *testing.T) {
	pinEmptyRootDir(t)
	tests := []struct {
		name       string
		body       string
		statusCode int
		errMsg     string
	}{
		{
			name:       "invalid JSON",
			body:       `{"invalid": json}`,
			statusCode: http.StatusBadRequest,
			errMsg:     "invalid",
		},
		{
			name:       "missing hash field",
			body:       `{"reason": "duplicate"}`,
			statusCode: http.StatusBadRequest,
			errMsg:     "Hash",
		},
		{
			name:       "missing reason field",
			body:       `{"hash": "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}`,
			statusCode: http.StatusBadRequest,
			errMsg:     "Reason",
		},
		{
			name:       "hash too short",
			body:       `{"hash": "abc123", "reason": "duplicate"}`,
			statusCode: http.StatusBadRequest,
			errMsg:     "64 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			mockStore := mocks.NewMockStore(t)
			mockStore.EXPECT().SetRootDir(mock.Anything).Return()

			oldStore := database.GetGlobalStore()
			database.SetGlobalStore(mockStore)
			defer func() { database.SetGlobalStore(oldStore) }()

			srv := NewServer(mockStore)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/blocked-hashes", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)

			assert.Equal(t, tt.statusCode, w.Code)
			if tt.errMsg != "" {
				assert.Contains(t, w.Body.String(), tt.errMsg)
			}
			mockStore.AssertExpectations(t)
		})
	}
}

// TestAddBlockedHashDatabaseError tests database failure in addBlockedHash
func TestAddBlockedHashDatabaseError(t *testing.T) {
	pinEmptyRootDir(t)
	gin.SetMode(gin.TestMode)
	mockStore := mocks.NewMockStore(t)

	validHash := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	mockStore.EXPECT().SetRootDir(mock.Anything).Return()
	mockStore.EXPECT().
		AddBlockedHash(validHash, "duplicate file").
		Return(errors.New("database error: constraint violation")).
		Once()

	oldStore := database.GetGlobalStore()
	database.SetGlobalStore(mockStore)
	defer func() { database.SetGlobalStore(oldStore) }()

	srv := NewServer(mockStore)

	body := `{"hash": "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", "reason": "duplicate file"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blocked-hashes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockStore.AssertExpectations(t)
}

// TestDeleteWorkErrors tests error scenarios for deleteWork (63.6% → target 75%+)
func TestDeleteWorkErrors(t *testing.T) {
	pinEmptyRootDir(t)
	tests := []struct {
		name       string
		workID     string
		mockSetup  func(*mocks.MockStore)
		statusCode int
	}{
		{
			name:   "database error",
			workID: "01HQWKV1234567890ABCDEFGHJK",
			mockSetup: func(m *mocks.MockStore) {
				m.EXPECT().SetRootDir(mock.Anything).Return()
				// WorkService.DeleteWork checks existence first, then deletes.
				m.EXPECT().GetWorkByID("01HQWKV1234567890ABCDEFGHJK").Return(&database.Work{ID: "01HQWKV1234567890ABCDEFGHJK", Title: "Work"}, nil).Once()
				m.EXPECT().DeleteWork("01HQWKV1234567890ABCDEFGHJK").Return(errors.New("database connection error")).Once()
			},
			statusCode: http.StatusInternalServerError,
		},
		{
			name:   "work not found",
			workID: "01HQWKV9999999999999999999",
			mockSetup: func(m *mocks.MockStore) {
				m.EXPECT().SetRootDir(mock.Anything).Return()
				// WorkService.DeleteWork checks existence; nil work → returns "work not found" without calling DeleteWork.
				m.EXPECT().GetWorkByID("01HQWKV9999999999999999999").Return(nil, nil).Once()
			},
			statusCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			mockStore := mocks.NewMockStore(t)
			tt.mockSetup(mockStore)

			oldStore := database.GetGlobalStore()
			database.SetGlobalStore(mockStore)
			defer func() { database.SetGlobalStore(oldStore) }()

			srv := NewServer(mockStore)

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/works/"+tt.workID, nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)

			assert.Equal(t, tt.statusCode, w.Code)
			mockStore.AssertExpectations(t)
		})
	}
}
