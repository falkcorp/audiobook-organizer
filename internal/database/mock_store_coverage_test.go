// file: internal/database/mock_store_coverage_test.go
// version: 1.1.0
// guid: 9f8e7d6c-5b4a-3c2d-1e0f-a9b8c7d6e5f4
// last-edited: 2026-06-14

package database

import (
	"testing"
	"time"
)

// TestMockStore_CustomFuncPaths tests the custom function paths for all MockStore methods
// to achieve 100% coverage. This complements TestMockStore_AllMethods which tests the nil paths.
func TestMockStore_CustomFuncPaths(t *testing.T) {
	mock := &MockStore{}

	// Set all custom functions to simple implementations
	// Lifecycle
	mock.CloseFunc = func() error { return nil }
	mock.ResetFunc = func() error { return nil }

	// Metadata
	mock.GetMetadataFieldStatesFunc = func(string) ([]MetadataFieldState, error) { return nil, nil }
	mock.UpsertMetadataFieldStateFunc = func(*MetadataFieldState) error { return nil }
	mock.DeleteMetadataFieldStateFunc = func(string, string) error { return nil }

	// Authors
	mock.GetAllAuthorsFunc = func() ([]Author, error) { return nil, nil }
	mock.GetAuthorByIDFunc = func(int) (*Author, error) { return nil, nil }
	mock.GetAuthorByNameFunc = func(string) (*Author, error) { return nil, nil }
	mock.CreateAuthorFunc = func(string) (*Author, error) { return nil, nil }

	// Series
	mock.GetAllSeriesFunc = func() ([]Series, error) { return nil, nil }
	mock.GetSeriesByIDFunc = func(int) (*Series, error) { return nil, nil }
	mock.GetSeriesByNameFunc = func(string, *int) (*Series, error) { return nil, nil }
	mock.CreateSeriesFunc = func(string, *int) (*Series, error) { return nil, nil }

	// Works
	mock.GetAllWorksFunc = func() ([]Work, error) { return nil, nil }
	mock.GetWorkByIDFunc = func(string) (*Work, error) { return nil, nil }
	mock.CreateWorkFunc = func(*Work) (*Work, error) { return nil, nil }
	mock.UpdateWorkFunc = func(string, *Work) (*Work, error) { return nil, nil }
	mock.DeleteWorkFunc = func(string) error { return nil }
	mock.GetBooksByWorkIDFunc = func(string) ([]Book, error) { return nil, nil }

	// Books
	mock.GetAllBooksFunc = func(int, int) ([]Book, error) { return nil, nil }
	mock.GetBookByIDFunc = func(string) (*Book, error) { return nil, nil }
	mock.GetBookByFilePathFunc = func(string) (*Book, error) { return nil, nil }
	mock.GetBookByFileHashFunc = func(string) (*Book, error) { return nil, nil }
	mock.GetBookByOriginalHashFunc = func(string) (*Book, error) { return nil, nil }
	mock.GetBookByOrganizedHashFunc = func(string) (*Book, error) { return nil, nil }
	mock.GetDuplicateBooksFunc = func() ([][]Book, error) { return nil, nil }
	mock.GetBooksBySeriesIDFunc = func(int) ([]Book, error) { return nil, nil }
	mock.GetBooksByAuthorIDFunc = func(int) ([]Book, error) { return nil, nil }
	mock.CreateBookFunc = func(*Book) (*Book, error) { return nil, nil }
	mock.UpdateBookFunc = func(string, *Book) (*Book, error) { return nil, nil }
	mock.DeleteBookFunc = func(string) error { return nil }
	mock.SearchBooksFunc = func(string, int, int) ([]Book, error) { return nil, nil }
	mock.CountBooksFunc = func() (int, error) { return 0, nil }
	mock.CountFilesFunc = func() (int, error) { return 0, nil }
	mock.ListSoftDeletedBooksFunc = func(int, int, *time.Time) ([]Book, error) { return nil, nil }
	mock.GetBooksByVersionGroupFunc = func(string) ([]Book, error) { return nil, nil }
	mock.GetBookIDsByISBNASINFunc = func(string, string, string) ([]string, error) { return nil, nil }

	// Import Paths
	mock.GetAllImportPathsFunc = func() ([]ImportPath, error) { return nil, nil }
	mock.GetImportPathByIDFunc = func(int) (*ImportPath, error) { return nil, nil }
	mock.GetImportPathByPathFunc = func(string) (*ImportPath, error) { return nil, nil }
	mock.CreateImportPathFunc = func(string, string) (*ImportPath, error) { return nil, nil }
	mock.UpdateImportPathFunc = func(int, *ImportPath) error { return nil }
	mock.DeleteImportPathFunc = func(int) error { return nil }

	// Operations
	mock.CreateOperationFunc = func(string, string, *string) (*Operation, error) { return nil, nil }
	mock.GetOperationByIDFunc = func(string) (*Operation, error) { return nil, nil }
	mock.GetRecentOperationsFunc = func(int) ([]Operation, error) { return nil, nil }
	mock.UpdateOperationStatusFunc = func(string, string, int, int, string) error { return nil }
	mock.UpdateOperationErrorFunc = func(string, string) error { return nil }

	// Operation Logs
	mock.AddOperationLogFunc = func(string, string, string, *string) error { return nil }
	mock.GetOperationLogsFunc = func(string) ([]OperationLog, error) { return nil, nil }

	// User Preferences
	mock.GetUserPreferenceFunc = func(string) (*UserPreference, error) { return nil, nil }
	mock.SetUserPreferenceFunc = func(string, string) error { return nil }
	mock.GetAllUserPreferencesFunc = func() ([]UserPreference, error) { return nil, nil }

	// Settings
	mock.GetSettingFunc = func(string) (*Setting, error) { return nil, nil }
	mock.SetSettingFunc = func(string, string, string, bool) error { return nil }
	mock.GetAllSettingsFunc = func() ([]Setting, error) { return nil, nil }
	mock.DeleteSettingFunc = func(string) error { return nil }

	// Playlists
	mock.CreatePlaylistFunc = func(string, *int, string) (*Playlist, error) { return nil, nil }
	mock.GetPlaylistByIDFunc = func(int) (*Playlist, error) { return nil, nil }
	mock.GetPlaylistBySeriesIDFunc = func(int) (*Playlist, error) { return nil, nil }
	mock.AddPlaylistItemFunc = func(int, int, int) error { return nil }
	mock.GetPlaylistItemsFunc = func(int) ([]PlaylistItem, error) { return nil, nil }

	// Users
	mock.CreateUserFunc = func(string, string, string, string, []string, string) (*User, error) { return nil, nil }
	mock.GetUserByIDFunc = func(string) (*User, error) { return nil, nil }
	mock.GetUserByUsernameFunc = func(string) (*User, error) { return nil, nil }
	mock.GetUserByEmailFunc = func(string) (*User, error) { return nil, nil }
	mock.UpdateUserFunc = func(*User) error { return nil }

	// Sessions
	mock.CreateSessionFunc = func(string, string, string, time.Duration) (*Session, error) { return nil, nil }
	mock.GetSessionFunc = func(string) (*Session, error) { return nil, nil }
	mock.RevokeSessionFunc = func(string) error { return nil }
	mock.ListUserSessionsFunc = func(string) ([]Session, error) { return nil, nil }

	// Per-user preferences
	mock.SetUserPreferenceForUserFunc = func(string, string, string) error { return nil }
	mock.GetUserPreferenceForUserFunc = func(string, string) (*UserPreferenceKV, error) { return nil, nil }
	mock.GetAllPreferencesForUserFunc = func(string) ([]UserPreferenceKV, error) { return nil, nil }

	// Book segments
	mock.CreateBookSegmentFunc = func(int, *BookSegment) (*BookSegment, error) { return nil, nil }
	mock.UpdateBookSegmentFunc = func(*BookSegment) error { return nil }
	mock.ListBookSegmentsFunc = func(int) ([]BookSegment, error) { return nil, nil }
	mock.MergeBookSegmentsFunc = func(int, *BookSegment, []string) error { return nil }

	// Playback events
	mock.AddPlaybackEventFunc = func(*PlaybackEvent) error { return nil }
	mock.ListPlaybackEventsFunc = func(string, int, int) ([]PlaybackEvent, error) { return nil, nil }
	mock.UpdatePlaybackProgressFunc = func(*PlaybackProgress) error { return nil }
	mock.GetPlaybackProgressFunc = func(string, int) (*PlaybackProgress, error) { return nil, nil }

	// Stats
	mock.IncrementBookPlayStatsFunc = func(int, int) error { return nil }
	mock.GetBookStatsFunc = func(int) (*BookStats, error) { return nil, nil }
	mock.IncrementUserListenStatsFunc = func(string, int) error { return nil }
	mock.GetUserStatsFunc = func(string) (*UserStats, error) { return nil, nil }

	// Hash blocklist
	mock.IsHashBlockedFunc = func(string) (bool, error) { return false, nil }
	mock.AddBlockedHashFunc = func(string, string) error { return nil }
	mock.RemoveBlockedHashFunc = func(string) error { return nil }
	mock.GetAllBlockedHashesFunc = func() ([]DoNotImport, error) { return nil, nil }
	mock.GetBlockedHashByHashFunc = func(string) (*DoNotImport, error) { return nil, nil }

	// Now call all methods to execute the custom function paths
	_ = mock.Close()
	_ = mock.Reset()
	_, _ = mock.GetMetadataFieldStates("book-1")
	_ = mock.UpsertMetadataFieldState(&MetadataFieldState{})
	_ = mock.DeleteMetadataFieldState("book-1", "title")
	_, _ = mock.GetAllAuthors()
	_, _ = mock.GetAuthorByID(1)
	_, _ = mock.GetAuthorByName("Test")
	_, _ = mock.CreateAuthor("Test")
	_, _ = mock.GetAllSeries()
	_, _ = mock.GetSeriesByID(1)
	_, _ = mock.GetSeriesByName("Test", nil)
	_, _ = mock.CreateSeries("Test", nil)
	_, _ = mock.GetAllWorks()
	_, _ = mock.GetWorkByID("work-1")
	_, _ = mock.CreateWork(&Work{})
	_, _ = mock.UpdateWork("work-1", &Work{})
	_ = mock.DeleteWork("work-1")
	_, _ = mock.GetBooksByWorkID("work-1")
	_, _ = mock.GetAllBooks(10, 0)
	_, _ = mock.GetBookByID("book-1")
	_, _ = mock.GetBookByFilePath("/path")
	_, _ = mock.GetBookByFileHash("hash")
	_, _ = mock.GetBookByOriginalHash("hash")
	_, _ = mock.GetBookByOrganizedHash("hash")
	_, _ = mock.GetDuplicateBooks()
	_, _ = mock.GetBooksBySeriesID(1)
	_, _ = mock.GetBooksByAuthorID(1)
	_, _ = mock.CreateBook(&Book{})
	_, _ = mock.UpdateBook("book-1", &Book{})
	_ = mock.DeleteBook("book-1")
	_, _ = mock.SearchBooks("query", 10, 0)
	_, _ = mock.CountBooks()
	_, _ = mock.CountFiles()
	_, _ = mock.ListSoftDeletedBooks(10, 0, nil)
	_, _ = mock.GetBooksByVersionGroup("group-1")
	_, _ = mock.GetBookIDsByISBNASIN("0123456789", "9780123456786", "B001234567")
	_, _ = mock.GetAllImportPaths()
	_, _ = mock.GetImportPathByID(1)
	_, _ = mock.GetImportPathByPath("/path")
	_, _ = mock.CreateImportPath("/path", "name")
	_ = mock.UpdateImportPath(1, &ImportPath{})
	_ = mock.DeleteImportPath(1)
	_, _ = mock.CreateOperation("op-1", "scan", nil)
	_, _ = mock.GetOperationByID("op-1")
	_, _ = mock.GetRecentOperations(10)
	_ = mock.UpdateOperationStatus("op-1", "running", 1, 10, "msg")
	_ = mock.UpdateOperationError("op-1", "error")
	_ = mock.AddOperationLog("op-1", "info", "message", nil)
	_, _ = mock.GetOperationLogs("op-1")
	_, _ = mock.GetUserPreference("key")
	_ = mock.SetUserPreference("key", "value")
	_, _ = mock.GetAllUserPreferences()
	_, _ = mock.GetSetting("key")
	_ = mock.SetSetting("key", "value", "string", false)
	_, _ = mock.GetAllSettings()
	_ = mock.DeleteSetting("key")
	_, _ = mock.CreatePlaylist("name", nil, "/path")
	_, _ = mock.GetPlaylistByID(1)
	_, _ = mock.GetPlaylistBySeriesID(1)
	_ = mock.AddPlaylistItem(1, 1, 1)
	_, _ = mock.GetPlaylistItems(1)
	_, _ = mock.CreateUser("user", "email", "algo", "hash", []string{"user"}, "active")
	_, _ = mock.GetUserByID("user-1")
	_, _ = mock.GetUserByUsername("username")
	_, _ = mock.GetUserByEmail("email")
	_ = mock.UpdateUser(&User{})
	_, _ = mock.CreateSession("user-1", "127.0.0.1", "agent", 24*time.Hour)
	_, _ = mock.GetSession("session-1")
	_ = mock.RevokeSession("session-1")
	_, _ = mock.ListUserSessions("user-1")
	_ = mock.SetUserPreferenceForUser("user-1", "key", "value")
	_, _ = mock.GetUserPreferenceForUser("user-1", "key")
	_, _ = mock.GetAllPreferencesForUser("user-1")
	_, _ = mock.CreateBookSegment(1, &BookSegment{})
	_ = mock.UpdateBookSegment(&BookSegment{})
	_, _ = mock.ListBookSegments(1)
	_ = mock.MergeBookSegments(1, &BookSegment{}, []string{"seg-1"})
	_ = mock.AddPlaybackEvent(&PlaybackEvent{})
	_, _ = mock.ListPlaybackEvents("user-1", 1, 10)
	_ = mock.UpdatePlaybackProgress(&PlaybackProgress{})
	_, _ = mock.GetPlaybackProgress("user-1", 1)
	_ = mock.IncrementBookPlayStats(1, 60)
	_, _ = mock.GetBookStats(1)
	_ = mock.IncrementUserListenStats("user-1", 60)
	_, _ = mock.GetUserStats("user-1")
	_, _ = mock.IsHashBlocked("hash")
	_ = mock.AddBlockedHash("hash", "reason")
	_ = mock.RemoveBlockedHash("hash")
	_, _ = mock.GetAllBlockedHashes()
	_, _ = mock.GetBlockedHashByHash("hash")

	// If we got here without panicking, all custom function paths were executed
}
