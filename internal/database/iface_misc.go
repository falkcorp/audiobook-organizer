// file: internal/database/iface_misc.go
// version: 1.16.0
// guid: 473781a7-1a31-4914-b7c7-8efc91f9f7e6
// last-edited: 2026-07-03

package database

import "context"
import "errors"
import "time"

// BookFileHashUpdater is the narrow interface required by fileops.WriteTagsSafe
// to record pre- and post-write file hashes without needing the full Store.
// SQLiteStore satisfies this automatically via its UpdateBookFileHashes method.
type BookFileHashUpdater interface {
	// UpdateBookFileHashes records the SHA-256 fingerprints taken before and
	// after a tag write. original_file_hash is written only if the row has
	// no existing value (first-write semantics); post_metadata_hash is always
	// overwritten with the latest post-write fingerprint.
	UpdateBookFileHashes(fileID, originalHash, postHash string) error
}

// LifecycleStore covers store startup/teardown.
type LifecycleStore interface {
	Close() error
	Reset() error
}

// NarratorStore covers narrators + book-narrator joins.
type NarratorStore interface {
	CreateNarrator(name string) (*Narrator, error)
	GetNarratorByID(id int) (*Narrator, error)
	GetNarratorByName(name string) (*Narrator, error)
	ListNarrators() ([]Narrator, error)
	GetBookNarrators(bookID string) ([]BookNarrator, error)
	SetBookNarrators(bookID string, narrators []BookNarrator) error
	// GetNarratorsByBookIDs returns a map from bookID → []Narrator for all given book IDs.
	// Returns an empty map (not nil) if bookIDs is empty.
	GetNarratorsByBookIDs(ctx context.Context, bookIDs []string) (map[string][]Narrator, error)
}

// WorkStore covers Work CRUD.
type WorkStore interface {
	GetAllWorks() ([]Work, error)
	GetWorkByID(id string) (*Work, error)
	CreateWork(work *Work) (*Work, error)
	UpdateWork(id string, work *Work) (*Work, error)
	DeleteWork(id string) error
	GetBooksByWorkID(workID string) ([]Book, error)
	GetAllWorkBookCounts() (map[string]int, error)
}

// SessionStore covers authenticated session CRUD.
type SessionStore interface {
	CreateSession(userID, ip, userAgent string, ttl time.Duration) (*Session, error)
	GetSession(id string) (*Session, error)
	RevokeSession(id string) error
	ListUserSessions(userID string) ([]Session, error)
	DeleteExpiredSessions(now time.Time) (int, error)
}

// RoleStore covers Role CRUD.
type RoleStore interface {
	GetRoleByID(id string) (*Role, error)
	GetRoleByName(name string) (*Role, error)
	ListRoles() ([]Role, error)
	CreateRole(role *Role) (*Role, error)
	UpdateRole(role *Role) error
	DeleteRole(id string) error
}

// APIKeyStore covers APIKey CRUD and revocation.
type APIKeyStore interface {
	CreateAPIKey(key *APIKey) (*APIKey, error)
	GetAPIKey(id string) (*APIKey, error)
	GetAPIKeyByHash(hash string) (*APIKey, error)
	ListAPIKeysForUser(userID string) ([]APIKey, error)
	ListAllAPIKeys() ([]APIKey, error)
	RevokeAPIKey(id string) error
	SetAPIKeyStatus(id, status string, at time.Time) error
	// SetAPIKeyExpiry updates only the ExpiresAt field on an existing key,
	// leaving Status untouched. Used for the rotation grace window, where the
	// old key must keep working until `at` rather than being revoked
	// immediately (SEC-1/PROC-6).
	SetAPIKeyExpiry(id string, at time.Time) error
	TouchAPIKeyLastUsed(id string, at time.Time, ip string) error
}

// InviteStore covers Invite CRUD and atomic consume.
type InviteStore interface {
	CreateInvite(invite *Invite) (*Invite, error)
	GetInvite(token string) (*Invite, error)
	ListActiveInvites() ([]Invite, error)
	DeleteInvite(token string) error
	ConsumeInvite(token, passwordHashAlgo, passwordHash string) (*User, error)
}

// UserPreferenceStore covers both global and per-user preferences.
type UserPreferenceStore interface {
	GetUserPreference(key string) (*UserPreference, error)
	SetUserPreference(key, value string) error
	GetAllUserPreferences() ([]UserPreference, error)
	SetUserPreferenceForUser(userID, key, value string) error
	GetUserPreferenceForUser(userID, key string) (*UserPreferenceKV, error)
	GetAllPreferencesForUser(userID string) ([]UserPreferenceKV, error)
}

