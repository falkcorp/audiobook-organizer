// file: internal/server/service_layer_test.go
// version: 1.13.4
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-11-09

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/util"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/stretchr/testify/mock"
)

// TestAudiobookUpdateService_ExtractBoolField tests ExtractBoolField method
func TestAudiobookUpdateService_ExtractBoolField(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]any
		key       string
		wantValue bool
		wantOK    bool
	}{
		{
			name:      "extract true",
			payload:   map[string]any{"verified": true},
			key:       "verified",
			wantValue: true,
			wantOK:    true,
		},
		{
			name:      "extract false",
			payload:   map[string]any{"verified": false},
			key:       "verified",
			wantValue: false,
			wantOK:    true,
		},
		{
			name:      "missing key",
			payload:   map[string]any{"other": true},
			key:       "verified",
			wantValue: false,
			wantOK:    false,
		},
		{
			name:      "wrong type (string instead of bool)",
			payload:   map[string]any{"verified": "true"},
			key:       "verified",
			wantValue: false,
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := util.ExtractBoolField(tt.payload, tt.key)
			if value != tt.wantValue {
				t.Errorf("expected value %v, got %v", tt.wantValue, value)
			}
			if ok != tt.wantOK {
				t.Errorf("expected ok %v, got %v", tt.wantOK, ok)
			}
		})
	}
}

// TestAudiobookUpdateService_ExtractOverrides_EdgeCases tests edge cases for ExtractOverrides
func TestAudiobookUpdateService_ExtractOverrides_EdgeCases(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookUpdateService(mockStore)

	tests := []struct {
		name      string
		payload   map[string]any
		wantOK    bool
		wantCount int
	}{
		{
			name:      "missing overrides key",
			payload:   map[string]any{"other": "value"},
			wantOK:    false,
			wantCount: 0,
		},
		{
			name:      "wrong type (not a map)",
			payload:   map[string]any{"overrides": "not a map"},
			wantOK:    false,
			wantCount: 0,
		},
		{
			name:      "empty overrides map",
			payload:   map[string]any{"overrides": map[string]any{}},
			wantOK:    true,
			wantCount: 0,
		},
		{
			name: "valid overrides with multiple fields",
			payload: map[string]any{
				"overrides": map[string]any{
					"title":  "value1",
					"author": "value2",
					"year":   "value3",
				},
			},
			wantOK:    true,
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := svc.ExtractOverrides(tt.payload)
			if ok != tt.wantOK {
				t.Errorf("expected ok %v, got %v", tt.wantOK, ok)
			}
			if ok && len(value) != tt.wantCount {
				t.Errorf("expected %d overrides, got %d", tt.wantCount, len(value))
			}
		})
	}
}

