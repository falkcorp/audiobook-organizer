// file: internal/database/mock_store.go
// version: 1.62.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-06-14

package database

import (
	"context"
	"fmt"
	"time"
)

// Compile-time assertion: MockStore must implement Store. If a new
// method is added to the Store interface without a corresponding
// MockStore method, this line breaks the build before tests even
// start — catching the drift that #241 had to fix after merge.
//
// (We keep this hand-written MockStore alongside the mockery-generated
// one in internal/database/mocks because the two patterns serve
// different callers: this one is permissive by default — unconfigured
// methods return zero values silently, matching the historical
// "minimal stub" idiom used by ~22 server test files. Converting all
// of those to mockery's strict-expectation model would require
// either adding .Maybe() expectations for every method each code
// path happens to call, or writing a loose-mock helper that
// re-creates this pattern on top of the generated mock. Neither is
// clearly an improvement, so we keep the two patterns and rely on
// this assertion + CI vet to prevent drift.)
var _ Store = (*MockStore)(nil)

// MockStore is a simple mock implementation for testing services
type MockStore struct {
	// Book methods
	GetBookByIDFunc                 func(id string) (*Book, error)
	GetBookByFilePathFunc           func(path string) (*Book, error)
	GetAllBooksFunc                 func(limit, offset int) ([]Book, error)
	ListBookIDsFunc                 func() ([]string, error)
	GetAllBookSummariesFunc         func(limit, offset int) ([]BookSummary, error)
	GetBooksByWorkIDFunc            func(workID string) ([]Book, error)
	GetBooksBySeriesIDFunc          func(seriesID int) ([]Book, error)
	GetBooksByAuthorIDFunc          func(authorID int) ([]Book, error)
	GetBookByITunesPersistentIDFunc func(persistentID string) (*Book, error)
	ListBooksByITunesPIDFunc        func(limit, offset int) ([]Book, error)
	GetBookByFileHashFunc           func(hash string) (*Book, error)
	GetBookByOriginalHashFunc       func(hash string) (*Book, error)
	GetBookByOrganizedHashFunc      func(hash string) (*Book, error)
	GetBookVersionsFunc             func(id string, limit int) ([]BookSnapshot, error)
	GetBookAtVersionFunc            func(id string, ts time.Time) (*Book, error)
	RevertBookToVersionFunc         func(id string, ts time.Time) (*Book, error)
	PruneBookVersionsFunc           func(id string, keepCount int) (int, error)
	GetDuplicateBooksFunc           func() ([][]Book, error)
	CreateBookFunc                  func(book *Book) (*Book, error)
	UpdateBookFunc                  func(id string, book *Book) (*Book, error)
	UpdateBookRatingError           error
	DeleteBookFunc                  func(id string) error
	SearchBooksFunc                 func(query string, limit, offset int) ([]Book, error)
	CountBooksFunc                  func() (int, error)
	GetDistinctGenresFunc           func() ([]string, error)
	GetDistinctLanguagesFunc        func() ([]string, error)
	CountFilesFunc                  func() (int, error)
	CountAuthorsFunc                func() (int, error)
	CountSeriesFunc                 func() (int, error)
	GetBookCountsByLocationFunc     func(rootDir string) (int, int, error)
	GetDashboardStatsFunc           func() (*DashboardStats, error)
	SetRootDirFunc                  func(string)
	InvalidateLibraryStatsFunc      func()
	ListSoftDeletedBooksFunc        func(limit, offset int, olderThan *time.Time) ([]Book, error)

	// Work methods
	GetAllWorksFunc func() ([]Work, error)
	GetWorkByIDFunc func(id string) (*Work, error)
	CreateWorkFunc  func(work *Work) (*Work, error)
	UpdateWorkFunc  func(id string, work *Work) (*Work, error)
	DeleteWorkFunc  func(id string) error

	// Author methods
	GetAllAuthorsFunc    func() ([]Author, error)
	GetAuthorByIDFunc    func(id int) (*Author, error)
	GetAuthorByNameFunc  func(name string) (*Author, error)
	CreateAuthorFunc     func(name string) (*Author, error)
	DeleteAuthorFunc     func(id int) error
	UpdateAuthorNameFunc func(id int, name string) error

	GetAuthorsByIDsFunc func(ids []int) (map[int]*Author, error)

	// Author Alias methods
	GetAuthorAliasesFunc    func(authorID int) ([]AuthorAlias, error)
	GetAllAuthorAliasesFunc func() ([]AuthorAlias, error)
	CreateAuthorAliasFunc   func(authorID int, aliasName string, aliasType string) (*AuthorAlias, error)
	DeleteAuthorAliasFunc   func(id int) error
	FindAuthorByAliasFunc   func(aliasName string) (*Author, error)

	// Author Tombstones
	CreateAuthorTombstoneFunc  func(oldID, canonicalID int) error
	GetAuthorTombstoneFunc     func(oldID int) (int, error)
	ResolveTombstoneChainsFunc func() (int, error)

	// Series methods
	GetAllSeriesFunc     func() ([]Series, error)
	GetSeriesByIDFunc    func(id int) (*Series, error)
	GetSeriesByNameFunc  func(name string, authorID *int) (*Series, error)
	CreateSeriesFunc     func(name string, authorID *int) (*Series, error)
	DeleteSeriesFunc     func(id int) error
	UpdateSeriesNameFunc func(id int, name string) error
	GetSeriesByIDsFunc   func(ids []int) (map[int]*Series, error)

	// Metadata
	GetMetadataFieldStatesFunc   func(bookID string) ([]MetadataFieldState, error)
	UpsertMetadataFieldStateFunc func(state *MetadataFieldState) error
	DeleteMetadataFieldStateFunc func(bookID, field string) error

	// Metadata change history
	RecordMetadataChangeFunc     func(record *MetadataChangeRecord) error
	GetMetadataChangeHistoryFunc func(bookID string, field string, limit int) ([]MetadataChangeRecord, error)
	GetBookChangeHistoryFunc     func(bookID string, limit int) ([]MetadataChangeRecord, error)

	// Import Paths
	GetAllImportPathsFunc      func() ([]ImportPath, error)
	GetImportPathByIDFunc      func(id int) (*ImportPath, error)
	GetImportPathByPathFunc    func(path string) (*ImportPath, error)
	CreateImportPathFunc       func(path, name string) (*ImportPath, error)
	UpdateImportPathFunc       func(id int, importPath *ImportPath) error
	DeleteImportPathFunc       func(id int) error
	CountBooksByPathPrefixFunc func(prefix string) (int, error)

	// Operations
	CreateOperationFunc           func(id, opType string, folderPath *string) (*Operation, error)
	GetOperationByIDFunc          func(id string) (*Operation, error)
	GetRecentOperationsFunc       func(limit int) ([]Operation, error)
	ListOperationsFunc            func(limit, offset int) ([]Operation, int, error)
	UpdateOperationStatusFunc     func(id, status string, progress, total int, message string) error
	UpdateOperationErrorFunc      func(id, errorMessage string) error
	UpdateOperationResultDataFunc func(id string, resultData string) error

	// Operation State Persistence
	SaveOperationStateFunc       func(opID string, state []byte) error
	GetOperationStateFunc        func(opID string) ([]byte, error)
	SaveOperationParamsFunc      func(opID string, params []byte) error
	GetOperationParamsFunc       func(opID string) ([]byte, error)
	DeleteOperationStateFunc     func(opID string) error
	DeleteOperationsByStatusFunc func(statuses []string) (int, error)
	DeleteOperationWithLogsFunc  func(id string) error
	GetInterruptedOperationsFunc func() ([]Operation, error)

	// Operation Logs
	AddOperationLogFunc          func(operationID, level, message string, details *string) error
	GetOperationLogsFunc         func(operationID string) ([]OperationLog, error)
	SaveOperationSummaryLogFunc  func(op *OperationSummaryLog) error
	GetOperationSummaryLogFunc   func(id string) (*OperationSummaryLog, error)
	ListOperationSummaryLogsFunc func(limit, offset int) ([]OperationSummaryLog, error)

	// System activity log
	AddSystemActivityLogFunc    func(source, level, message string) error
	GetSystemActivityLogsFunc   func(source string, limit int) ([]SystemActivityLog, error)
	PruneOperationLogsFunc      func(olderThan time.Time) (int, error)
	PruneOperationChangesFunc   func(olderThan time.Time) (int, error)
	PruneSystemActivityLogsFunc func(olderThan time.Time) (int, error)

	// AI jobs
	CreateAIJobFunc        func(job AIJob, payloadJSON []byte) error
	GetAIJobFunc           func(id string) (AIJob, error)
	GetAIJobByBatchIDFunc  func(batchID string) (AIJob, error)
	GetAIJobPayloadFunc    func(id string) ([]byte, error)
	MarkAIJobSubmittedFunc func(id, batchID string) error
	MarkAIJobCompletedFunc func(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error
	MarkAIJobFailedFunc    func(id, errMsg string) error
	ListAIJobsFunc         func(typeFilter, statusFilter string, limit, offset int) ([]AIJob, error)

	// Metadata rejections
	AddMetadataRejectionFunc     func(r MetadataRejection) error
	GetMetadataRejectionsFunc    func(bookID string) ([]MetadataRejection, error)
	DeleteMetadataRejectionsFunc func(bookID string) error

	// User Preferences
	GetUserPreferenceFunc     func(key string) (*UserPreference, error)
	SetUserPreferenceFunc     func(key, value string) error
	GetAllUserPreferencesFunc func() ([]UserPreference, error)

	// Settings
	GetSettingFunc     func(key string) (*Setting, error)
	SetSettingFunc     func(key, value, typ string, isSecret bool) error
	GetAllSettingsFunc func() ([]Setting, error)
	DeleteSettingFunc  func(key string) error

	// RawKV — func delegates so tests can intercept prefix scans / deletes.
	SetRawFunc      func(key string, value []byte) error
	GetRawFunc      func(key string) ([]byte, error)
	DeleteRawFunc   func(key string) error
	ScanPrefixFunc  func(prefix string) ([]KVPair, error)
	CountPrefixFunc func(prefix string) (int64, error)

	// Playlists
	CreatePlaylistFunc        func(name string, seriesID *int, filePath string) (*Playlist, error)
	GetPlaylistByIDFunc       func(id int) (*Playlist, error)
	GetPlaylistBySeriesIDFunc func(seriesID int) (*Playlist, error)
	AddPlaylistItemFunc       func(playlistID, bookID, position int) error
	GetPlaylistItemsFunc      func(playlistID int) ([]PlaylistItem, error)

	// Users
	CreateUserFunc        func(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error)
	GetUserByIDFunc       func(id string) (*User, error)
	GetUserByUsernameFunc func(username string) (*User, error)
	GetUserByEmailFunc    func(email string) (*User, error)
	UpdateUserFunc        func(user *User) error
	ListUsersFunc         func() ([]User, error)

	// Sessions
	CreateSessionFunc         func(userID, ip, userAgent string, ttl time.Duration) (*Session, error)
	GetSessionFunc            func(id string) (*Session, error)
	RevokeSessionFunc         func(id string) error
	ListUserSessionsFunc      func(userID string) ([]Session, error)
	DeleteExpiredSessionsFunc func(now time.Time) (int, error)
	CountUsersFunc            func() (int, error)

	// Roles
	GetRoleByIDFunc   func(id string) (*Role, error)
	GetRoleByNameFunc func(name string) (*Role, error)
	ListRolesFunc     func() ([]Role, error)
	CreateRoleFunc    func(role *Role) (*Role, error)
	UpdateRoleFunc    func(role *Role) error
	DeleteRoleFunc    func(id string) error

	// User positions + book state (spec 3.6)
	SetUserPositionFunc            func(userID, bookID, segmentID string, positionSeconds float64) error
	GetUserPositionFunc            func(userID, bookID string) (*UserPosition, error)
	ListUserPositionsForBookFunc   func(userID, bookID string) ([]UserPosition, error)
	ClearUserPositionsFunc         func(userID, bookID string) error
	SetUserBookStateFunc           func(state *UserBookState) error
	GetUserBookStateFunc           func(userID, bookID string) (*UserBookState, error)
	ListUserBookStatesByStatusFunc func(userID, status string, limit, offset int) ([]UserBookState, error)
	ListUserPositionsSinceFunc     func(userID string, t time.Time) ([]UserPosition, error)

	// Book versions
	CreateBookVersionFunc           func(v *BookVersion) (*BookVersion, error)
	GetBookVersionFunc              func(id string) (*BookVersion, error)
	GetBookVersionsByBookIDFunc     func(bookID string) ([]BookVersion, error)
	GetActiveVersionForBookFunc     func(bookID string) (*BookVersion, error)
	UpdateBookVersionFunc           func(v *BookVersion) error
	DeleteBookVersionFunc           func(id string) error
	GetBookVersionByTorrentHashFunc func(hash string) (*BookVersion, error)
	ListTrashedBookVersionsFunc     func() ([]BookVersion, error)
	ListPurgedBookVersionsFunc      func() ([]BookVersion, error)

	// User playlists (spec 3.4)
	CreateUserPlaylistFunc         func(pl *UserPlaylist) (*UserPlaylist, error)
	GetUserPlaylistFunc            func(id string) (*UserPlaylist, error)
	GetUserPlaylistByNameFunc      func(name string) (*UserPlaylist, error)
	GetUserPlaylistByITunesPIDFunc func(pid string) (*UserPlaylist, error)
	ListUserPlaylistsFunc          func(playlistType string, limit, offset int) ([]UserPlaylist, int, error)
	ListUserPlaylistsForUserFunc   func(userID, playlistType string, limit, offset int) ([]UserPlaylist, int, error)
	UpdateUserPlaylistFunc         func(pl *UserPlaylist) error
	DeleteUserPlaylistFunc         func(id string) error
	ListDirtyUserPlaylistsFunc     func() ([]UserPlaylist, error)

	// API keys
	CreateAPIKeyFunc        func(key *APIKey) (*APIKey, error)
	GetAPIKeyFunc           func(id string) (*APIKey, error)
	GetAPIKeyByHashFunc     func(hash string) (*APIKey, error)
	ListAPIKeysForUserFunc  func(userID string) ([]APIKey, error)
	ListAllAPIKeysFunc      func() ([]APIKey, error)
	RevokeAPIKeyFunc        func(id string) error
	SetAPIKeyStatusFunc     func(id, status string, at time.Time) error
	TouchAPIKeyLastUsedFunc func(id string, at time.Time, ip string) error

	// Invites
	CreateInviteFunc      func(invite *Invite) (*Invite, error)
	GetInviteFunc         func(token string) (*Invite, error)
	ListActiveInvitesFunc func() ([]Invite, error)
	DeleteInviteFunc      func(token string) error
	ConsumeInviteFunc     func(token, passwordHashAlgo, passwordHash string) (*User, error)

	// Per-user preferences
	SetUserPreferenceForUserFunc func(userID, key, value string) error
	GetUserPreferenceForUserFunc func(userID, key string) (*UserPreferenceKV, error)
	GetAllPreferencesForUserFunc func(userID string) ([]UserPreferenceKV, error)

	// Book segments
	CreateBookSegmentFunc  func(bookNumericID int, segment *BookSegment) (*BookSegment, error)
	UpdateBookSegmentFunc  func(segment *BookSegment) error
	ListBookSegmentsFunc   func(bookNumericID int) ([]BookSegment, error)
	MergeBookSegmentsFunc  func(bookNumericID int, newSegment *BookSegment, supersedeIDs []string) error
	GetBookSegmentByIDFunc func(segmentID string) (*BookSegment, error)
	MoveSegmentsToBookFunc func(segmentIDs []string, targetBookNumericID int) error

	// Playback events
	AddPlaybackEventFunc       func(event *PlaybackEvent) error
	ListPlaybackEventsFunc     func(userID string, bookNumericID int, limit int) ([]PlaybackEvent, error)
	UpdatePlaybackProgressFunc func(progress *PlaybackProgress) error
	GetPlaybackProgressFunc    func(userID string, bookNumericID int) (*PlaybackProgress, error)

	// Stats
	IncrementBookPlayStatsFunc   func(bookNumericID int, seconds int) error
	GetBookStatsFunc             func(bookNumericID int) (*BookStats, error)
	IncrementUserListenStatsFunc func(userID string, seconds int) error
	GetUserStatsFunc             func(userID string) (*UserStats, error)

	// Hash blocklist
	IsHashBlockedFunc        func(hash string) (bool, error)
	AddBlockedHashFunc       func(hash, reason string) error
	RemoveBlockedHashFunc    func(hash string) error
	GetAllBlockedHashesFunc  func() ([]DoNotImport, error)
	GetBlockedHashByHashFunc func(hash string) (*DoNotImport, error)

	// Tombstone operations
	CreateBookTombstoneFunc func(book *Book) error
	GetBookTombstoneFunc    func(id string) (*Book, error)
	DeleteBookTombstoneFunc func(id string) error
	ListBookTombstonesFunc  func(limit int) ([]Book, error)

	// Version Management
	GetBooksByVersionGroupFunc       func(groupID string) ([]Book, error)
	GetBooksByMetadataSourceHashFunc func(hash string) ([]Book, error)
	GetBookIDsByISBNASINFunc         func(isbn10, isbn13, asin string) ([]string, error)

	// iTunes Library Fingerprints
	SaveLibraryFingerprintFunc func(path string, size int64, modTime time.Time, crc32 uint32) error
	GetLibraryFingerprintFunc  func(path string) (*LibraryFingerprintRecord, error)

	// Scan cache
	GetScanCacheMapFunc     func() (map[string]ScanCacheEntry, error)
	UpdateScanCacheFunc     func(bookID string, mtime int64, size int64) error
	MarkNeedsRescanFunc     func(bookID string) error
	GetDirtyBookFoldersFunc func() ([]string, error)

	// External ID mapping
	CreateExternalIDMappingFunc      func(mapping *ExternalIDMapping) error
	GetBookByExternalIDFunc          func(source, externalID string) (string, error)
	GetExternalIDsForBookFunc        func(bookID string) ([]ExternalIDMapping, error)
	IsExternalIDTombstonedFunc       func(source, externalID string) (bool, error)
	TombstoneExternalIDFunc          func(source, externalID string) error
	ReassignExternalIDsFunc          func(oldBookID, newBookID string) error
	BulkCreateExternalIDMappingsFunc func(mappings []ExternalIDMapping) error

	// BookFile methods
	CreateBookFileFunc                  func(file *BookFile) error
	GetAllBookFilesFunc                 func() ([]BookFile, error)
	UpdateBookFileFunc                  func(id string, file *BookFile) error
	UpdateBookFileHashesFunc            func(id, originalHash, postMetadataHash string) error
	GetBookFilesFunc                    func(bookID string) ([]BookFile, error)
	GetBookFileByIDFunc                 func(bookID, fileID string) (*BookFile, error)
	GetBookFileByPIDFunc                func(itunesPID string) (*BookFile, error)
	ClearITunesPIDFunc                  func(itunesPID string) (bool, error)
	GetBookFileByPathFunc               func(filePath string) (*BookFile, error)
	MarkFileImportedFromDelugeFunc      func(ctx context.Context, originalPath, libraryPath, torrentHash string) error
	GetBookFileByAcoustIDFunc           func(fingerprint string) (*BookFile, error)
	GetBookFileByAcoustIDFuzzyFunc      func(fingerprint string, minSimilarity float64) (*BookFile, error)
	DeleteBookFileFunc                  func(id string) error
	DeleteBookFilesForBookFunc          func(bookID string) error
	UpsertBookFileFunc                  func(file *BookFile) error
	BatchUpsertBookFilesFunc            func(files []*BookFile) error
	MoveBookFilesToBookFunc             func(fileIDs []string, sourceBookID, targetBookID string) error
	GetDuplicateFilesByHashFunc         func(limit int) ([]DuplicateFileGroup, error)
	GetBookBySegmentFileHashFunc        func(hash string) (*Book, error)
	SetBookFileHashFunc                 func(id, hash string) error
	GetBookFileHashStatsFunc            func() (*BookFileHashStats, error)
	GetBookMetadataHashStatsFunc        func() (*BookMetadataHashStats, error)
	GetFilesWithFingerprintFailuresFunc func(reason string, limit, offset int) ([]BookFile, int64, error)
	GetAcoustIDStatsFunc                func() (*AcoustIDStats, error)

	// Path history
	RecordPathChangeFunc   func(change *BookPathChange) error
	GetBookPathHistoryFunc func(bookID string) ([]BookPathChange, error)

	// Book Tags
	AddBookTagFunc             func(bookID, tag string) error
	AddBookTagWithSourceFunc   func(bookID, tag, source string) error
	RemoveBookTagFunc          func(bookID, tag string) error
	RemoveBookTagsByPrefixFunc func(bookID, prefix, source string) error
	GetBookTagsFunc            func(bookID string) ([]string, error)
	GetBookTagsDetailedFunc    func(bookID string) ([]BookTag, error)
	SetBookTagsFunc            func(bookID string, tags []string) error
	ListAllTagsFunc            func() ([]TagWithCount, error)
	GetBooksByTagFunc          func(tag string) ([]string, error)

	// Author Tags
	AddAuthorTagFunc             func(authorID int, tag string) error
	AddAuthorTagWithSourceFunc   func(authorID int, tag, source string) error
	RemoveAuthorTagFunc          func(authorID int, tag string) error
	RemoveAuthorTagsByPrefixFunc func(authorID int, prefix, source string) error
	GetAuthorTagsFunc            func(authorID int) ([]string, error)
	GetAuthorTagsDetailedFunc    func(authorID int) ([]BookTag, error)
	SetAuthorTagsFunc            func(authorID int, tags []string) error
	ListAllAuthorTagsFunc        func() ([]TagWithCount, error)
	GetAuthorsByTagFunc          func(tag string) ([]int, error)

	// Series Tags
	AddSeriesTagFunc             func(seriesID int, tag string) error
	AddSeriesTagWithSourceFunc   func(seriesID int, tag, source string) error
	RemoveSeriesTagFunc          func(seriesID int, tag string) error
	RemoveSeriesTagsByPrefixFunc func(seriesID int, prefix, source string) error
	GetSeriesTagsFunc            func(seriesID int) ([]string, error)
	GetSeriesTagsDetailedFunc    func(seriesID int) ([]BookTag, error)
	SetSeriesTagsFunc            func(seriesID int, tags []string) error
	ListAllSeriesTagsFunc        func() ([]TagWithCount, error)
	GetSeriesByTagFunc           func(tag string) ([]int, error)

	// Lifecycle
	CloseFunc func() error
	ResetFunc func() error

	// MetadataCacheStore (METADATA-CACHED-MATCHER)
	GetMetadataCacheFunc      func(bookID string) (*MetadataCandidateCache, error)
	PutMetadataCacheFunc      func(entry *MetadataCandidateCache) error
	DeleteMetadataCacheFunc   func(bookID string) error
	ListMetadataCacheKeysFunc func() ([]MetadataCacheSummary, error)
}

func (m *MockStore) GetMetadataCache(bookID string) (*MetadataCandidateCache, error) {
	if m.GetMetadataCacheFunc != nil {
		return m.GetMetadataCacheFunc(bookID)
	}
	return nil, nil
}
func (m *MockStore) PutMetadataCache(entry *MetadataCandidateCache) error {
	if m.PutMetadataCacheFunc != nil {
		return m.PutMetadataCacheFunc(entry)
	}
	return nil
}
func (m *MockStore) DeleteMetadataCache(bookID string) error {
	if m.DeleteMetadataCacheFunc != nil {
		return m.DeleteMetadataCacheFunc(bookID)
	}
	return nil
}
func (m *MockStore) ListMetadataCacheKeys() ([]MetadataCacheSummary, error) {
	if m.ListMetadataCacheKeysFunc != nil {
		return m.ListMetadataCacheKeysFunc()
	}
	return nil, nil
}

// Implement all Store interface methods

func (m *MockStore) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

func (m *MockStore) GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error) {
	if m.GetMetadataFieldStatesFunc != nil {
		return m.GetMetadataFieldStatesFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) UpsertMetadataFieldState(state *MetadataFieldState) error {
	if m.UpsertMetadataFieldStateFunc != nil {
		return m.UpsertMetadataFieldStateFunc(state)
	}
	return nil
}

func (m *MockStore) DeleteMetadataFieldState(bookID, field string) error {
	if m.DeleteMetadataFieldStateFunc != nil {
		return m.DeleteMetadataFieldStateFunc(bookID, field)
	}
	return nil
}

func (m *MockStore) RecordMetadataChange(record *MetadataChangeRecord) error {
	if m.RecordMetadataChangeFunc != nil {
		return m.RecordMetadataChangeFunc(record)
	}
	return nil
}

func (m *MockStore) GetMetadataChangeHistory(bookID string, field string, limit int) ([]MetadataChangeRecord, error) {
	if m.GetMetadataChangeHistoryFunc != nil {
		return m.GetMetadataChangeHistoryFunc(bookID, field, limit)
	}
	return nil, nil
}

func (m *MockStore) GetBookChangeHistory(bookID string, limit int) ([]MetadataChangeRecord, error) {
	if m.GetBookChangeHistoryFunc != nil {
		return m.GetBookChangeHistoryFunc(bookID, limit)
	}
	return nil, nil
}

func (m *MockStore) GetAllAuthors() ([]Author, error) {
	if m.GetAllAuthorsFunc != nil {
		return m.GetAllAuthorsFunc()
	}
	return nil, nil
}

func (m *MockStore) GetAuthorByID(id int) (*Author, error) {
	if m.GetAuthorByIDFunc != nil {
		return m.GetAuthorByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetAuthorsByIDs(ids []int) (map[int]*Author, error) {
	if m.GetAuthorsByIDsFunc != nil {
		return m.GetAuthorsByIDsFunc(ids)
	}
	return map[int]*Author{}, nil
}

func (m *MockStore) GetAuthorByName(name string) (*Author, error) {
	if m.GetAuthorByNameFunc != nil {
		return m.GetAuthorByNameFunc(name)
	}
	return nil, nil
}

func (m *MockStore) CreateAuthor(name string) (*Author, error) {
	if m.CreateAuthorFunc != nil {
		return m.CreateAuthorFunc(name)
	}
	return nil, nil
}

func (m *MockStore) DeleteAuthor(id int) error {
	if m.DeleteAuthorFunc != nil {
		return m.DeleteAuthorFunc(id)
	}
	return nil
}

func (m *MockStore) UpdateAuthorName(id int, name string) error {
	if m.UpdateAuthorNameFunc != nil {
		return m.UpdateAuthorNameFunc(id, name)
	}
	return nil
}

func (m *MockStore) GetAuthorAliases(authorID int) ([]AuthorAlias, error) {
	if m.GetAuthorAliasesFunc != nil {
		return m.GetAuthorAliasesFunc(authorID)
	}
	return []AuthorAlias{}, nil
}

func (m *MockStore) GetAllAuthorAliases() ([]AuthorAlias, error) {
	if m.GetAllAuthorAliasesFunc != nil {
		return m.GetAllAuthorAliasesFunc()
	}
	return []AuthorAlias{}, nil
}

func (m *MockStore) CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*AuthorAlias, error) {
	if m.CreateAuthorAliasFunc != nil {
		return m.CreateAuthorAliasFunc(authorID, aliasName, aliasType)
	}
	return nil, nil
}

func (m *MockStore) DeleteAuthorAlias(id int) error {
	if m.DeleteAuthorAliasFunc != nil {
		return m.DeleteAuthorAliasFunc(id)
	}
	return nil
}

func (m *MockStore) FindAuthorByAlias(aliasName string) (*Author, error) {
	if m.FindAuthorByAliasFunc != nil {
		return m.FindAuthorByAliasFunc(aliasName)
	}
	return nil, nil
}

func (m *MockStore) GetAllSeries() ([]Series, error) {
	if m.GetAllSeriesFunc != nil {
		return m.GetAllSeriesFunc()
	}
	return nil, nil
}

func (m *MockStore) GetSeriesByID(id int) (*Series, error) {
	if m.GetSeriesByIDFunc != nil {
		return m.GetSeriesByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetSeriesByIDs(ids []int) (map[int]*Series, error) {
	if m.GetSeriesByIDsFunc != nil {
		return m.GetSeriesByIDsFunc(ids)
	}
	return map[int]*Series{}, nil
}

func (m *MockStore) GetSeriesByName(name string, authorID *int) (*Series, error) {
	if m.GetSeriesByNameFunc != nil {
		return m.GetSeriesByNameFunc(name, authorID)
	}
	return nil, nil
}

func (m *MockStore) CreateSeries(name string, authorID *int) (*Series, error) {
	if m.CreateSeriesFunc != nil {
		return m.CreateSeriesFunc(name, authorID)
	}
	return nil, nil
}

func (m *MockStore) DeleteSeries(id int) error {
	if m.DeleteSeriesFunc != nil {
		return m.DeleteSeriesFunc(id)
	}
	return nil
}

func (m *MockStore) UpdateSeriesName(id int, name string) error {
	if m.UpdateSeriesNameFunc != nil {
		return m.UpdateSeriesNameFunc(id, name)
	}
	return nil
}

func (m *MockStore) GetAllWorks() ([]Work, error) {
	if m.GetAllWorksFunc != nil {
		return m.GetAllWorksFunc()
	}
	return nil, nil
}

func (m *MockStore) GetWorkByID(id string) (*Work, error) {
	if m.GetWorkByIDFunc != nil {
		return m.GetWorkByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) CreateWork(work *Work) (*Work, error) {
	if m.CreateWorkFunc != nil {
		return m.CreateWorkFunc(work)
	}
	return nil, nil
}

func (m *MockStore) UpdateWork(id string, work *Work) (*Work, error) {
	if m.UpdateWorkFunc != nil {
		return m.UpdateWorkFunc(id, work)
	}
	return nil, nil
}

func (m *MockStore) DeleteWork(id string) error {
	if m.DeleteWorkFunc != nil {
		return m.DeleteWorkFunc(id)
	}
	return nil
}

func (m *MockStore) GetBooksByWorkID(workID string) ([]Book, error) {
	if m.GetBooksByWorkIDFunc != nil {
		return m.GetBooksByWorkIDFunc(workID)
	}
	return nil, nil
}

func (m *MockStore) GetAllBooks(limit, offset int) ([]Book, error) {
	if m.GetAllBooksFunc != nil {
		return m.GetAllBooksFunc(limit, offset)
	}
	return nil, nil
}

func (m *MockStore) ListBookIDs() ([]string, error) {
	if m.ListBookIDsFunc != nil {
		return m.ListBookIDsFunc()
	}
	return nil, nil
}

func (m *MockStore) GetAllBookSummaries(limit, offset int) ([]BookSummary, error) {
	if m.GetAllBookSummariesFunc != nil {
		return m.GetAllBookSummariesFunc(limit, offset)
	}
	return nil, nil
}

func (m *MockStore) GetBookByID(id string) (*Book, error) {
	if m.GetBookByIDFunc != nil {
		return m.GetBookByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetBookByFilePath(path string) (*Book, error) {
	if m.GetBookByFilePathFunc != nil {
		return m.GetBookByFilePathFunc(path)
	}
	return nil, nil
}

func (m *MockStore) GetBookByITunesPersistentID(persistentID string) (*Book, error) {
	if m.GetBookByITunesPersistentIDFunc != nil {
		return m.GetBookByITunesPersistentIDFunc(persistentID)
	}
	return nil, nil
}

func (m *MockStore) ListBooksByITunesPID(limit, offset int) ([]Book, error) {
	if m.ListBooksByITunesPIDFunc != nil {
		return m.ListBooksByITunesPIDFunc(limit, offset)
	}
	return nil, nil
}

func (m *MockStore) GetBookByFileHash(hash string) (*Book, error) {
	if m.GetBookByFileHashFunc != nil {
		return m.GetBookByFileHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) GetBookByOriginalHash(hash string) (*Book, error) {
	if m.GetBookByOriginalHashFunc != nil {
		return m.GetBookByOriginalHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) GetBookByOrganizedHash(hash string) (*Book, error) {
	if m.GetBookByOrganizedHashFunc != nil {
		return m.GetBookByOrganizedHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) GetDuplicateBooks() ([][]Book, error) {
	if m.GetDuplicateBooksFunc != nil {
		return m.GetDuplicateBooksFunc()
	}
	return nil, nil
}

func (m *MockStore) GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]Book, error) {
	return nil, nil
}

func (m *MockStore) GetFolderDuplicates() ([][]Book, error) {
	return nil, nil
}

func (m *MockStore) GetDuplicateBooksByMetadata(threshold float64) ([][]Book, error) {
	return nil, nil
}

func (m *MockStore) GetBooksBySeriesID(seriesID int) ([]Book, error) {
	if m.GetBooksBySeriesIDFunc != nil {
		return m.GetBooksBySeriesIDFunc(seriesID)
	}
	return nil, nil
}

func (m *MockStore) GetBooksByAuthorID(authorID int) ([]Book, error) {
	if m.GetBooksByAuthorIDFunc != nil {
		return m.GetBooksByAuthorIDFunc(authorID)
	}
	return nil, nil
}

func (m *MockStore) GetBookAuthors(bookID string) ([]BookAuthor, error) {
	return nil, nil
}

func (m *MockStore) SetBookAuthors(bookID string, authors []BookAuthor) error {
	return nil
}

func (m *MockStore) GetBooksByAuthorIDWithRole(authorID int) ([]Book, error) {
	return m.GetBooksByAuthorID(authorID)
}

func (m *MockStore) GetAllAuthorBookCounts() (map[int]int, error) {
	return map[int]int{}, nil
}

func (m *MockStore) GetAllWorkBookCounts() (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *MockStore) GetAllAuthorFileCounts() (map[int]int, error) {
	return map[int]int{}, nil
}

func (m *MockStore) GetAllSeriesBookCounts() (map[int]int, error) {
	return map[int]int{}, nil
}

func (m *MockStore) GetAllSeriesFileCounts() (map[int]int, error) {
	return map[int]int{}, nil
}

func (m *MockStore) CreateNarrator(name string) (*Narrator, error) {
	return nil, nil
}

func (m *MockStore) GetNarratorByID(id int) (*Narrator, error) {
	return nil, nil
}

func (m *MockStore) GetNarratorByName(name string) (*Narrator, error) {
	return nil, nil
}

func (m *MockStore) ListNarrators() ([]Narrator, error) {
	return nil, nil
}

func (m *MockStore) GetBookNarrators(bookID string) ([]BookNarrator, error) {
	return nil, nil
}

func (m *MockStore) SetBookNarrators(bookID string, narrators []BookNarrator) error {
	return nil
}

func (m *MockStore) CreateBook(book *Book) (*Book, error) {
	if m.CreateBookFunc != nil {
		return m.CreateBookFunc(book)
	}
	return nil, nil
}

func (m *MockStore) UpdateBook(id string, book *Book) (*Book, error) {
	if m.UpdateBookFunc != nil {
		return m.UpdateBookFunc(id, book)
	}
	return nil, nil
}

func (m *MockStore) UpdateBookRating(id string, req UpdateBookRatingRequest) error {
	return m.UpdateBookRatingError
}

func (m *MockStore) DeleteBook(id string) error {
	if m.DeleteBookFunc != nil {
		return m.DeleteBookFunc(id)
	}
	return nil
}

func (m *MockStore) SearchBooks(query string, limit, offset int) ([]Book, error) {
	if m.SearchBooksFunc != nil {
		return m.SearchBooksFunc(query, limit, offset)
	}
	return nil, nil
}

func (m *MockStore) CountBooks() (int, error) {
	if m.CountBooksFunc != nil {
		return m.CountBooksFunc()
	}
	return 0, nil
}

func (m *MockStore) GetDistinctGenres() ([]string, error) {
	if m.GetDistinctGenresFunc != nil {
		return m.GetDistinctGenresFunc()
	}
	return nil, nil
}

func (m *MockStore) GetDistinctLanguages() ([]string, error) {
	if m.GetDistinctLanguagesFunc != nil {
		return m.GetDistinctLanguagesFunc()
	}
	return nil, nil
}

func (m *MockStore) CountFiles() (int, error) {
	if m.CountFilesFunc != nil {
		return m.CountFilesFunc()
	}
	return 0, nil
}

func (m *MockStore) CountAuthors() (int, error) {
	if m.CountAuthorsFunc != nil {
		return m.CountAuthorsFunc()
	}
	return 0, nil
}

func (m *MockStore) CountSeries() (int, error) {
	if m.CountSeriesFunc != nil {
		return m.CountSeriesFunc()
	}
	return 0, nil
}

func (m *MockStore) GetBookCountsByLocation(rootDir string) (int, int, error) {
	if m.GetBookCountsByLocationFunc != nil {
		return m.GetBookCountsByLocationFunc(rootDir)
	}
	return 0, 0, nil
}

func (m *MockStore) GetBookSizesByLocation(rootDir string) (int64, int64, error) {
	return 0, 0, nil
}

func (m *MockStore) GetDashboardStats() (*DashboardStats, error) {
	if m.GetDashboardStatsFunc != nil {
		return m.GetDashboardStatsFunc()
	}
	return &DashboardStats{
		StateDistribution:  map[string]int{},
		FormatDistribution: map[string]int{},
		BooksByImportPath:  map[int]int{},
		SizeByImportPath:   map[int]int64{},
	}, nil
}

func (m *MockStore) SetRootDir(rootDir string) {
	if m.SetRootDirFunc != nil {
		m.SetRootDirFunc(rootDir)
	}
}

func (m *MockStore) InvalidateLibraryStats() {
	if m.InvalidateLibraryStatsFunc != nil {
		m.InvalidateLibraryStatsFunc()
	}
}

func (m *MockStore) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]Book, error) {
	if m.ListSoftDeletedBooksFunc != nil {
		return m.ListSoftDeletedBooksFunc(limit, offset, olderThan)
	}
	return nil, nil
}

func (m *MockStore) CreateBookTombstone(book *Book) error {
	if m.CreateBookTombstoneFunc != nil {
		return m.CreateBookTombstoneFunc(book)
	}
	return nil
}

func (m *MockStore) GetBookTombstone(id string) (*Book, error) {
	if m.GetBookTombstoneFunc != nil {
		return m.GetBookTombstoneFunc(id)
	}
	return nil, nil
}

func (m *MockStore) DeleteBookTombstone(id string) error {
	if m.DeleteBookTombstoneFunc != nil {
		return m.DeleteBookTombstoneFunc(id)
	}
	return nil
}

func (m *MockStore) ListBookTombstones(limit int) ([]Book, error) {
	if m.ListBookTombstonesFunc != nil {
		return m.ListBookTombstonesFunc(limit)
	}
	return nil, nil
}

func (m *MockStore) GetBooksByVersionGroup(groupID string) ([]Book, error) {
	if m.GetBooksByVersionGroupFunc != nil {
		return m.GetBooksByVersionGroupFunc(groupID)
	}
	return nil, nil
}

func (m *MockStore) GetBooksByMetadataSourceHash(hash string) ([]Book, error) {
	if m.GetBooksByMetadataSourceHashFunc != nil {
		return m.GetBooksByMetadataSourceHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error) {
	if m.GetBookIDsByISBNASINFunc != nil {
		return m.GetBookIDsByISBNASINFunc(isbn10, isbn13, asin)
	}
	return nil, nil
}

func (m *MockStore) GetAllImportPaths() ([]ImportPath, error) {
	if m.GetAllImportPathsFunc != nil {
		return m.GetAllImportPathsFunc()
	}
	return nil, nil
}

func (m *MockStore) GetImportPathByID(id int) (*ImportPath, error) {
	if m.GetImportPathByIDFunc != nil {
		return m.GetImportPathByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetImportPathByPath(path string) (*ImportPath, error) {
	if m.GetImportPathByPathFunc != nil {
		return m.GetImportPathByPathFunc(path)
	}
	return nil, nil
}

func (m *MockStore) CreateImportPath(path, name string) (*ImportPath, error) {
	if m.CreateImportPathFunc != nil {
		return m.CreateImportPathFunc(path, name)
	}
	return nil, nil
}

func (m *MockStore) UpdateImportPath(id int, importPath *ImportPath) error {
	if m.UpdateImportPathFunc != nil {
		return m.UpdateImportPathFunc(id, importPath)
	}
	return nil
}

func (m *MockStore) DeleteImportPath(id int) error {
	if m.DeleteImportPathFunc != nil {
		return m.DeleteImportPathFunc(id)
	}
	return nil
}

func (m *MockStore) CountBooksByPathPrefix(prefix string) (int, error) {
	if m.CountBooksByPathPrefixFunc != nil {
		return m.CountBooksByPathPrefixFunc(prefix)
	}
	return 0, nil
}

func (m *MockStore) CreateOperation(id, opType string, folderPath *string) (*Operation, error) {
	if m.CreateOperationFunc != nil {
		return m.CreateOperationFunc(id, opType, folderPath)
	}
	return nil, nil
}

func (m *MockStore) GetOperationByID(id string) (*Operation, error) {
	if m.GetOperationByIDFunc != nil {
		return m.GetOperationByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetRecentOperations(limit int) ([]Operation, error) {
	if m.GetRecentOperationsFunc != nil {
		return m.GetRecentOperationsFunc(limit)
	}
	return nil, nil
}

func (m *MockStore) ListOperations(limit, offset int) ([]Operation, int, error) {
	if m.ListOperationsFunc != nil {
		return m.ListOperationsFunc(limit, offset)
	}
	return nil, 0, nil
}

func (m *MockStore) UpdateOperationStatus(id, status string, progress, total int, message string) error {
	if m.UpdateOperationStatusFunc != nil {
		return m.UpdateOperationStatusFunc(id, status, progress, total, message)
	}
	return nil
}

func (m *MockStore) UpdateOperationError(id, errorMessage string) error {
	if m.UpdateOperationErrorFunc != nil {
		return m.UpdateOperationErrorFunc(id, errorMessage)
	}
	return nil
}

func (m *MockStore) UpdateOperationResultData(id string, resultData string) error {
	if m.UpdateOperationResultDataFunc != nil {
		return m.UpdateOperationResultDataFunc(id, resultData)
	}
	return nil
}

func (m *MockStore) SaveOperationState(opID string, state []byte) error {
	if m.SaveOperationStateFunc != nil {
		return m.SaveOperationStateFunc(opID, state)
	}
	return nil
}

func (m *MockStore) GetOperationState(opID string) ([]byte, error) {
	if m.GetOperationStateFunc != nil {
		return m.GetOperationStateFunc(opID)
	}
	return nil, nil
}

func (m *MockStore) SaveOperationParams(opID string, params []byte) error {
	if m.SaveOperationParamsFunc != nil {
		return m.SaveOperationParamsFunc(opID, params)
	}
	return nil
}

func (m *MockStore) GetOperationParams(opID string) ([]byte, error) {
	if m.GetOperationParamsFunc != nil {
		return m.GetOperationParamsFunc(opID)
	}
	return nil, nil
}

func (m *MockStore) DeleteOperationState(opID string) error {
	if m.DeleteOperationStateFunc != nil {
		return m.DeleteOperationStateFunc(opID)
	}
	return nil
}

func (m *MockStore) DeleteOperationsByStatus(statuses []string) (int, error) {
	if m.DeleteOperationsByStatusFunc != nil {
		return m.DeleteOperationsByStatusFunc(statuses)
	}
	return 0, nil
}

func (m *MockStore) DeleteOperationWithLogs(id string) error {
	if m.DeleteOperationWithLogsFunc != nil {
		return m.DeleteOperationWithLogsFunc(id)
	}
	return nil
}

func (m *MockStore) GetInterruptedOperations() ([]Operation, error) {
	if m.GetInterruptedOperationsFunc != nil {
		return m.GetInterruptedOperationsFunc()
	}
	return nil, nil
}

func (m *MockStore) AddOperationLog(operationID, level, message string, details *string) error {
	if m.AddOperationLogFunc != nil {
		return m.AddOperationLogFunc(operationID, level, message, details)
	}
	return nil
}

func (m *MockStore) GetOperationLogs(operationID string) ([]OperationLog, error) {
	if m.GetOperationLogsFunc != nil {
		return m.GetOperationLogsFunc(operationID)
	}
	return nil, nil
}

func (m *MockStore) SaveOperationSummaryLog(op *OperationSummaryLog) error {
	if m.SaveOperationSummaryLogFunc != nil {
		return m.SaveOperationSummaryLogFunc(op)
	}
	return nil
}

func (m *MockStore) GetOperationSummaryLog(id string) (*OperationSummaryLog, error) {
	if m.GetOperationSummaryLogFunc != nil {
		return m.GetOperationSummaryLogFunc(id)
	}
	return nil, nil
}

func (m *MockStore) ListOperationSummaryLogs(limit, offset int) ([]OperationSummaryLog, error) {
	if m.ListOperationSummaryLogsFunc != nil {
		return m.ListOperationSummaryLogsFunc(limit, offset)
	}
	return nil, nil
}

func (m *MockStore) GetUserPreference(key string) (*UserPreference, error) {
	if m.GetUserPreferenceFunc != nil {
		return m.GetUserPreferenceFunc(key)
	}
	return nil, nil
}

func (m *MockStore) SetUserPreference(key, value string) error {
	if m.SetUserPreferenceFunc != nil {
		return m.SetUserPreferenceFunc(key, value)
	}
	return nil
}

func (m *MockStore) GetAllUserPreferences() ([]UserPreference, error) {
	if m.GetAllUserPreferencesFunc != nil {
		return m.GetAllUserPreferencesFunc()
	}
	return nil, nil
}

func (m *MockStore) GetSetting(key string) (*Setting, error) {
	if m.GetSettingFunc != nil {
		return m.GetSettingFunc(key)
	}
	return nil, nil
}

func (m *MockStore) SetSetting(key, value, typ string, isSecret bool) error {
	if m.SetSettingFunc != nil {
		return m.SetSettingFunc(key, value, typ, isSecret)
	}
	return nil
}

func (m *MockStore) GetAllSettings() ([]Setting, error) {
	if m.GetAllSettingsFunc != nil {
		return m.GetAllSettingsFunc()
	}
	return nil, nil
}

func (m *MockStore) DeleteSetting(key string) error {
	if m.DeleteSettingFunc != nil {
		return m.DeleteSettingFunc(key)
	}
	return nil
}

func (m *MockStore) CreatePlaylist(name string, seriesID *int, filePath string) (*Playlist, error) {
	if m.CreatePlaylistFunc != nil {
		return m.CreatePlaylistFunc(name, seriesID, filePath)
	}
	return nil, nil
}

func (m *MockStore) GetPlaylistByID(id int) (*Playlist, error) {
	if m.GetPlaylistByIDFunc != nil {
		return m.GetPlaylistByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetPlaylistBySeriesID(seriesID int) (*Playlist, error) {
	if m.GetPlaylistBySeriesIDFunc != nil {
		return m.GetPlaylistBySeriesIDFunc(seriesID)
	}
	return nil, nil
}

func (m *MockStore) AddPlaylistItem(playlistID, bookID, position int) error {
	if m.AddPlaylistItemFunc != nil {
		return m.AddPlaylistItemFunc(playlistID, bookID, position)
	}
	return nil
}

func (m *MockStore) GetPlaylistItems(playlistID int) ([]PlaylistItem, error) {
	if m.GetPlaylistItemsFunc != nil {
		return m.GetPlaylistItemsFunc(playlistID)
	}
	return nil, nil
}

func (m *MockStore) CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error) {
	if m.CreateUserFunc != nil {
		return m.CreateUserFunc(username, email, passwordHashAlgo, passwordHash, roles, status)
	}
	return nil, nil
}

func (m *MockStore) GetUserByID(id string) (*User, error) {
	if m.GetUserByIDFunc != nil {
		return m.GetUserByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetUserByUsername(username string) (*User, error) {
	if m.GetUserByUsernameFunc != nil {
		return m.GetUserByUsernameFunc(username)
	}
	return nil, nil
}

func (m *MockStore) GetUserByEmail(email string) (*User, error) {
	if m.GetUserByEmailFunc != nil {
		return m.GetUserByEmailFunc(email)
	}
	return nil, nil
}

func (m *MockStore) UpdateUser(user *User) error {
	if m.UpdateUserFunc != nil {
		return m.UpdateUserFunc(user)
	}
	return nil
}

func (m *MockStore) ListUsers() ([]User, error) {
	if m.ListUsersFunc != nil {
		return m.ListUsersFunc()
	}
	return nil, nil
}

func (m *MockStore) CreateSession(userID, ip, userAgent string, ttl time.Duration) (*Session, error) {
	if m.CreateSessionFunc != nil {
		return m.CreateSessionFunc(userID, ip, userAgent, ttl)
	}
	return nil, nil
}

func (m *MockStore) GetSession(id string) (*Session, error) {
	if m.GetSessionFunc != nil {
		return m.GetSessionFunc(id)
	}
	return nil, nil
}

func (m *MockStore) RevokeSession(id string) error {
	if m.RevokeSessionFunc != nil {
		return m.RevokeSessionFunc(id)
	}
	return nil
}

func (m *MockStore) ListUserSessions(userID string) ([]Session, error) {
	if m.ListUserSessionsFunc != nil {
		return m.ListUserSessionsFunc(userID)
	}
	return nil, nil
}

func (m *MockStore) DeleteExpiredSessions(now time.Time) (int, error) {
	if m.DeleteExpiredSessionsFunc != nil {
		return m.DeleteExpiredSessionsFunc(now)
	}
	return 0, nil
}

func (m *MockStore) CountUsers() (int, error) {
	if m.CountUsersFunc != nil {
		return m.CountUsersFunc()
	}
	return 0, nil
}

func (m *MockStore) GetRoleByID(id string) (*Role, error) {
	if m.GetRoleByIDFunc != nil {
		return m.GetRoleByIDFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetRoleByName(name string) (*Role, error) {
	if m.GetRoleByNameFunc != nil {
		return m.GetRoleByNameFunc(name)
	}
	return nil, nil
}

func (m *MockStore) ListRoles() ([]Role, error) {
	if m.ListRolesFunc != nil {
		return m.ListRolesFunc()
	}
	return nil, nil
}

func (m *MockStore) CreateRole(role *Role) (*Role, error) {
	if m.CreateRoleFunc != nil {
		return m.CreateRoleFunc(role)
	}
	return role, nil
}

func (m *MockStore) UpdateRole(role *Role) error {
	if m.UpdateRoleFunc != nil {
		return m.UpdateRoleFunc(role)
	}
	return nil
}

func (m *MockStore) DeleteRole(id string) error {
	if m.DeleteRoleFunc != nil {
		return m.DeleteRoleFunc(id)
	}
	return nil
}

func (m *MockStore) SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error {
	if m.SetUserPositionFunc != nil {
		return m.SetUserPositionFunc(userID, bookID, segmentID, positionSeconds)
	}
	return nil
}

func (m *MockStore) GetUserPosition(userID, bookID string) (*UserPosition, error) {
	if m.GetUserPositionFunc != nil {
		return m.GetUserPositionFunc(userID, bookID)
	}
	return nil, nil
}

func (m *MockStore) ListUserPositionsForBook(userID, bookID string) ([]UserPosition, error) {
	if m.ListUserPositionsForBookFunc != nil {
		return m.ListUserPositionsForBookFunc(userID, bookID)
	}
	return nil, nil
}

func (m *MockStore) ClearUserPositions(userID, bookID string) error {
	if m.ClearUserPositionsFunc != nil {
		return m.ClearUserPositionsFunc(userID, bookID)
	}
	return nil
}

func (m *MockStore) SetUserBookState(state *UserBookState) error {
	if m.SetUserBookStateFunc != nil {
		return m.SetUserBookStateFunc(state)
	}
	return nil
}

func (m *MockStore) GetUserBookState(userID, bookID string) (*UserBookState, error) {
	if m.GetUserBookStateFunc != nil {
		return m.GetUserBookStateFunc(userID, bookID)
	}
	return nil, nil
}

func (m *MockStore) ListUserBookStatesByStatus(userID, status string, limit, offset int) ([]UserBookState, error) {
	if m.ListUserBookStatesByStatusFunc != nil {
		return m.ListUserBookStatesByStatusFunc(userID, status, limit, offset)
	}
	return nil, nil
}

func (m *MockStore) ListUserPositionsSince(userID string, t time.Time) ([]UserPosition, error) {
	if m.ListUserPositionsSinceFunc != nil {
		return m.ListUserPositionsSinceFunc(userID, t)
	}
	return nil, nil
}

func (m *MockStore) CreateBookVersion(v *BookVersion) (*BookVersion, error) {
	if m.CreateBookVersionFunc != nil {
		return m.CreateBookVersionFunc(v)
	}
	return v, nil
}

func (m *MockStore) GetBookVersion(id string) (*BookVersion, error) {
	if m.GetBookVersionFunc != nil {
		return m.GetBookVersionFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetBookVersionsByBookID(bookID string) ([]BookVersion, error) {
	if m.GetBookVersionsByBookIDFunc != nil {
		return m.GetBookVersionsByBookIDFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) GetActiveVersionForBook(bookID string) (*BookVersion, error) {
	if m.GetActiveVersionForBookFunc != nil {
		return m.GetActiveVersionForBookFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) UpdateBookVersion(v *BookVersion) error {
	if m.UpdateBookVersionFunc != nil {
		return m.UpdateBookVersionFunc(v)
	}
	return nil
}

func (m *MockStore) DeleteBookVersion(id string) error {
	if m.DeleteBookVersionFunc != nil {
		return m.DeleteBookVersionFunc(id)
	}
	return nil
}

func (m *MockStore) GetBookVersionByTorrentHash(hash string) (*BookVersion, error) {
	if m.GetBookVersionByTorrentHashFunc != nil {
		return m.GetBookVersionByTorrentHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) ListTrashedBookVersions() ([]BookVersion, error) {
	if m.ListTrashedBookVersionsFunc != nil {
		return m.ListTrashedBookVersionsFunc()
	}
	return nil, nil
}

func (m *MockStore) ListPurgedBookVersions() ([]BookVersion, error) {
	if m.ListPurgedBookVersionsFunc != nil {
		return m.ListPurgedBookVersionsFunc()
	}
	return nil, nil
}

// ---- User playlists (spec 3.4) ----

func (m *MockStore) CreateUserPlaylist(pl *UserPlaylist) (*UserPlaylist, error) {
	if m.CreateUserPlaylistFunc != nil {
		return m.CreateUserPlaylistFunc(pl)
	}
	return pl, nil
}

func (m *MockStore) GetUserPlaylist(id string) (*UserPlaylist, error) {
	if m.GetUserPlaylistFunc != nil {
		return m.GetUserPlaylistFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetUserPlaylistByName(name string) (*UserPlaylist, error) {
	if m.GetUserPlaylistByNameFunc != nil {
		return m.GetUserPlaylistByNameFunc(name)
	}
	return nil, nil
}

func (m *MockStore) GetUserPlaylistByITunesPID(pid string) (*UserPlaylist, error) {
	if m.GetUserPlaylistByITunesPIDFunc != nil {
		return m.GetUserPlaylistByITunesPIDFunc(pid)
	}
	return nil, nil
}

func (m *MockStore) ListUserPlaylists(playlistType string, limit, offset int) ([]UserPlaylist, int, error) {
	if m.ListUserPlaylistsFunc != nil {
		return m.ListUserPlaylistsFunc(playlistType, limit, offset)
	}
	return nil, 0, nil
}

func (m *MockStore) ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]UserPlaylist, int, error) {
	if m.ListUserPlaylistsForUserFunc != nil {
		return m.ListUserPlaylistsForUserFunc(userID, playlistType, limit, offset)
	}
	return nil, 0, nil
}

func (m *MockStore) UpdateUserPlaylist(pl *UserPlaylist) error {
	if m.UpdateUserPlaylistFunc != nil {
		return m.UpdateUserPlaylistFunc(pl)
	}
	return nil
}

func (m *MockStore) DeleteUserPlaylist(id string) error {
	if m.DeleteUserPlaylistFunc != nil {
		return m.DeleteUserPlaylistFunc(id)
	}
	return nil
}

func (m *MockStore) ListDirtyUserPlaylists() ([]UserPlaylist, error) {
	if m.ListDirtyUserPlaylistsFunc != nil {
		return m.ListDirtyUserPlaylistsFunc()
	}
	return nil, nil
}

func (m *MockStore) CreateAPIKey(key *APIKey) (*APIKey, error) {
	if m.CreateAPIKeyFunc != nil {
		return m.CreateAPIKeyFunc(key)
	}
	return key, nil
}

func (m *MockStore) GetAPIKey(id string) (*APIKey, error) {
	if m.GetAPIKeyFunc != nil {
		return m.GetAPIKeyFunc(id)
	}
	return nil, nil
}

func (m *MockStore) GetAPIKeyByHash(hash string) (*APIKey, error) {
	if m.GetAPIKeyByHashFunc != nil {
		return m.GetAPIKeyByHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) ListAPIKeysForUser(userID string) ([]APIKey, error) {
	if m.ListAPIKeysForUserFunc != nil {
		return m.ListAPIKeysForUserFunc(userID)
	}
	return nil, nil
}

func (m *MockStore) ListAllAPIKeys() ([]APIKey, error) {
	if m.ListAllAPIKeysFunc != nil {
		return m.ListAllAPIKeysFunc()
	}
	return nil, nil
}

func (m *MockStore) RevokeAPIKey(id string) error {
	if m.RevokeAPIKeyFunc != nil {
		return m.RevokeAPIKeyFunc(id)
	}
	return nil
}

func (m *MockStore) SetAPIKeyStatus(id, status string, at time.Time) error {
	if m.SetAPIKeyStatusFunc != nil {
		return m.SetAPIKeyStatusFunc(id, status, at)
	}
	return nil
}

func (m *MockStore) TouchAPIKeyLastUsed(id string, at time.Time, ip string) error {
	if m.TouchAPIKeyLastUsedFunc != nil {
		return m.TouchAPIKeyLastUsedFunc(id, at, ip)
	}
	return nil
}

func (m *MockStore) CreateInvite(invite *Invite) (*Invite, error) {
	if m.CreateInviteFunc != nil {
		return m.CreateInviteFunc(invite)
	}
	return invite, nil
}

func (m *MockStore) GetInvite(token string) (*Invite, error) {
	if m.GetInviteFunc != nil {
		return m.GetInviteFunc(token)
	}
	return nil, nil
}

func (m *MockStore) ListActiveInvites() ([]Invite, error) {
	if m.ListActiveInvitesFunc != nil {
		return m.ListActiveInvitesFunc()
	}
	return nil, nil
}

func (m *MockStore) DeleteInvite(token string) error {
	if m.DeleteInviteFunc != nil {
		return m.DeleteInviteFunc(token)
	}
	return nil
}

func (m *MockStore) ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error) {
	if m.ConsumeInviteFunc != nil {
		return m.ConsumeInviteFunc(token, passwordHashAlgo, passwordHash)
	}
	return nil, nil
}

func (m *MockStore) SetUserPreferenceForUser(userID, key, value string) error {
	if m.SetUserPreferenceForUserFunc != nil {
		return m.SetUserPreferenceForUserFunc(userID, key, value)
	}
	return nil
}

func (m *MockStore) GetUserPreferenceForUser(userID, key string) (*UserPreferenceKV, error) {
	if m.GetUserPreferenceForUserFunc != nil {
		return m.GetUserPreferenceForUserFunc(userID, key)
	}
	return nil, nil
}

func (m *MockStore) GetAllPreferencesForUser(userID string) ([]UserPreferenceKV, error) {
	if m.GetAllPreferencesForUserFunc != nil {
		return m.GetAllPreferencesForUserFunc(userID)
	}
	return nil, nil
}

func (m *MockStore) CreateBookSegment(bookNumericID int, segment *BookSegment) (*BookSegment, error) {
	if m.CreateBookSegmentFunc != nil {
		return m.CreateBookSegmentFunc(bookNumericID, segment)
	}
	return nil, nil
}

func (m *MockStore) UpdateBookSegment(segment *BookSegment) error {
	if m.UpdateBookSegmentFunc != nil {
		return m.UpdateBookSegmentFunc(segment)
	}
	return nil
}

func (m *MockStore) ListBookSegments(bookNumericID int) ([]BookSegment, error) {
	if m.ListBookSegmentsFunc != nil {
		return m.ListBookSegmentsFunc(bookNumericID)
	}
	return nil, nil
}

func (m *MockStore) MergeBookSegments(bookNumericID int, newSegment *BookSegment, supersedeIDs []string) error {
	if m.MergeBookSegmentsFunc != nil {
		return m.MergeBookSegmentsFunc(bookNumericID, newSegment, supersedeIDs)
	}
	return nil
}

func (m *MockStore) GetBookSegmentByID(segmentID string) (*BookSegment, error) {
	if m.GetBookSegmentByIDFunc != nil {
		return m.GetBookSegmentByIDFunc(segmentID)
	}
	return nil, fmt.Errorf("segment not found: %s", segmentID)
}

func (m *MockStore) MoveSegmentsToBook(segmentIDs []string, targetBookNumericID int) error {
	if m.MoveSegmentsToBookFunc != nil {
		return m.MoveSegmentsToBookFunc(segmentIDs, targetBookNumericID)
	}
	return nil
}

func (m *MockStore) AddPlaybackEvent(event *PlaybackEvent) error {
	if m.AddPlaybackEventFunc != nil {
		return m.AddPlaybackEventFunc(event)
	}
	return nil
}

func (m *MockStore) ListPlaybackEvents(userID string, bookNumericID int, limit int) ([]PlaybackEvent, error) {
	if m.ListPlaybackEventsFunc != nil {
		return m.ListPlaybackEventsFunc(userID, bookNumericID, limit)
	}
	return nil, nil
}

func (m *MockStore) UpdatePlaybackProgress(progress *PlaybackProgress) error {
	if m.UpdatePlaybackProgressFunc != nil {
		return m.UpdatePlaybackProgressFunc(progress)
	}
	return nil
}

func (m *MockStore) GetPlaybackProgress(userID string, bookNumericID int) (*PlaybackProgress, error) {
	if m.GetPlaybackProgressFunc != nil {
		return m.GetPlaybackProgressFunc(userID, bookNumericID)
	}
	return nil, nil
}

func (m *MockStore) IncrementBookPlayStats(bookNumericID int, seconds int) error {
	if m.IncrementBookPlayStatsFunc != nil {
		return m.IncrementBookPlayStatsFunc(bookNumericID, seconds)
	}
	return nil
}

func (m *MockStore) GetBookStats(bookNumericID int) (*BookStats, error) {
	if m.GetBookStatsFunc != nil {
		return m.GetBookStatsFunc(bookNumericID)
	}
	return nil, nil
}

func (m *MockStore) IncrementUserListenStats(userID string, seconds int) error {
	if m.IncrementUserListenStatsFunc != nil {
		return m.IncrementUserListenStatsFunc(userID, seconds)
	}
	return nil
}

func (m *MockStore) GetUserStats(userID string) (*UserStats, error) {
	if m.GetUserStatsFunc != nil {
		return m.GetUserStatsFunc(userID)
	}
	return nil, nil
}

func (m *MockStore) IsHashBlocked(hash string) (bool, error) {
	if m.IsHashBlockedFunc != nil {
		return m.IsHashBlockedFunc(hash)
	}
	return false, nil
}

func (m *MockStore) AddBlockedHash(hash, reason string) error {
	if m.AddBlockedHashFunc != nil {
		return m.AddBlockedHashFunc(hash, reason)
	}
	return nil
}

func (m *MockStore) RemoveBlockedHash(hash string) error {
	if m.RemoveBlockedHashFunc != nil {
		return m.RemoveBlockedHashFunc(hash)
	}
	return nil
}

func (m *MockStore) GetAllBlockedHashes() ([]DoNotImport, error) {
	if m.GetAllBlockedHashesFunc != nil {
		return m.GetAllBlockedHashesFunc()
	}
	return nil, nil
}

func (m *MockStore) GetBlockedHashByHash(hash string) (*DoNotImport, error) {
	if m.GetBlockedHashByHashFunc != nil {
		return m.GetBlockedHashByHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) SaveLibraryFingerprint(path string, size int64, modTime time.Time, crc32 uint32) error {
	if m.SaveLibraryFingerprintFunc != nil {
		return m.SaveLibraryFingerprintFunc(path, size, modTime, crc32)
	}
	return nil
}

func (m *MockStore) GetLibraryFingerprint(path string) (*LibraryFingerprintRecord, error) {
	if m.GetLibraryFingerprintFunc != nil {
		return m.GetLibraryFingerprintFunc(path)
	}
	return nil, nil
}

func (m *MockStore) Reset() error {
	if m.ResetFunc != nil {
		return m.ResetFunc()
	}
	return nil
}

func (m *MockStore) SetLastWrittenAt(id string, t time.Time) error {
	return nil
}

func (m *MockStore) GetBookSnapshots(id string, limit int) ([]BookSnapshot, error) {
	if m.GetBookVersionsFunc != nil {
		return m.GetBookVersionsFunc(id, limit)
	}
	return nil, nil
}

func (m *MockStore) GetBookAtVersion(id string, ts time.Time) (*Book, error) {
	if m.GetBookAtVersionFunc != nil {
		return m.GetBookAtVersionFunc(id, ts)
	}
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockStore) RevertBookToVersion(id string, ts time.Time) (*Book, error) {
	if m.RevertBookToVersionFunc != nil {
		return m.RevertBookToVersionFunc(id, ts)
	}
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *MockStore) PruneBookSnapshots(id string, keepCount int) (int, error) {
	if m.PruneBookVersionsFunc != nil {
		return m.PruneBookVersionsFunc(id, keepCount)
	}
	return 0, nil
}

func (m *MockStore) MarkITunesSynced(bookIDs []string) (int64, error) {
	return int64(len(bookIDs)), nil
}
func (m *MockStore) GetITunesDirtyBooks() ([]Book, error)                  { return nil, nil }
func (m *MockStore) GetITunesPurgePendingBooks() ([]Book, error)           { return nil, nil }
func (m *MockStore) GetQuarantinedBooks(limit, offset int) ([]Book, error) { return nil, nil }
func (m *MockStore) CountQuarantinedBooks() (int, error)                   { return 0, nil }
func (m *MockStore) GetScanFailCount(pathHash string) (int, error)         { return 0, nil }
func (m *MockStore) IncrScanFailCount(pathHash string) (int, error)        { return 1, nil }
func (m *MockStore) ResetScanFailCount(pathHash string) error              { return nil }
func (m *MockStore) MergeChapterBooks(_ string, _ []string, _ string, _ float64) error {
	return nil
}

func (m *MockStore) FlagMetadataHashDuplicate(_, _ string) error { return nil }

func (m *MockStore) RecomputeBookAggregates(_ string) error { return nil }

func (m *MockStore) Optimize() error {
	return nil
}

func (m *MockStore) CreateOperationChange(change *OperationChange) error {
	return nil
}

func (m *MockStore) GetOperationChanges(operationID string) ([]*OperationChange, error) {
	return nil, nil
}

func (m *MockStore) GetBookChanges(bookID string) ([]*OperationChange, error) {
	return nil, nil
}

func (m *MockStore) RevertOperationChanges(operationID string) error {
	return nil
}

func (m *MockStore) CreateAuthorTombstone(oldID, canonicalID int) error {
	if m.CreateAuthorTombstoneFunc != nil {
		return m.CreateAuthorTombstoneFunc(oldID, canonicalID)
	}
	return nil
}

func (m *MockStore) GetAuthorTombstone(oldID int) (int, error) {
	if m.GetAuthorTombstoneFunc != nil {
		return m.GetAuthorTombstoneFunc(oldID)
	}
	return 0, nil
}

func (m *MockStore) ResolveTombstoneChains() (int, error) {
	if m.ResolveTombstoneChainsFunc != nil {
		return m.ResolveTombstoneChainsFunc()
	}
	return 0, nil
}

func (m *MockStore) AddSystemActivityLog(source, level, message string) error {
	if m.AddSystemActivityLogFunc != nil {
		return m.AddSystemActivityLogFunc(source, level, message)
	}
	return nil
}

func (m *MockStore) GetSystemActivityLogs(source string, limit int) ([]SystemActivityLog, error) {
	if m.GetSystemActivityLogsFunc != nil {
		return m.GetSystemActivityLogsFunc(source, limit)
	}
	return nil, nil
}

func (m *MockStore) PruneOperationLogs(olderThan time.Time) (int, error) {
	if m.PruneOperationLogsFunc != nil {
		return m.PruneOperationLogsFunc(olderThan)
	}
	return 0, nil
}

func (m *MockStore) PruneOperationChanges(olderThan time.Time) (int, error) {
	if m.PruneOperationChangesFunc != nil {
		return m.PruneOperationChangesFunc(olderThan)
	}
	return 0, nil
}

func (m *MockStore) PruneSystemActivityLogs(olderThan time.Time) (int, error) {
	if m.PruneSystemActivityLogsFunc != nil {
		return m.PruneSystemActivityLogsFunc(olderThan)
	}
	return 0, nil
}

func (m *MockStore) GetScanCacheMap() (map[string]ScanCacheEntry, error) {
	if m.GetScanCacheMapFunc != nil {
		return m.GetScanCacheMapFunc()
	}
	return nil, nil
}

func (m *MockStore) UpdateScanCache(bookID string, mtime int64, size int64) error {
	if m.UpdateScanCacheFunc != nil {
		return m.UpdateScanCacheFunc(bookID, mtime, size)
	}
	return nil
}

func (m *MockStore) MarkNeedsRescan(bookID string) error {
	if m.MarkNeedsRescanFunc != nil {
		return m.MarkNeedsRescanFunc(bookID)
	}
	return nil
}

func (m *MockStore) GetDirtyBookFolders() ([]string, error) {
	if m.GetDirtyBookFoldersFunc != nil {
		return m.GetDirtyBookFoldersFunc()
	}
	return nil, nil
}

func (m *MockStore) CreateDeferredITunesUpdate(bookID, persistentID, oldPath, newPath, updateType string) error {
	return nil
}

func (m *MockStore) GetPendingDeferredITunesUpdates() ([]DeferredITunesUpdate, error) {
	return nil, nil
}

func (m *MockStore) MarkDeferredITunesUpdateApplied(id int) error {
	return nil
}

func (m *MockStore) GetDeferredITunesUpdatesByBookID(bookID string) ([]DeferredITunesUpdate, error) {
	return nil, nil
}

func (m *MockStore) CreateExternalIDMapping(mapping *ExternalIDMapping) error {
	if m.CreateExternalIDMappingFunc != nil {
		return m.CreateExternalIDMappingFunc(mapping)
	}
	return nil
}

func (m *MockStore) GetBookByExternalID(source, externalID string) (string, error) {
	if m.GetBookByExternalIDFunc != nil {
		return m.GetBookByExternalIDFunc(source, externalID)
	}
	return "", nil
}

func (m *MockStore) GetExternalIDsForBook(bookID string) ([]ExternalIDMapping, error) {
	if m.GetExternalIDsForBookFunc != nil {
		return m.GetExternalIDsForBookFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) IsExternalIDTombstoned(source, externalID string) (bool, error) {
	if m.IsExternalIDTombstonedFunc != nil {
		return m.IsExternalIDTombstonedFunc(source, externalID)
	}
	return false, nil
}

func (m *MockStore) TombstoneExternalID(source, externalID string) error {
	if m.TombstoneExternalIDFunc != nil {
		return m.TombstoneExternalIDFunc(source, externalID)
	}
	return nil
}

func (m *MockStore) ReassignExternalIDs(oldBookID, newBookID string) error {
	if m.ReassignExternalIDsFunc != nil {
		return m.ReassignExternalIDsFunc(oldBookID, newBookID)
	}
	return nil
}

func (m *MockStore) BulkCreateExternalIDMappings(mappings []ExternalIDMapping) error {
	if m.BulkCreateExternalIDMappingsFunc != nil {
		return m.BulkCreateExternalIDMappingsFunc(mappings)
	}
	return nil
}

func (m *MockStore) MarkExternalIDRemoved(source, externalID string) error { return nil }
func (m *MockStore) SetExternalIDProvenance(source, externalID, provenance string) error {
	return nil
}
func (m *MockStore) GetRemovedExternalIDs(source string) ([]ExternalIDMapping, error) {
	return nil, nil
}

func (m *MockStore) SetRaw(key string, value []byte) error {
	if m.SetRawFunc != nil {
		return m.SetRawFunc(key, value)
	}
	return nil
}
func (m *MockStore) GetRaw(key string) ([]byte, error) {
	if m.GetRawFunc != nil {
		return m.GetRawFunc(key)
	}
	return nil, nil
}
func (m *MockStore) DeleteRaw(key string) error {
	if m.DeleteRawFunc != nil {
		return m.DeleteRawFunc(key)
	}
	return nil
}
func (m *MockStore) ScanPrefix(prefix string) ([]KVPair, error) {
	if m.ScanPrefixFunc != nil {
		return m.ScanPrefixFunc(prefix)
	}
	return nil, nil
}
func (m *MockStore) CountPrefix(prefix string) (int64, error) {
	if m.CountPrefixFunc != nil {
		return m.CountPrefixFunc(prefix)
	}
	return 0, nil
}
func (m *MockStore) CreateOperationResult(result *OperationResult) error { return nil }
func (m *MockStore) GetOperationResults(operationID string) ([]OperationResult, error) {
	return nil, nil
}
func (m *MockStore) GetOperationResultsPage(operationID string, limit, offset int) ([]OperationResult, int, error) {
	return nil, 0, nil
}
func (m *MockStore) GetRecentCompletedOperations(limit int) ([]Operation, error) {
	return nil, nil
}

func (m *MockStore) GetBookUserTags(bookID string) ([]string, error)    { return nil, nil }
func (m *MockStore) SetBookUserTags(bookID string, tags []string) error { return nil }
func (m *MockStore) AddBookUserTag(bookID string, tag string) error     { return nil }
func (m *MockStore) RemoveBookUserTag(bookID string, tag string) error  { return nil }

func (m *MockStore) GetBookAlternativeTitles(bookID string) ([]BookAlternativeTitle, error) {
	return nil, nil
}
func (m *MockStore) AddBookAlternativeTitle(bookID, title, source, language string) error { return nil }
func (m *MockStore) RemoveBookAlternativeTitle(bookID, title string) error                { return nil }
func (m *MockStore) SetBookAlternativeTitles(bookID string, titles []BookAlternativeTitle) error {
	return nil
}

func (m *MockStore) RecordPathChange(change *BookPathChange) error {
	if m.RecordPathChangeFunc != nil {
		return m.RecordPathChangeFunc(change)
	}
	return nil
}

func (m *MockStore) GetBookPathHistory(bookID string) ([]BookPathChange, error) {
	if m.GetBookPathHistoryFunc != nil {
		return m.GetBookPathHistoryFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) AddBookTag(bookID, tag string) error {
	if m.AddBookTagFunc != nil {
		return m.AddBookTagFunc(bookID, tag)
	}
	return nil
}

func (m *MockStore) RemoveBookTag(bookID, tag string) error {
	if m.RemoveBookTagFunc != nil {
		return m.RemoveBookTagFunc(bookID, tag)
	}
	return nil
}

func (m *MockStore) GetBookTags(bookID string) ([]string, error) {
	if m.GetBookTagsFunc != nil {
		return m.GetBookTagsFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) SetBookTags(bookID string, tags []string) error {
	if m.SetBookTagsFunc != nil {
		return m.SetBookTagsFunc(bookID, tags)
	}
	return nil
}

func (m *MockStore) ListAllTags() ([]TagWithCount, error) {
	if m.ListAllTagsFunc != nil {
		return m.ListAllTagsFunc()
	}
	return nil, nil
}

func (m *MockStore) GetBooksByTag(tag string) ([]string, error) {
	if m.GetBooksByTagFunc != nil {
		return m.GetBooksByTagFunc(tag)
	}
	return nil, nil
}

func (m *MockStore) AddBookTagWithSource(bookID, tag, source string) error {
	if m.AddBookTagWithSourceFunc != nil {
		return m.AddBookTagWithSourceFunc(bookID, tag, source)
	}
	if m.AddBookTagFunc != nil {
		return m.AddBookTagFunc(bookID, tag)
	}
	return nil
}

func (m *MockStore) RemoveBookTagsByPrefix(bookID, prefix, source string) error {
	if m.RemoveBookTagsByPrefixFunc != nil {
		return m.RemoveBookTagsByPrefixFunc(bookID, prefix, source)
	}
	return nil
}

func (m *MockStore) GetBookTagsDetailed(bookID string) ([]BookTag, error) {
	if m.GetBookTagsDetailedFunc != nil {
		return m.GetBookTagsDetailedFunc(bookID)
	}
	return nil, nil
}

// ---- Author tag methods ----

func (m *MockStore) AddAuthorTag(authorID int, tag string) error {
	if m.AddAuthorTagFunc != nil {
		return m.AddAuthorTagFunc(authorID, tag)
	}
	return nil
}

func (m *MockStore) AddAuthorTagWithSource(authorID int, tag, source string) error {
	if m.AddAuthorTagWithSourceFunc != nil {
		return m.AddAuthorTagWithSourceFunc(authorID, tag, source)
	}
	if m.AddAuthorTagFunc != nil {
		return m.AddAuthorTagFunc(authorID, tag)
	}
	return nil
}

func (m *MockStore) RemoveAuthorTag(authorID int, tag string) error {
	if m.RemoveAuthorTagFunc != nil {
		return m.RemoveAuthorTagFunc(authorID, tag)
	}
	return nil
}

func (m *MockStore) RemoveAuthorTagsByPrefix(authorID int, prefix, source string) error {
	if m.RemoveAuthorTagsByPrefixFunc != nil {
		return m.RemoveAuthorTagsByPrefixFunc(authorID, prefix, source)
	}
	return nil
}

func (m *MockStore) GetAuthorTags(authorID int) ([]string, error) {
	if m.GetAuthorTagsFunc != nil {
		return m.GetAuthorTagsFunc(authorID)
	}
	return nil, nil
}

func (m *MockStore) GetAuthorTagsDetailed(authorID int) ([]BookTag, error) {
	if m.GetAuthorTagsDetailedFunc != nil {
		return m.GetAuthorTagsDetailedFunc(authorID)
	}
	return nil, nil
}

func (m *MockStore) SetAuthorTags(authorID int, tags []string) error {
	if m.SetAuthorTagsFunc != nil {
		return m.SetAuthorTagsFunc(authorID, tags)
	}
	return nil
}

func (m *MockStore) ListAllAuthorTags() ([]TagWithCount, error) {
	if m.ListAllAuthorTagsFunc != nil {
		return m.ListAllAuthorTagsFunc()
	}
	return nil, nil
}

func (m *MockStore) GetAuthorsByTag(tag string) ([]int, error) {
	if m.GetAuthorsByTagFunc != nil {
		return m.GetAuthorsByTagFunc(tag)
	}
	return nil, nil
}

// ---- Series tag methods ----

func (m *MockStore) AddSeriesTag(seriesID int, tag string) error {
	if m.AddSeriesTagFunc != nil {
		return m.AddSeriesTagFunc(seriesID, tag)
	}
	return nil
}

func (m *MockStore) AddSeriesTagWithSource(seriesID int, tag, source string) error {
	if m.AddSeriesTagWithSourceFunc != nil {
		return m.AddSeriesTagWithSourceFunc(seriesID, tag, source)
	}
	if m.AddSeriesTagFunc != nil {
		return m.AddSeriesTagFunc(seriesID, tag)
	}
	return nil
}

func (m *MockStore) RemoveSeriesTag(seriesID int, tag string) error {
	if m.RemoveSeriesTagFunc != nil {
		return m.RemoveSeriesTagFunc(seriesID, tag)
	}
	return nil
}

func (m *MockStore) RemoveSeriesTagsByPrefix(seriesID int, prefix, source string) error {
	if m.RemoveSeriesTagsByPrefixFunc != nil {
		return m.RemoveSeriesTagsByPrefixFunc(seriesID, prefix, source)
	}
	return nil
}

func (m *MockStore) GetSeriesTags(seriesID int) ([]string, error) {
	if m.GetSeriesTagsFunc != nil {
		return m.GetSeriesTagsFunc(seriesID)
	}
	return nil, nil
}

func (m *MockStore) GetSeriesTagsDetailed(seriesID int) ([]BookTag, error) {
	if m.GetSeriesTagsDetailedFunc != nil {
		return m.GetSeriesTagsDetailedFunc(seriesID)
	}
	return nil, nil
}

func (m *MockStore) SetSeriesTags(seriesID int, tags []string) error {
	if m.SetSeriesTagsFunc != nil {
		return m.SetSeriesTagsFunc(seriesID, tags)
	}
	return nil
}

func (m *MockStore) ListAllSeriesTags() ([]TagWithCount, error) {
	if m.ListAllSeriesTagsFunc != nil {
		return m.ListAllSeriesTagsFunc()
	}
	return nil, nil
}

func (m *MockStore) GetSeriesByTag(tag string) ([]int, error) {
	if m.GetSeriesByTagFunc != nil {
		return m.GetSeriesByTagFunc(tag)
	}
	return nil, nil
}

// ---- BookFile methods ----

func (m *MockStore) CreateBookFile(file *BookFile) error {
	if m.CreateBookFileFunc != nil {
		return m.CreateBookFileFunc(file)
	}
	return nil
}
func (m *MockStore) UpdateBookFile(id string, file *BookFile) error {
	if m.UpdateBookFileFunc != nil {
		return m.UpdateBookFileFunc(id, file)
	}
	return nil
}
func (m *MockStore) GetBookFiles(bookID string) ([]BookFile, error) {
	if m.GetBookFilesFunc != nil {
		return m.GetBookFilesFunc(bookID)
	}
	return nil, nil
}
func (m *MockStore) GetAllBookFiles() ([]BookFile, error) {
	if m.GetAllBookFilesFunc != nil {
		return m.GetAllBookFilesFunc()
	}
	return nil, nil
}
func (m *MockStore) GetBookFilesNeedingDelugeImport() ([]BookFile, error) { return nil, nil }

func (m *MockStore) MarkFileImportedFromDeluge(ctx context.Context, originalPath, libraryPath, torrentHash string) error {
	if m.MarkFileImportedFromDelugeFunc != nil {
		return m.MarkFileImportedFromDelugeFunc(ctx, originalPath, libraryPath, torrentHash)
	}
	return nil
}

func (m *MockStore) GetBookFileByID(bookID, fileID string) (*BookFile, error) {
	if m.GetBookFileByIDFunc != nil {
		return m.GetBookFileByIDFunc(bookID, fileID)
	}
	return nil, nil
}
func (m *MockStore) GetBookFileByPID(itunesPID string) (*BookFile, error) {
	if m.GetBookFileByPIDFunc != nil {
		return m.GetBookFileByPIDFunc(itunesPID)
	}
	return nil, nil
}
func (m *MockStore) ClearITunesPID(itunesPID string) (bool, error) {
	if m.ClearITunesPIDFunc != nil {
		return m.ClearITunesPIDFunc(itunesPID)
	}
	return false, nil
}
func (m *MockStore) GetBookFileByPath(filePath string) (*BookFile, error) {
	if m.GetBookFileByPathFunc != nil {
		return m.GetBookFileByPathFunc(filePath)
	}
	return nil, nil
}
func (m *MockStore) GetBookFileByAcoustID(fp string) (*BookFile, error) {
	if m.GetBookFileByAcoustIDFunc != nil {
		return m.GetBookFileByAcoustIDFunc(fp)
	}
	return nil, nil
}
func (m *MockStore) GetBookFileByAcoustIDFuzzy(fp string, minSimilarity float64) (*BookFile, error) {
	if m.GetBookFileByAcoustIDFuzzyFunc != nil {
		return m.GetBookFileByAcoustIDFuzzyFunc(fp, minSimilarity)
	}
	return nil, nil
}
func (m *MockStore) DeleteBookFile(id string) error {
	if m.DeleteBookFileFunc != nil {
		return m.DeleteBookFileFunc(id)
	}
	return nil
}
func (m *MockStore) DeleteBookFilesForBook(bookID string) error {
	if m.DeleteBookFilesForBookFunc != nil {
		return m.DeleteBookFilesForBookFunc(bookID)
	}
	return nil
}
func (m *MockStore) UpsertBookFile(file *BookFile) error {
	if m.UpsertBookFileFunc != nil {
		return m.UpsertBookFileFunc(file)
	}
	return nil
}
func (m *MockStore) BatchUpsertBookFiles(files []*BookFile) error {
	if m.BatchUpsertBookFilesFunc != nil {
		return m.BatchUpsertBookFilesFunc(files)
	}
	return nil
}
func (m *MockStore) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	if m.MoveBookFilesToBookFunc != nil {
		return m.MoveBookFilesToBookFunc(fileIDs, sourceBookID, targetBookID)
	}
	return nil
}

func (m *MockStore) GetDuplicateFilesByHash(limit int) ([]DuplicateFileGroup, error) {
	if m.GetDuplicateFilesByHashFunc != nil {
		return m.GetDuplicateFilesByHashFunc(limit)
	}
	return nil, nil
}

func (m *MockStore) GetBookBySegmentFileHash(hash string) (*Book, error) {
	if m.GetBookBySegmentFileHashFunc != nil {
		return m.GetBookBySegmentFileHashFunc(hash)
	}
	return nil, nil
}

func (m *MockStore) GetBookFileHashStats() (*BookFileHashStats, error) {
	if m.GetBookFileHashStatsFunc != nil {
		return m.GetBookFileHashStatsFunc()
	}
	return &BookFileHashStats{}, nil
}

func (m *MockStore) GetBookMetadataHashStats() (*BookMetadataHashStats, error) {
	if m.GetBookMetadataHashStatsFunc != nil {
		return m.GetBookMetadataHashStatsFunc()
	}
	return &BookMetadataHashStats{}, nil
}

func (m *MockStore) GetFilesWithFingerprintFailures(reason string, limit, offset int) ([]BookFile, int64, error) {
	if m.GetFilesWithFingerprintFailuresFunc != nil {
		return m.GetFilesWithFingerprintFailuresFunc(reason, limit, offset)
	}
	return nil, 0, nil
}

func (m *MockStore) GetAcoustIDStats() (*AcoustIDStats, error) {
	if m.GetAcoustIDStatsFunc != nil {
		return m.GetAcoustIDStatsFunc()
	}
	return &AcoustIDStats{}, nil
}

func (m *MockStore) CreateAIJob(job AIJob, payloadJSON []byte) error {
	if m.CreateAIJobFunc != nil {
		return m.CreateAIJobFunc(job, payloadJSON)
	}
	return nil
}

func (m *MockStore) GetAIJob(id string) (AIJob, error) {
	if m.GetAIJobFunc != nil {
		return m.GetAIJobFunc(id)
	}
	return AIJob{}, nil
}

func (m *MockStore) GetAIJobByBatchID(batchID string) (AIJob, error) {
	if m.GetAIJobByBatchIDFunc != nil {
		return m.GetAIJobByBatchIDFunc(batchID)
	}
	return AIJob{}, nil
}

func (m *MockStore) GetAIJobPayload(id string) ([]byte, error) {
	if m.GetAIJobPayloadFunc != nil {
		return m.GetAIJobPayloadFunc(id)
	}
	return nil, nil
}

func (m *MockStore) MarkAIJobSubmitted(id, batchID string) error {
	if m.MarkAIJobSubmittedFunc != nil {
		return m.MarkAIJobSubmittedFunc(id, batchID)
	}
	return nil
}

func (m *MockStore) MarkAIJobCompleted(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error {
	if m.MarkAIJobCompletedFunc != nil {
		return m.MarkAIJobCompletedFunc(id, status, successCount, errorCount, rowErrors)
	}
	return nil
}

func (m *MockStore) MarkAIJobFailed(id, errMsg string) error {
	if m.MarkAIJobFailedFunc != nil {
		return m.MarkAIJobFailedFunc(id, errMsg)
	}
	return nil
}

func (m *MockStore) ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]AIJob, error) {
	if m.ListAIJobsFunc != nil {
		return m.ListAIJobsFunc(typeFilter, statusFilter, limit, offset)
	}
	return nil, nil
}

func (m *MockStore) UpdateBookFileHashes(id, originalHash, postMetadataHash string) error {
	if m.UpdateBookFileHashesFunc != nil {
		return m.UpdateBookFileHashesFunc(id, originalHash, postMetadataHash)
	}
	return nil
}

func (m *MockStore) SetBookFileHash(id, hash string) error {
	if m.SetBookFileHashFunc != nil {
		return m.SetBookFileHashFunc(id, hash)
	}
	return nil
}

func (m *MockStore) AddMetadataRejection(r MetadataRejection) error {
	if m.AddMetadataRejectionFunc != nil {
		return m.AddMetadataRejectionFunc(r)
	}
	return nil
}

func (m *MockStore) GetMetadataRejections(bookID string) ([]MetadataRejection, error) {
	if m.GetMetadataRejectionsFunc != nil {
		return m.GetMetadataRejectionsFunc(bookID)
	}
	return nil, nil
}

func (m *MockStore) DeleteMetadataRejections(bookID string) error {
	if m.DeleteMetadataRejectionsFunc != nil {
		return m.DeleteMetadataRejectionsFunc(bookID)
	}
	return nil
}

func (m *MockStore) GetAuthorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Author, error) {
	return make(map[string][]Author), nil
}

func (m *MockStore) GetNarratorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Narrator, error) {
	return make(map[string][]Narrator), nil
}

// OpsV2Store stub methods — permissive no-ops for tests that don't need v2 ops.
func (m *MockStore) UpsertOpDefinitionV2(_ OpDefinitionV2Row) error    { return nil }
func (m *MockStore) DeleteOrphanOpDefsV2(_ []string) error             { return nil }
func (m *MockStore) InsertOperationV2(_ OperationV2Row) error          { return nil }
func (m *MockStore) ListQueuedOperationsV2() ([]OperationV2Row, error) { return nil, nil }
func (m *MockStore) GetOperationV2(_ string) (*OperationV2Row, error)  { return nil, nil }
func (m *MockStore) UpdateOperationV2Status(_ string, _ string, _, _ *time.Time, _ *string) error {
	return nil
}
func (m *MockStore) SetOperationV2StatusIfQueued(_, _ string) (bool, error) { return false, nil }
func (m *MockStore) CountRunningByPluginV2(_ string) (int, error)           { return 0, nil }
func (m *MockStore) ListActiveOperationsV2() ([]OperationV2Row, error)      { return nil, nil }
func (m *MockStore) IncrementResumeCountV2(_ string) error                  { return nil }
func (m *MockStore) InsertOpStrikeV2(_ OpStrikeV2Row) error                 { return nil }
func (m *MockStore) GetOpStateV2(_ string) (*OpStateV2Row, error)           { return nil, nil }
func (m *MockStore) DeleteOpStateV2(_ string) error                         { return nil }
func (m *MockStore) UpdateOpProgressV2(_ string, _, _ int, _ string) error  { return nil }
func (m *MockStore) UpdateOpPhaseV2(_ string, _ *string) error              { return nil }
func (m *MockStore) UpdateOpCheckpointV2(_ string, _ int) error             { return nil }
func (m *MockStore) AppendOpLogsV2(_ []OpLogV2Row) error                    { return nil }
func (m *MockStore) InsertOpErrorV2(_ OpErrorV2Row) error                   { return nil }
func (m *MockStore) UpsertOpStateV2(_ OpStateV2Row) error                   { return nil }
func (m *MockStore) ListOperationsV2Since(_ time.Time, _ int) ([]OperationV2Row, error) {
	return nil, nil
}
func (m *MockStore) GetOpLogsV2(_ string, _ int) ([]OpLogV2Row, error) { return nil, nil }

// UOS dependency-scheduling stubs (Task 2).
func (m *MockStore) GetDepRev(_ OpSubject) (uint64, error)                       { return 0, nil }
func (m *MockStore) BumpDepRev(_ OpSubject) (uint64, error)                      { return 1, nil }
func (m *MockStore) RecordOpCompletion(_ OpSubject, _, _ string, _ uint64) error { return nil }
func (m *MockStore) GetOpCompletion(_ OpSubject, _ string) (uint64, bool, error) {
	return 0, false, nil
}
func (m *MockStore) ListFileCompletions(_ OpSubject, _ string) (map[string]uint64, error) {
	return nil, nil
}
func (m *MockStore) ListWaitingDepsOps() ([]OperationV2Row, error) { return nil, nil }
func (m *MockStore) PromoteToQueued(_ string) error                { return nil }

// M3 batch bucket stubs. MockStore is a permissive stub — these are no-ops
// by default. Tests that need batch-bucket behaviour should use fakeStore
// (in internal/operations/registry/teststore_test.go) which carries real state.
func (m *MockStore) AddToBatchBucket(_ string, _ OpSubject) error         { return nil }
func (m *MockStore) ListBatchBucket(_ string) ([]BatchBucketEntry, error) { return nil, nil }
func (m *MockStore) ClearBatchBucket(_ string, _ []OpSubject) error       { return nil }