// UserPositionStore covers per-user position + derived book state.
type UserPositionStore interface {
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	GetUserPosition(userID, bookID string) (*UserPosition, error)
	ListUserPositionsForBook(userID, bookID string) ([]UserPosition, error)
	ClearUserPositions(userID, bookID string) error
	SetUserBookState(state *UserBookState) error
	GetUserBookState(userID, bookID string) (*UserBookState, error)
	ListUserBookStatesByStatus(userID, status string, limit, offset int) ([]UserBookState, error)
	ListUserPositionsSince(userID string, t time.Time) ([]UserPosition, error)
}

// BookVersionStore covers version CRUD, lifecycle, and lookups.
type BookVersionStore interface {
	CreateBookVersion(v *BookVersion) (*BookVersion, error)
	GetBookVersion(id string) (*BookVersion, error)
	GetBookVersionsByBookID(bookID string) ([]BookVersion, error)
	GetActiveVersionForBook(bookID string) (*BookVersion, error)
	UpdateBookVersion(v *BookVersion) error
	DeleteBookVersion(id string) error
	GetBookVersionByTorrentHash(hash string) (*BookVersion, error)
	ListTrashedBookVersions() ([]BookVersion, error)
	ListPurgedBookVersions() ([]BookVersion, error)
}

// BookFileStore covers the canonical BookFile surface.
type BookFileStore interface {
	CreateBookFile(file *BookFile) error
	UpdateBookFile(id string, file *BookFile) error
	GetBookFiles(bookID string) ([]BookFile, error)
	GetAllBookFiles() ([]BookFile, error)
	// GetBookFilesNeedingDelugeImport returns book_files that have a deluge_hash
	// but have not yet been copied into the library (imported_from_deluge_at IS NULL).
	GetBookFilesNeedingDelugeImport() ([]BookFile, error)
	GetBookFileByID(bookID, fileID string) (*BookFile, error)
	GetBookFileByPID(itunesPID string) (*BookFile, error)
	// ClearITunesPID surgically clears itunes_persistent_id and itunes_path
	// on the book_file row matching the given PID. Used by the iTunes
	// orphan-cleanup path so that DB state converges after a successful
	// ITL remove. Returns (false, nil) if no matching row exists.
	ClearITunesPID(itunesPID string) (cleared bool, err error)
	GetBookFileByPath(filePath string) (*BookFile, error)
	GetBookFileByAcoustID(fingerprint string) (*BookFile, error)
	GetBookFileByAcoustIDFuzzy(fingerprint string, minSimilarity float64) (*BookFile, error)
	DeleteBookFile(id string) error
	DeleteBookFilesForBook(bookID string) error
	UpsertBookFile(file *BookFile) error
	BatchUpsertBookFiles(files []*BookFile) error
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
	// UpdateBookFileHashes is a surgical update that records pre-write and post-write
	// SHA-256 hashes without touching any other BookFile fields.
	UpdateBookFileHashes(id, originalHash, postMetadataHash string) error
	// MarkFileImportedFromDeluge records that a file has been imported from a
	// Deluge download directory. originalPath is the source (download) path,
	// libraryPath is the destination inside the organized library, and
	// torrentHash is the Deluge info-hash (optional). Implementations SHOULD
	// match by originalPath first, then fall back to matching by torrentHash.
	MarkFileImportedFromDeluge(ctx context.Context, originalPath, libraryPath, torrentHash string) error
	// GetDuplicateFilesByHash returns groups of book_files that share the same
	// original_file_hash (non-empty). Each group has ≥2 entries and represents
	// the same physical audio file in multiple locations.
	GetDuplicateFilesByHash(limit int) ([]DuplicateFileGroup, error)
	// GetBookBySegmentFileHash looks up a BookFile by file_hash or
	// original_file_hash and returns the parent Book. Used by the scanner's
	// multi-file dedup tally to match individual segment files across folders
	// without assuming that whole-directory == same book.
	GetBookBySegmentFileHash(hash string) (*Book, error)
	// SetBookFileHash sets file_hash on a book_file row, mirroring what the
	// scanner does on initial import. Also sets original_file_hash if it is
	// currently empty. Used by the backfill handler to populate hashes for
	// files that were imported before hash tracking was added.
	SetBookFileHash(id, hash string) error
	// GetBookFileHashStats returns aggregate hash-coverage statistics for all
	// book_files in the library, including a per-library-path breakdown.
	GetBookFileHashStats() (*BookFileHashStats, error)
	// GetBookMetadataHashStats returns aggregate metadata_source_hash coverage
	// across all books, including a per-library-path breakdown.
	GetBookMetadataHashStats() (*BookMetadataHashStats, error)
	// GetFilesWithFingerprintFailures returns book_files where FingerprintFailedAt is set,
	// optionally filtered by reason. Returns the filtered page plus total matching count.
	GetFilesWithFingerprintFailures(reason string, limit, offset int) ([]BookFile, int64, error)
	// GetAcoustIDStats returns AcoustID fingerprint coverage across all book files,
	// including a per-library-root breakdown.
	GetAcoustIDStats() (*AcoustIDStats, error)
}