// TestImportPathService_ValidatePath_Whitespace tests whitespace handling
func TestImportPathService_ValidatePath_Whitespace(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := importer.NewImportPathService(mockStore)

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "valid path",
			path:    "/test/path",
			wantErr: false,
		},
		{
			name:    "empty path returns error",
			path:    "",
			wantErr: true,
		},
		{
			name:    "whitespace only path returns error",
			path:    "   ",
			wantErr: true,
		},
		{
			name:    "tab only path returns error",
			path:    "\t\t",
			wantErr: true,
		},
		{
			name:    "path with leading/trailing spaces is valid",
			path:    "  /test/path  ",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.ValidatePath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				if err != importer.ErrImportPathEmpty {
					t.Errorf("expected importer.ErrImportPathEmpty, got %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestSystemService_FilterLogsBySearch_CaseInsensitive tests case insensitivity
func TestSystemService_FilterLogsBySearch_CaseInsensitive(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := sysinfo.NewSystemService(mockStore, "test", nil)

	logs := []database.OperationLog{
		{Message: "Starting SCAN operation"},
		{Message: "Found 10 audiobooks"},
		{Message: "Error processing file"},
	}

	tests := []struct {
		name       string
		searchTerm string
		wantCount  int
	}{
		{
			name:       "uppercase search term",
			searchTerm: "SCAN",
			wantCount:  1,
		},
		{
			name:       "lowercase search term",
			searchTerm: "scan",
			wantCount:  1,
		},
		{
			name:       "mixed case search term",
			searchTerm: "ScAn",
			wantCount:  1,
		},
		{
			name:       "empty search returns all",
			searchTerm: "",
			wantCount:  3,
		},
		{
			name:       "no matches",
			searchTerm: "xyz",
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := svc.FilterLogsBySearch(logs, tt.searchTerm)
			if len(result) != tt.wantCount {
				t.Errorf("expected %d results, got %d", tt.wantCount, len(result))
			}
		})
	}
}

// TestSystemService_SortLogsByTimestamp_EdgeCases tests edge cases for sorting
func TestSystemService_SortLogsByTimestamp_EdgeCases(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := sysinfo.NewSystemService(mockStore, "test", nil)

	now := time.Now()

	tests := []struct {
		name  string
		logs  []database.OperationLog
		check func(t *testing.T, result []database.OperationLog)
	}{
		{
			name: "already sorted logs remain sorted",
			logs: []database.OperationLog{
				{Message: "Third", CreatedAt: now.Add(2 * time.Hour)},
				{Message: "Second", CreatedAt: now.Add(1 * time.Hour)},
				{Message: "First", CreatedAt: now},
			},
			check: func(t *testing.T, result []database.OperationLog) {
				if result[0].Message != "Third" {
					t.Errorf("expected first message 'Third', got %q", result[0].Message)
				}
			},
		},
		{
			name: "identical timestamps",
			logs: []database.OperationLog{
				{Message: "A", CreatedAt: now},
				{Message: "B", CreatedAt: now},
				{Message: "C", CreatedAt: now},
			},
			check: func(t *testing.T, result []database.OperationLog) {
				if len(result) != 3 {
					t.Errorf("expected 3 logs, got %d", len(result))
				}
			},
		},
		{
			name: "empty logs",
			logs: []database.OperationLog{},
			check: func(t *testing.T, result []database.OperationLog) {
				if len(result) != 0 {
					t.Errorf("expected 0 logs, got %d", len(result))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := svc.SortLogsByTimestamp(tt.logs)
			tt.check(t, result)
		})
	}
}

// TestSystemService_PaginateLogs_EdgeCases tests edge cases for pagination
func TestSystemService_PaginateLogs_EdgeCases(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := sysinfo.NewSystemService(mockStore, "test", nil)

	logs := make([]database.OperationLog, 50)
	for i := range 50 {
		logs[i] = database.OperationLog{Message: "Log"}
	}

	tests := []struct {
		name      string
		logs      []database.OperationLog
		page      int
		pageSize  int
		wantCount int
	}{
		{
			name:      "negative page defaults to 1",
			logs:      logs,
			page:      -1,
			pageSize:  20,
			wantCount: 20,
		},
		{
			name:      "zero page defaults to 1",
			logs:      logs,
			page:      0,
			pageSize:  20,
			wantCount: 20,
		},
		{
			name:      "negative pageSize defaults to 20",
			logs:      logs,
			page:      1,
			pageSize:  -1,
			wantCount: 20,
		},
		{
			name:      "zero pageSize defaults to 20",
			logs:      logs,
			page:      1,
			pageSize:  0,
			wantCount: 20,
		},
		{
			name:      "page beyond range returns empty",
			logs:      logs,
			page:      100,
			pageSize:  20,
			wantCount: 0,
		},
		{
			name:      "last page partial results",
			logs:      logs,
			page:      3,
			pageSize:  20,
			wantCount: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := svc.PaginateLogs(tt.logs, tt.page, tt.pageSize)
			if len(result) != tt.wantCount {
				t.Errorf("expected %d logs, got %d", tt.wantCount, len(result))
			}
		})
	}
}

// TestSystemService_GetFormattedUptime tests uptime formatting
func TestSystemService_GetFormattedUptime(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := sysinfo.NewSystemService(mockStore, "test", nil)

	tests := []struct {
		name      string
		startTime time.Time
		check     func(t *testing.T, uptime string)
	}{
		{
			name:      "very recent start time",
			startTime: time.Now(),
			check: func(t *testing.T, uptime string) {
				if uptime == "" {
					t.Error("expected non-empty uptime string")
				}
			},
		},
		{
			name:      "one hour ago",
			startTime: time.Now().Add(-1 * time.Hour),
			check: func(t *testing.T, uptime string) {
				if !contains(uptime, "h") && !contains(uptime, "m") {
					t.Errorf("expected uptime to contain hours or minutes, got %q", uptime)
				}
			},
		},
		{
			name:      "one day ago",
			startTime: time.Now().Add(-24 * time.Hour),
			check: func(t *testing.T, uptime string) {
				if !contains(uptime, "h") {
					t.Errorf("expected uptime to contain hours, got %q", uptime)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uptime := svc.GetFormattedUptime(tt.startTime)
			tt.check(t, uptime)
		})
	}
}

// TestApplyOverrideToPayload tests applyOverrideToPayload function
func TestApplyOverrideToPayload(t *testing.T) {
	t.Run("title", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "title", "New Title")
		if payload.Title != "New Title" {
			t.Errorf("expected 'New Title', got %q", payload.Title)
		}
	})

	t.Run("author_name", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "author_name", "Author")
		if payload.AuthorName == nil || *payload.AuthorName != "Author" {
			t.Errorf("expected 'Author', got %v", payload.AuthorName)
		}
	})

	t.Run("series_name", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "series_name", "Series")
		if payload.SeriesName == nil || *payload.SeriesName != "Series" {
			t.Errorf("expected 'Series', got %v", payload.SeriesName)
		}
	})

	t.Run("narrator", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "narrator", "Narrator")
		if payload.Narrator == nil || *payload.Narrator != "Narrator" {
			t.Errorf("expected 'Narrator', got %v", payload.Narrator)
		}
	})

	t.Run("publisher", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "publisher", "Pub")
		if payload.Publisher == nil || *payload.Publisher != "Pub" {
			t.Errorf("expected 'Pub', got %v", payload.Publisher)
		}
	})

	t.Run("language", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "language", "en")
		if payload.Language == nil || *payload.Language != "en" {
			t.Errorf("expected 'en', got %v", payload.Language)
		}
	})

	t.Run("audiobook_release_year float64", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "audiobook_release_year", float64(2023))
		if payload.AudiobookReleaseYear == nil || *payload.AudiobookReleaseYear != 2023 {
			t.Errorf("expected 2023, got %v", payload.AudiobookReleaseYear)
		}
	})

	t.Run("audiobook_release_year int", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "audiobook_release_year", 2024)
		if payload.AudiobookReleaseYear == nil || *payload.AudiobookReleaseYear != 2024 {
			t.Errorf("expected 2024, got %v", payload.AudiobookReleaseYear)
		}
	})

	t.Run("isbn10", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "isbn10", "1234567890")
		if payload.ISBN10 == nil || *payload.ISBN10 != "1234567890" {
			t.Errorf("expected '1234567890', got %v", payload.ISBN10)
		}
	})

	t.Run("isbn13", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "isbn13", "9781234567890")
		if payload.ISBN13 == nil || *payload.ISBN13 != "9781234567890" {
			t.Errorf("expected '9781234567890', got %v", payload.ISBN13)
		}
	})

	t.Run("wrong type is no-op", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "title", 123)
		if payload.Title != "" {
			t.Errorf("expected empty title, got %q", payload.Title)
		}
	})

	t.Run("unknown field is no-op", func(t *testing.T) {
		payload := &AudiobookUpdate{Book: &database.Book{}}
		applyOverrideToPayload(payload, "unknown_field", "value")
		// Should not panic or change anything
	})
}

