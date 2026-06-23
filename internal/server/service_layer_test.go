// file: internal/server/service_layer_test.go
// version: 1.9.1
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-06-17

package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/util"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/sysinfo"
	"github.com/stretchr/testify/mock"
)

// TestConfigUpdateService_MaskSecrets tests MaskSecrets method
func TestConfigUpdateService_MaskSecrets(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := config.NewUpdateService(mockStore)

	tests := []struct {
		name     string
		config   config.Config
		expected config.Config
	}{
		{
			name: "mask openai api key",
			config: config.Config{
				OpenAIAPIKey: "sk-1234567890abcdef",
			},
			expected: config.Config{
				OpenAIAPIKey: "sk-****cdef",
			},
		},
		{
			name: "short secrets get masked to ****",
			config: config.Config{
				OpenAIAPIKey: "short",
			},
			expected: config.Config{
				OpenAIAPIKey: "****",
			},
		},
		{
			name: "empty secrets remain empty",
			config: config.Config{
				OpenAIAPIKey: "",
			},
			expected: config.Config{
				OpenAIAPIKey: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked := svc.MaskSecrets(tt.config)
			if masked.OpenAIAPIKey != tt.expected.OpenAIAPIKey {
				t.Errorf("expected OpenAIAPIKey %q, got %q", tt.expected.OpenAIAPIKey, masked.OpenAIAPIKey)
			}
		})
	}
}

// TestConfigUpdateService_ApplyUpdates_ErrorCases tests error cases for ApplyUpdates
func TestConfigUpdateService_ApplyUpdates_ErrorCases(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := config.NewUpdateService(mockStore)

	tests := []struct {
		name      string
		payload   map[string]any
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "nil payload returns error",
			payload:   nil,
			wantErr:   true,
			errSubstr: "configuration payload is required",
		},
		{
			name:      "database_type change rejected",
			payload:   map[string]any{"database_type": "mysql"},
			wantErr:   true,
			errSubstr: "database_type cannot be changed at runtime",
		},
		{
			name:      "enable_sqlite change rejected",
			payload:   map[string]any{"enable_sqlite": true},
			wantErr:   true,
			errSubstr: "enable_sqlite cannot be changed at runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.ApplyUpdates(tt.payload)
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
		})
	}
}

// TestConfigUpdateService_ApplyUpdates_ArrayFields tests array field updates
func TestConfigUpdateService_ApplyUpdates_ArrayFields(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	svc := config.NewUpdateService(mockStore)

	originalPatterns := config.AppConfig.ExcludePatterns
	originalExtensions := config.AppConfig.SupportedExtensions
	defer func() {
		config.AppConfig.ExcludePatterns = originalPatterns
		config.AppConfig.SupportedExtensions = originalExtensions
	}()

	payload := map[string]any{
		"exclude_patterns":     []any{"*.tmp", "*.bak", "*.cache"},
		"supported_extensions": []any{".mp3", ".m4b", ".m4a"},
	}

	err := svc.ApplyUpdates(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(config.AppConfig.ExcludePatterns) != 3 {
		t.Errorf("expected 3 exclude patterns, got %d", len(config.AppConfig.ExcludePatterns))
	}
	if config.AppConfig.ExcludePatterns[0] != "*.tmp" {
		t.Errorf("expected first pattern '*.tmp', got %q", config.AppConfig.ExcludePatterns[0])
	}

	if len(config.AppConfig.SupportedExtensions) != 3 {
		t.Errorf("expected 3 supported extensions, got %d", len(config.AppConfig.SupportedExtensions))
	}
	if config.AppConfig.SupportedExtensions[0] != ".mp3" {
		t.Errorf("expected first extension '.mp3', got %q", config.AppConfig.SupportedExtensions[0])
	}
}

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
	for i := 0; i < 50; i++ {
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

// TestConfigUpdateService_UpdateConfig tests UpdateConfig method
func TestConfigUpdateService_UpdateConfig(t *testing.T) {
	t.Run("nil db returns error", func(t *testing.T) {
		svc := &config.UpdateService{DB: nil}
		status, resp := svc.UpdateConfig(map[string]any{"root_dir": "/test"})
		if status != 500 {
			t.Errorf("expected 500, got %d", status)
		}
		if resp["error"] != "database not initialized" {
			t.Errorf("unexpected error: %v", resp["error"])
		}
	})

	t.Run("database_type rejected", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := config.NewUpdateService(mockStore)
		status, resp := svc.UpdateConfig(map[string]any{"database_type": "mysql"})
		if status != 400 {
			t.Errorf("expected 400, got %d", status)
		}
		if !contains(resp["error"].(string), "database_type") {
			t.Errorf("expected database_type error, got %v", resp["error"])
		}
	})

	t.Run("enable_sqlite rejected", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		svc := config.NewUpdateService(mockStore)
		status, resp := svc.UpdateConfig(map[string]any{"enable_sqlite": true})
		if status != 400 {
			t.Errorf("expected 400, got %d", status)
		}
		if !contains(resp["error"].(string), "enable_sqlite") {
			t.Errorf("expected enable_sqlite error, got %v", resp["error"])
		}
	})
}