// BookSegmentStore covers the deprecated segment surface, kept until
// the segment-removal PR.
type BookSegmentStore interface {
	CreateBookSegment(bookNumericID int, segment *BookSegment) (*BookSegment, error)
	UpdateBookSegment(segment *BookSegment) error
	ListBookSegments(bookNumericID int) ([]BookSegment, error)
	MergeBookSegments(bookNumericID int, newSegment *BookSegment, supersedeIDs []string) error
	GetBookSegmentByID(segmentID string) (*BookSegment, error)
	MoveSegmentsToBook(segmentIDs []string, targetBookNumericID int) error
}

// PlaylistStore covers the legacy series-playlist auto-generator.
type PlaylistStore interface {
	CreatePlaylist(name string, seriesID *int, filePath string) (*Playlist, error)
	GetPlaylistByID(id int) (*Playlist, error)
	GetPlaylistBySeriesID(seriesID int) (*Playlist, error)
	AddPlaylistItem(playlistID, bookID, position int) error
	GetPlaylistItems(playlistID int) ([]PlaylistItem, error)
}

// UserPlaylistStore covers smart + static user playlists (spec 3.4).
type UserPlaylistStore interface {
	CreateUserPlaylist(pl *UserPlaylist) (*UserPlaylist, error)
	GetUserPlaylist(id string) (*UserPlaylist, error)
	GetUserPlaylistByName(name string) (*UserPlaylist, error)
	GetUserPlaylistByITunesPID(pid string) (*UserPlaylist, error)
	ListUserPlaylists(playlistType string, limit, offset int) ([]UserPlaylist, int, error)
	// ListUserPlaylistsForUser returns only playlists created by userID. Used by
	// the API to scope list results per user (prevents cross-user disclosure).
	// An empty userID matches playlists with an empty CreatedByUserID.
	ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]UserPlaylist, int, error)
	UpdateUserPlaylist(pl *UserPlaylist) error
	DeleteUserPlaylist(id string) error
	ListDirtyUserPlaylists() ([]UserPlaylist, error)
}

// ImportPathStore covers managed import path CRUD.
type ImportPathStore interface {
	GetAllImportPaths() ([]ImportPath, error)
	GetImportPathByID(id int) (*ImportPath, error)
	GetImportPathByPath(path string) (*ImportPath, error)
	CreateImportPath(path, name string) (*ImportPath, error)
	UpdateImportPath(id int, importPath *ImportPath) error
	DeleteImportPath(id int) error
	// CountBooksByPathPrefix returns the total number of books whose FilePath
	// starts with the given prefix. Used to persist accurate BookCount on
	// ImportPath after a scan without doing a live count on every read.
	CountBooksByPathPrefix(prefix string) (int, error)
}

// MetadataStore covers MetadataFieldState, change history, and
// alternative titles.
type MetadataStore interface {
	GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error)
	UpsertMetadataFieldState(state *MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
	RecordMetadataChange(record *MetadataChangeRecord) error
	GetMetadataChangeHistory(bookID string, field string, limit int) ([]MetadataChangeRecord, error)
	GetBookChangeHistory(bookID string, limit int) ([]MetadataChangeRecord, error)
	GetBookAlternativeTitles(bookID string) ([]BookAlternativeTitle, error)
	AddBookAlternativeTitle(bookID, title, source, language string) error
	RemoveBookAlternativeTitle(bookID, title string) error
	SetBookAlternativeTitles(bookID string, titles []BookAlternativeTitle) error
}