// TestMetadataStateService_UpdateFetchedMetadata tests UpdateFetchedMetadata method
func TestMetadataStateService_UpdateFetchedMetadata(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	// Every save now retires the pre-migration user-preference blob, so an
	// unlock cannot be undone by the next read falling back to it. Whether a
	// given case reaches the delete depends on how far the save gets, so this
	// is Maybe rather than an ordered expectation.
	mockStore.EXPECT().DeleteUserPreference(mock.Anything).Return(nil).Maybe()
	svc := metafetch.NewMetadataStateService(mockStore)

	bookID := "01HXZABC123456789"

	tests := []struct {
		name      string
		bookID    string
		values    map[string]any
		setupMock func()
		wantErr   bool
		errSubstr string
	}{
		{
			name:   "update fetched metadata for new book",
			bookID: bookID,
			values: map[string]any{
				"title":  "Test Title",
				"author": "Test Author",
			},
			setupMock: func() {
				// First call in LoadMetadataState (in UpdateFetchedMetadata)
				mockStore.On("GetMetadataFieldStates", bookID).Return([]database.MetadataFieldState{}, nil).Once()
				// Mock GetUserPreference for legacy state check (returns nil to indicate no legacy state)
				mockStore.On("GetUserPreference", "metadata_state_"+bookID).Return(nil, nil).Once()
				// RecordMetadataChange calls from recordChange helper
				mockStore.On("RecordMetadataChange", mock.AnythingOfType("*store.MetadataChangeRecord")).Return(nil)
				// Second call in SaveMetadataState (in UpdateFetchedMetadata)
				mockStore.On("GetMetadataFieldStates", bookID).Return([]database.MetadataFieldState{}, nil).Once()
				mockStore.On("UpsertMetadataFieldState", matchMetadataFieldState(bookID, "title")).Return(nil).Once()
				mockStore.On("UpsertMetadataFieldState", matchMetadataFieldState(bookID, "author")).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:   "update fetched metadata for existing book",
			bookID: bookID,
			values: map[string]any{
				"narrator": "New Narrator",
			},
			setupMock: func() {
				existingState := []database.MetadataFieldState{
					{
						BookID:       bookID,
						Field:        "title",
						FetchedValue: new(`"Old Title"`),
						UpdatedAt:    time.Now().Add(-24 * time.Hour),
					},
				}
				// First call in LoadMetadataState
				mockStore.On("GetMetadataFieldStates", bookID).Return(existingState, nil).Once()
				// RecordMetadataChange calls from recordChange helper
				mockStore.On("RecordMetadataChange", mock.AnythingOfType("*store.MetadataChangeRecord")).Return(nil)
				// Second call in SaveMetadataState
				mockStore.On("GetMetadataFieldStates", bookID).Return(existingState, nil).Once()
				mockStore.On("UpsertMetadataFieldState", matchMetadataFieldState(bookID, "narrator")).Return(nil).Once()
				mockStore.On("UpsertMetadataFieldState", matchMetadataFieldState(bookID, "title")).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:   "error loading metadata state",
			bookID: bookID,
			values: map[string]any{
				"title": "Test",
			},
			setupMock: func() {
				mockStore.On("GetMetadataFieldStates", bookID).Return(nil, errors.New("database error")).Once()
			},
			wantErr:   true,
			errSubstr: "",
		},
		{
			name:   "empty values map",
			bookID: bookID,
			values: map[string]any{},
			setupMock: func() {
				// First call in LoadMetadataState
				mockStore.On("GetMetadataFieldStates", bookID).Return([]database.MetadataFieldState{}, nil).Once()
				mockStore.On("GetUserPreference", "metadata_state_"+bookID).Return(nil, nil).Once()
				// Second call in SaveMetadataState
				mockStore.On("GetMetadataFieldStates", bookID).Return([]database.MetadataFieldState{}, nil).Once()
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupMock != nil {
				tt.setupMock()
			}

			err := svc.UpdateFetchedMetadata(tt.bookID, tt.values)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errSubstr != "" && !contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error to contain %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			mockStore.AssertExpectations(t)
		})
	}
}

// matchMetadataFieldState is a helper to match MetadataFieldState in mock calls
func matchMetadataFieldState(bookID, field string) any {
	return mock.MatchedBy(func(state *database.MetadataFieldState) bool {
		return state.BookID == bookID && state.Field == field
	})
}

// TestImportPathService_UpdateImportPathEnabled tests UpdateImportPathEnabled method
func TestImportPathService_UpdateImportPathEnabled(t *testing.T) {
	t.Run("success case", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := importer.NewImportPathService(mockStore)

		importPath := &database.ImportPath{
			ID:      1,
			Path:    "/test/path",
			Name:    "Test Path",
			Enabled: false,
		}

		mockStore.EXPECT().GetImportPathByID(1).Return(importPath, nil)
		mockStore.EXPECT().UpdateImportPath(1, &database.ImportPath{
			ID:      1,
			Path:    "/test/path",
			Name:    "Test Path",
			Enabled: true,
		}).Return(nil)

		err := svc.UpdateImportPathEnabled(1, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("import path not found", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := importer.NewImportPathService(mockStore)

		mockStore.EXPECT().GetImportPathByID(999).Return(nil, nil)

		err := svc.UpdateImportPathEnabled(999, true)
		if err == nil {
			t.Error("expected error, got nil")
		}
		if !contains(err.Error(), "import path not found") {
			t.Errorf("expected 'import path not found' error, got %v", err)
		}
	})

	t.Run("database error on get", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := importer.NewImportPathService(mockStore)

		mockStore.EXPECT().GetImportPathByID(1).Return(nil, errors.New("database error"))

		err := svc.UpdateImportPathEnabled(1, true)
		if err == nil {
			t.Error("expected error, got nil")
		}
		if !contains(err.Error(), "import path not found") {
			t.Errorf("expected 'import path not found' error, got %v", err)
		}
	})
}

// TestImportPathService_GetImportPath tests GetImportPath method
func TestImportPathService_GetImportPath(t *testing.T) {
	t.Run("success case", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := importer.NewImportPathService(mockStore)

		expectedPath := &database.ImportPath{
			ID:      1,
			Path:    "/test/path",
			Name:    "Test Path",
			Enabled: true,
		}

		mockStore.EXPECT().GetImportPathByID(1).Return(expectedPath, nil)

		result, err := svc.GetImportPath(1)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if result.ID != expectedPath.ID {
			t.Errorf("expected ID %d, got %d", expectedPath.ID, result.ID)
		}
		if result.Path != expectedPath.Path {
			t.Errorf("expected path %q, got %q", expectedPath.Path, result.Path)
		}
	})

	t.Run("import path not found", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := importer.NewImportPathService(mockStore)

		mockStore.EXPECT().GetImportPathByID(999).Return(nil, nil)

		result, err := svc.GetImportPath(999)
		if err == nil {
			t.Error("expected error, got nil")
		}
		if result != nil {
			t.Errorf("expected nil result, got %v", result)
		}
		if !contains(err.Error(), "import path not found") {
			t.Errorf("expected 'import path not found' error, got %v", err)
		}
	})

	t.Run("database error", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := importer.NewImportPathService(mockStore)

		mockStore.EXPECT().GetImportPathByID(1).Return(nil, errors.New("database error"))

		result, err := svc.GetImportPath(1)
		if err == nil {
			t.Error("expected error, got nil")
		}
		if result != nil {
			t.Errorf("expected nil result, got %v", result)
		}
	})
}