// TestConfigUpdateService_ApplyUpdates_FieldTypes tests applying different field types
func TestConfigUpdateService_ApplyUpdates_FieldTypes(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	svc := config.NewUpdateService(mockStore)

	originalRootDir := config.AppConfig.RootDir
	originalAutoOrg := config.AppConfig.AutoOrganize
	originalScans := config.AppConfig.ConcurrentScans
	defer func() {
		config.AppConfig.RootDir = originalRootDir
		config.AppConfig.AutoOrganize = originalAutoOrg
		config.AppConfig.ConcurrentScans = originalScans
	}()

	err := svc.ApplyUpdates(map[string]any{
		"root_dir":         "/new/path",
		"auto_organize":    true,
		"concurrent_scans": float64(8),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.AppConfig.RootDir != "/new/path" {
		t.Errorf("expected root_dir '/new/path', got %q", config.AppConfig.RootDir)
	}
	if !config.AppConfig.AutoOrganize {
		t.Error("expected auto_organize true")
	}
	if config.AppConfig.ConcurrentScans != 8 {
		t.Errorf("expected concurrent_scans 8, got %d", config.AppConfig.ConcurrentScans)
	}
}

// TestMetadataStateService_UpdateFetchedMetadata tests UpdateFetchedMetadata method
func TestMetadataStateService_UpdateFetchedMetadata(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
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
				mockStore.On("RecordMetadataChange", mock.AnythingOfType("*database.MetadataChangeRecord")).Return(nil)
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
						FetchedValue: strPtr(`"Old Title"`),
						UpdatedAt:    time.Now().Add(-24 * time.Hour),
					},
				}
				// First call in LoadMetadataState
				mockStore.On("GetMetadataFieldStates", bookID).Return(existingState, nil).Once()
				// RecordMetadataChange calls from recordChange helper
				mockStore.On("RecordMetadataChange", mock.AnythingOfType("*database.MetadataChangeRecord")).Return(nil)
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

// strPtr returns a string pointer
func strPtr(s string) *string {
	return &s
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

// TestConfigUpdateService_UpdateConfig_AdditionalFields tests additional config fields
func TestConfigUpdateService_UpdateConfig_AdditionalFields(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
	svc := config.NewUpdateService(mockStore)

	originalPlaylistDir := config.AppConfig.PlaylistDir
	originalDatabasePath := config.AppConfig.DatabasePath
	originalSetupComplete := config.AppConfig.SetupComplete
	defer func() {
		config.AppConfig.PlaylistDir = originalPlaylistDir
		config.AppConfig.DatabasePath = originalDatabasePath
		config.AppConfig.SetupComplete = originalSetupComplete
	}()

	t.Run("update playlist_dir", func(t *testing.T) {
		// Mock SetSetting calls that will happen during config persistence
		mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

		status, resp := svc.UpdateConfig(map[string]any{
			"playlist_dir": "/new/playlist/path",
		})
		if status != 200 {
			t.Errorf("expected 200, got %d: %v", status, resp)
		}
		if config.AppConfig.PlaylistDir != "/new/playlist/path" {
			t.Errorf("expected playlist_dir '/new/playlist/path', got %q", config.AppConfig.PlaylistDir)
		}
		if _, ok := resp["message"]; !ok {
			t.Error("expected message in response")
		}
	})

	t.Run("update database_path", func(t *testing.T) {
		mockStore2 := mocks.NewMockStore(t)
		mockStore2.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
		mockStore2.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		svc2 := config.NewUpdateService(mockStore2)

		status, resp := svc2.UpdateConfig(map[string]any{
			"database_path": "/new/db/path.db",
		})
		if status != 200 {
			t.Errorf("expected 200, got %d: %v", status, resp)
		}
		if config.AppConfig.DatabasePath != "/new/db/path.db" {
			t.Errorf("expected database_path '/new/db/path.db', got %q", config.AppConfig.DatabasePath)
		}
		if _, ok := resp["message"]; !ok {
			t.Error("expected message in response")
		}
	})

	t.Run("setup_complete is derived from root_dir not set directly", func(t *testing.T) {
		mockStore3 := mocks.NewMockStore(t)
		mockStore3.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
		mockStore3.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		svc3 := config.NewUpdateService(mockStore3)

		// setup_complete payload is ignored; actual value is derived from root_dir
		config.AppConfig.RootDir = ""
		status, resp := svc3.UpdateConfig(map[string]any{
			"setup_complete": true,
		})
		if status != 200 {
			t.Errorf("expected 200, got %d: %v", status, resp)
		}
		// empty root_dir → setup_complete stays false regardless of payload
		if config.AppConfig.SetupComplete {
			t.Error("setup_complete must be derived from root_dir, not set directly")
		}
		if _, ok := resp["message"]; !ok {
			t.Error("expected message in response")
		}
	})

	t.Run("empty root_dir sets setup_complete to false", func(t *testing.T) {
		mockStore4 := mocks.NewMockStore(t)
		mockStore4.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
		mockStore4.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		svc4 := config.NewUpdateService(mockStore4)

		config.AppConfig.SetupComplete = true
		status, resp := svc4.UpdateConfig(map[string]any{
			"root_dir": "   ",
		})
		if status != 200 {
			t.Errorf("expected 200, got %d: %v", status, resp)
		}
		if config.AppConfig.SetupComplete {
			t.Error("expected setup_complete to be false when root_dir is empty")
		}
		if config.AppConfig.RootDir != "" {
			t.Errorf("expected empty root_dir, got %q", config.AppConfig.RootDir)
		}
	})

	t.Run("non-empty root_dir sets setup_complete to true", func(t *testing.T) {
		mockStore5 := mocks.NewMockStore(t)
		mockStore5.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
		mockStore5.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		svc5 := config.NewUpdateService(mockStore5)

		config.AppConfig.SetupComplete = false
		status, resp := svc5.UpdateConfig(map[string]any{
			"root_dir": "/valid/path",
		})
		if status != 200 {
			t.Errorf("expected 200, got %d: %v", status, resp)
		}
		if !config.AppConfig.SetupComplete {
			t.Error("expected setup_complete to be true when root_dir is set")
		}
		if config.AppConfig.RootDir != "/valid/path" {
			t.Errorf("expected root_dir '/valid/path', got %q", config.AppConfig.RootDir)
		}
	})
}

// TestConfigUpdateService_UpdateConfig_AllFields tests all updatable fields
func TestConfigUpdateService_UpdateConfig_AllFields(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	svc := config.NewUpdateService(mockStore)

	// Save original values
	originalOrgStrat := config.AppConfig.OrganizationStrategy
	originalScanOnStartup := config.AppConfig.ScanOnStartup
	originalAutoOrg := config.AppConfig.AutoOrganize
	originalFolderPattern := config.AppConfig.FolderNamingPattern
	originalFilePattern := config.AppConfig.FileNamingPattern
	originalBackups := config.AppConfig.CreateBackups
	originalLanguage := config.AppConfig.Language
	originalLogLevel := config.AppConfig.LogLevel
	originalAPIKey := config.AppConfig.OpenAIAPIKey
	originalEnableAI := config.AppConfig.EnableAIParsing
	originalConcurrentScans := config.AppConfig.ConcurrentScans
	defer func() {
		config.AppConfig.OrganizationStrategy = originalOrgStrat
		config.AppConfig.ScanOnStartup = originalScanOnStartup
		config.AppConfig.AutoOrganize = originalAutoOrg
		config.AppConfig.FolderNamingPattern = originalFolderPattern
		config.AppConfig.FileNamingPattern = originalFilePattern
		config.AppConfig.CreateBackups = originalBackups
		config.AppConfig.Language = originalLanguage
		config.AppConfig.LogLevel = originalLogLevel
		config.AppConfig.OpenAIAPIKey = originalAPIKey
		config.AppConfig.EnableAIParsing = originalEnableAI
		config.AppConfig.ConcurrentScans = originalConcurrentScans
	}()

	payload := map[string]any{
		"organization_strategy": "custom",
		"scan_on_startup":       true,
		"auto_organize":         true,
		"folder_naming_pattern": "{author}/{series}",
		"file_naming_pattern":   "{title}",
		"create_backups":        true,
		"language":              "en-US",
		"log_level":             "debug",
		"openai_api_key":        "sk-test123",
		"enable_ai_parsing":     true,
		"concurrent_scans":      float64(5),
	}

	status, resp := svc.UpdateConfig(payload)
	if status != 200 {
		t.Errorf("expected 200, got %d: %v", status, resp)
	}

	if config.AppConfig.OrganizationStrategy != "custom" {
		t.Errorf("expected organization_strategy 'custom', got %q", config.AppConfig.OrganizationStrategy)
	}
	if !config.AppConfig.ScanOnStartup {
		t.Error("expected scan_on_startup true")
	}
	if !config.AppConfig.AutoOrganize {
		t.Error("expected auto_organize true")
	}
	if config.AppConfig.FolderNamingPattern != "{author}/{series}" {
		t.Errorf("expected folder_naming_pattern '{author}/{series}', got %q", config.AppConfig.FolderNamingPattern)
	}
	if config.AppConfig.FileNamingPattern != "{title}" {
		t.Errorf("expected file_naming_pattern '{title}', got %q", config.AppConfig.FileNamingPattern)
	}
	if !config.AppConfig.CreateBackups {
		t.Error("expected create_backups true")
	}
	if config.AppConfig.Language != "en-US" {
		t.Errorf("expected language 'en-US', got %q", config.AppConfig.Language)
	}
	if config.AppConfig.LogLevel != "debug" {
		t.Errorf("expected log_level 'debug', got %q", config.AppConfig.LogLevel)
	}
	if config.AppConfig.OpenAIAPIKey != "sk-test123" {
		t.Errorf("expected openai_api_key 'sk-test123', got %q", config.AppConfig.OpenAIAPIKey)
	}
	if !config.AppConfig.EnableAIParsing {
		t.Error("expected enable_ai_parsing true")
	}
	if config.AppConfig.ConcurrentScans != 5 {
		t.Errorf("expected concurrent_scans 5, got %d", config.AppConfig.ConcurrentScans)
	}

	// Response contains message and masked config — no "updated" field in new arch
	if _, ok := resp["message"]; !ok {
		t.Error("expected message in response")
	}
	if _, ok := resp["config"]; !ok {
		t.Error("expected config in response")
	}
}

// TestConfigUpdateService_UpdateConfig_IntConcurrentScans tests int concurrent_scans
func TestConfigUpdateService_UpdateConfig_IntConcurrentScans(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetSetting", mock.Anything).Return(nil, nil).Maybe()
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	svc := config.NewUpdateService(mockStore)

	originalConcurrentScans := config.AppConfig.ConcurrentScans
	defer func() {
		config.AppConfig.ConcurrentScans = originalConcurrentScans
	}()

	status, resp := svc.UpdateConfig(map[string]any{
		"concurrent_scans": 8,
	})
	if status != 200 {
		t.Errorf("expected 200, got %d: %v", status, resp)
	}
	if config.AppConfig.ConcurrentScans != 8 {
		t.Errorf("expected concurrent_scans 8, got %d", config.AppConfig.ConcurrentScans)
	}
}

// TestConfigUpdateService_ApplyUpdates_OpenAIKey tests OpenAI key saved via ApplyUpdates
func TestConfigUpdateService_ApplyUpdates_OpenAIKey(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	svc := config.NewUpdateService(mockStore)

	originalKey := config.AppConfig.OpenAIAPIKey
	originalAI := config.AppConfig.EnableAIParsing
	defer func() {
		config.AppConfig.OpenAIAPIKey = originalKey
		config.AppConfig.EnableAIParsing = originalAI
	}()

	err := svc.ApplyUpdates(map[string]any{
		"openai_api_key":    "sk-proj-test12345",
		"enable_ai_parsing": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.AppConfig.OpenAIAPIKey != "sk-proj-test12345" {
		t.Errorf("expected OpenAI key 'sk-proj-test12345', got %q", config.AppConfig.OpenAIAPIKey)
	}
	if !config.AppConfig.EnableAIParsing {
		t.Error("expected EnableAIParsing to be true")
	}
}

// TestConfigUpdateService_ValidateUpdate tests ValidateUpdate method
func TestConfigUpdateService_ValidateUpdate(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := config.NewUpdateService(mockStore)

	tests := []struct {
		name    string
		payload map[string]any
		wantErr bool
	}{
		{
			name:    "valid payload with data",
			payload: map[string]any{"root_dir": "/test"},
			wantErr: false,
		},
		{
			name:    "empty payload returns error",
			payload: map[string]any{},
			wantErr: true,
		},
		{
			name:    "nil payload returns error",
			payload: nil,
			wantErr: true, // ValidateUpdate checks len(payload)==0, nil map has len 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.ValidateUpdate(tt.payload)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
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

// TestServerHelpers_DecodeRawValue tests decodeRawValue function
func TestServerHelpers_DecodeRawValue(t *testing.T) {
	tests := []struct {
		name     string
		raw      json.RawMessage
		expected any
	}{
		{
			name:     "nil raw message",
			raw:      nil,
			expected: nil,
		},
		{
			name:     "string value",
			raw:      json.RawMessage(`"test"`),
			expected: "test",
		},
		{
			name:     "number value",
			raw:      json.RawMessage(`42`),
			expected: float64(42),
		},
		{
			name:     "boolean value",
			raw:      json.RawMessage(`true`),
			expected: true,
		},
		{
			name:     "invalid json returns string",
			raw:      json.RawMessage(`{invalid`),
			expected: "{invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := decodeRawValue(tt.raw)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestServerHelpers_StringVal tests stringVal function
func TestServerHelpers_StringVal(t *testing.T) {
	tests := []struct {
		name     string
		input    *string
		expected any
	}{
		{
			name:     "nil pointer returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name:     "non-nil pointer returns value",
			input:    strPtr("test"),
			expected: "test",
		},
		{
			name:     "empty string pointer returns empty string",
			input:    strPtr(""),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stringVal(tt.input)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestServerHelpers_IntVal tests intVal function
func TestServerHelpers_IntVal(t *testing.T) {
	tests := []struct {
		name     string
		input    *int
		expected any
	}{
		{
			name:     "nil pointer returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name:     "non-nil pointer returns value",
			input:    intPtrHelper(42),
			expected: 42,
		},
		{
			name:     "zero value pointer returns zero",
			input:    intPtrHelper(0),
			expected: 0,
		},
		{
			name:     "negative value pointer returns negative",
			input:    intPtrHelper(-10),
			expected: -10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := intVal(tt.input)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
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

// TestConfigUpdateService_ExtractIntField_EdgeCases tests edge cases
func TestConfigUpdateService_ExtractIntField_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]any
		key       string
		wantValue int
		wantOK    bool
	}{
		{
			name:      "valid int from float64",
			payload:   map[string]any{"count": float64(42)},
			key:       "count",
			wantValue: 42,
			wantOK:    true,
		},
		{
			name:      "missing key",
			payload:   map[string]any{"other": float64(10)},
			key:       "count",
			wantValue: 0,
			wantOK:    false,
		},
		{
			name:      "wrong type (string)",
			payload:   map[string]any{"count": "42"},
			key:       "count",
			wantValue: 0,
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := util.ExtractIntField(tt.payload, tt.key)
			if value != tt.wantValue {
				t.Errorf("expected value %d, got %d", tt.wantValue, value)
			}
			if ok != tt.wantOK {
				t.Errorf("expected ok %v, got %v", tt.wantOK, ok)
			}
		})
	}
}