// HashBlocklistStore covers DoNotImport entries.
type HashBlocklistStore interface {
	IsHashBlocked(hash string) (bool, error)
	AddBlockedHash(hash, reason string) error
	RemoveBlockedHash(hash string) error
	GetAllBlockedHashes() ([]DoNotImport, error)
	GetBlockedHashByHash(hash string) (*DoNotImport, error)
}

// RawKVStore covers the low-level key-value escape hatch.
type RawKVStore interface {
	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
	ScanPrefix(prefix string) ([]KVPair, error)
	CountPrefix(prefix string) (int64, error)
}

// PlaybackStore covers playback events, progress, and stats.
type PlaybackStore interface {
	AddPlaybackEvent(event *PlaybackEvent) error
	ListPlaybackEvents(userID string, bookNumericID int, limit int) ([]PlaybackEvent, error)
	UpdatePlaybackProgress(progress *PlaybackProgress) error
	GetPlaybackProgress(userID string, bookNumericID int) (*PlaybackProgress, error)
	IncrementBookPlayStats(bookNumericID int, seconds int) error
	GetBookStats(bookNumericID int) (*BookStats, error)
	IncrementUserListenStats(userID string, seconds int) error
	GetUserStats(userID string) (*UserStats, error)
}

// SettingsStore covers persistent encrypted configuration.
type SettingsStore interface {
	GetSetting(key string) (*Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
	GetAllSettings() ([]Setting, error)
	DeleteSetting(key string) error
}

// StatsStore covers aggregate counts and dashboard metrics.
type StatsStore interface {
	CountFiles() (int, error)
	CountAuthors() (int, error)
	CountSeries() (int, error)
	GetBookCountsByLocation(rootDir string) (library, import_ int, err error)
	GetBookSizesByLocation(rootDir string) (librarySize, importSize int64, err error)
	GetDashboardStats() (*DashboardStats, error)
	// SetRootDir tells the store which directory is the organized library root,
	// used to split OrganizedBooks vs UnorganizedBooks in LibraryStats.
	SetRootDir(rootDir string)
	// InvalidateLibraryStats drops the cached LibraryStats so the next call
	// to GetDashboardStats triggers a fresh recompute.
	InvalidateLibraryStats()
}

// MaintenanceStore covers database maintenance and scan-cache.
type MaintenanceStore interface {
	Optimize() error
	GetScanCacheMap() (map[string]ScanCacheEntry, error)
	UpdateScanCache(bookID string, mtime int64, size int64) error
	MarkNeedsRescan(bookID string) error
	GetDirtyBookFolders() ([]string, error)
}

// SystemActivityStore covers cross-cutting system activity log.
type SystemActivityStore interface {
	AddSystemActivityLog(source, level, message string) error
	GetSystemActivityLogs(source string, limit int) ([]SystemActivityLog, error)
	PruneSystemActivityLogs(olderThan time.Time) (int, error)
}

// AIJobsStore is the subset of Store used by internal/ai/aijobs.
type AIJobsStore interface {
	CreateAIJob(job AIJob, payloadJSON []byte) error
	GetAIJob(id string) (AIJob, error)
	GetAIJobByBatchID(batchID string) (AIJob, error)
	GetAIJobPayload(id string) ([]byte, error)
	MarkAIJobSubmitted(id, batchID string) error
	MarkAIJobCompleted(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error
	MarkAIJobFailed(id, errMsg string) error
	ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]AIJob, error)
}

// RejectedMetadataStore records candidates that were rejected (user action,
// below-threshold, duration mismatch, wrong language) so auditing is possible.
type RejectedMetadataStore interface {
	AddMetadataRejection(r MetadataRejection) error
	GetMetadataRejections(bookID string) ([]MetadataRejection, error)
	DeleteMetadataRejections(bookID string) error
}

// ErrSQLiteRBACUnsupported is a sentinel kept for source compatibility after
// the SQLite backend was removed in fable5 TASK-022. PebbleDB always supports
// RBAC; this error is never returned at runtime. Callers that check
// errors.Is(err, ErrSQLiteRBACUnsupported) will always evaluate to false.
//
// Deprecated: The SQLite backend no longer exists; this sentinel will be
// removed in a future cleanup once all callers have been updated.
var ErrSQLiteRBACUnsupported = errors.New("SQLite backend no longer exists; RBAC is always available on PebbleDB")