// TestAudiobookService_GetSoftDeletedBooks_Error tests error path
func TestAudiobookService_GetSoftDeletedBooks_Error(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().ListSoftDeletedBooks(50, 0, (*time.Time)(nil)).Return(nil, errors.New("database error"))

	books, err := svc.GetSoftDeletedBooks(context.Background(), 50, 0, nil)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if books != nil {
		t.Errorf("expected nil books on error, got %v", books)
	}
	if !contains(err.Error(), "database error") {
		t.Errorf("expected 'database error', got %v", err)
	}
}

// TestAudiobookService_GetSoftDeletedBooks_WithDaysFilter tests olderThanDays parameter
func TestAudiobookService_GetSoftDeletedBooks_WithDaysFilter(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	days := 30
	mockStore.EXPECT().ListSoftDeletedBooks(50, 0, mock.MatchedBy(func(cutoff *time.Time) bool {
		return cutoff != nil
	})).Return([]database.Book{}, nil)

	books, err := svc.GetSoftDeletedBooks(context.Background(), 50, 0, &days)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if books == nil {
		t.Error("expected non-nil books slice")
	}
}

// TestAudiobookService_CountAudiobooks_Error tests error path
func TestAudiobookService_CountAudiobooks_Error(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().CountPrimaryBooks().Return(0, errors.New("database error"))

	count, err := svc.CountAudiobooks(context.Background())
	if err == nil {
		t.Error("expected error, got nil")
	}
	if count != 0 {
		t.Errorf("expected 0 count on error, got %d", count)
	}
	if !contains(err.Error(), "database error") {
		t.Errorf("expected 'database error', got %v", err)
	}
}

// TestAudiobookService_CountAudiobooks_NilDB tests nil database
func TestAudiobookService_CountAudiobooks_NilDB(t *testing.T) {
	svc := NewAudiobookService(nil)

	count, err := svc.CountAudiobooks(context.Background())
	if err == nil {
		t.Error("expected error, got nil")
	}
	if count != 0 {
		t.Errorf("expected 0 count on error, got %d", count)
	}
	if !contains(err.Error(), "database not initialized") {
		t.Errorf("expected 'database not initialized', got %v", err)
	}
}

// TestAudiobookService_GetSoftDeletedBooks_NilDB tests nil database
func TestAudiobookService_GetSoftDeletedBooks_NilDB(t *testing.T) {
	svc := NewAudiobookService(nil)

	books, err := svc.GetSoftDeletedBooks(context.Background(), 50, 0, nil)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if books != nil {
		t.Errorf("expected nil books on error, got %v", books)
	}
	if !contains(err.Error(), "database not initialized") {
		t.Errorf("expected 'database not initialized', got %v", err)
	}
}

// TestDashboardService_GetHealthCheckResponse_Degraded tests degraded status
func TestDashboardService_GetHealthCheckResponse_Degraded(t *testing.T) {
	// Use nil db to trigger the error path in CollectDashboardMetrics
	// (individual DB call errors are swallowed, only nil db returns error)
	svc := sysinfo.NewDashboardService(nil)

	resp := svc.GetHealthCheckResponse("1.0.0")
	if resp.Status != "degraded" {
		t.Errorf("expected status 'degraded', got %q", resp.Status)
	}
	if resp.PartialError == "" {
		t.Error("expected PartialError to be set")
	}
	if !contains(resp.PartialError, "database not initialized") {
		t.Errorf("expected PartialError to contain 'database not initialized', got %q", resp.PartialError)
	}
	// Verify metrics are still returned (with default values)
	if resp.Metrics.Books != 0 || resp.Metrics.Authors != 0 {
		t.Error("expected zero metrics on error")
	}
}

// TestFilesystemService_CreateExclusion_EmptyPath tests empty path error
func TestFilesystemService_CreateExclusion_EmptyPath(t *testing.T) {
	svc := fileops.NewFilesystemService(nil)

	err := svc.CreateExclusion(context.Background(), "")
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !contains(err.Error(), "path is required") {
		t.Errorf("expected 'path is required', got %v", err)
	}
}

// TestFilesystemService_CreateExclusion_NotDirectory tests file path error
func TestFilesystemService_CreateExclusion_NotDirectory(t *testing.T) {
	// Create a real file so stat succeeds but it's not a directory
	tmpFile := filepath.Join(t.TempDir(), "not_a_dir.txt")
	os.WriteFile(tmpFile, []byte("test"), 0644)

	// Store must allow the path so the allow-list gate passes and the
	// not-a-directory check is what rejects it.
	mockStore := new(mocks.MockImportPathStore)
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{{Path: filepath.Dir(tmpFile)}}, nil)
	svc := fileops.NewFilesystemService(mockStore)

	err := svc.CreateExclusion(context.Background(), tmpFile)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !contains(err.Error(), "must be a directory") {
		t.Errorf("expected 'must be a directory', got %v", err)
	}
}

// TestFilesystemService_RemoveExclusion_EmptyPath tests empty path error
func TestFilesystemService_RemoveExclusion_EmptyPath(t *testing.T) {
	svc := fileops.NewFilesystemService(nil)

	err := svc.RemoveExclusion(context.Background(), "")
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !contains(err.Error(), "path is required") {
		t.Errorf("expected 'path is required', got %v", err)
	}
}
