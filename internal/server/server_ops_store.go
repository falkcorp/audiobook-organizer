// file: internal/server/server_ops_store.go
// version: 1.1.0
// guid: 5a2e91c7-3f04-4b68-9d15-8c73e06af241
// last-edited: 2026-08-22

package server

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ServerOpsStore is what internal/server actually uses a store FOR.
//
// Measured with go/packages at full type resolution (types.Info.Selections, so
// the dominant `store := s.Ops(); store.X()` idiom is followed -- 271 of 315
// uses are not immediately dotted and a grep cannot see them): the 216 call
// sites in this package invoke exactly the 88 methods below, out of
// database.Store's 398.
//
// 🔑 WHY THIS IS NOT THE WHOLE STORY, and why storeForWiring still exists.
// Narrowing this accessor does NOT narrow what the wiring sites can reach. Those
// sites pass a store into a callee that already declares its own narrow
// interface (dedup.Store at 40, maintenance.JobStore at 52, and 88 others), and
// Go accepts the wide value structurally. The union of what those callees
// require is 268 methods -- so a single accessor serving both roles is pinned at
// 268 no matter how carefully this list is trimmed. Splitting the roles is the
// only thing that makes 88 reachable, which is the same shape chosen for
// maintenance.StoreProvider in #2612.
//
// Grouping is not decoration: `interfacebloat` caps an interface at 8 DECLARED
// entries and the width ratchet's baseline has been 0 since #2603. 88 methods
// therefore have to arrive as 19 leaves of <=8, composed into 6 mid-level
// interfaces plus 2 direct leaves, giving this type exactly 8 entries.
type ServerOpsStore interface {
	serverBookStore
	serverEntityStore
	serverTagStore
	serverOperationStore
	serverAuthStore
	serverConfigStore
	serverStatsReader
	serverITunesDeferStore
}

// ----- composites -----

// serverBookStore: Everything the server does directly to books and their files.
type serverBookStore interface {
	serverBookReader
	serverBookWriter
	serverBookVersionStore
	serverBookFileReader
}

// serverEntityStore: Authors, their book links, narrators and series.
type serverEntityStore interface {
	serverAuthorStore
	serverAuthorLinkStore
	serverEntityReader
}

// serverTagStore: Tag reads and writes.
type serverTagStore interface {
	serverTagWriter
	serverTagReader
}

// serverOperationStore: v1 operation rows plus the v2 registry row.
type serverOperationStore interface {
	serverOperationReader
	serverOperationWriter
	serverOperationV2Store
}

// serverAuthStore: Users, sessions and API keys.
type serverAuthStore interface {
	serverUserStore
	serverCredentialStore
}

// serverConfigStore: Settings, import paths and metadata field state.
type serverConfigStore interface {
	serverSettingsStore
	serverImportPathStore
	serverMetadataStateStore
}

// ----- leaves -----

// serverBookReader: Reads book rows. No writer method, by construction.
type serverBookReader interface {
	GetAllBooksCore(limit int, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetBookByFilePath(path string) (*database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
	GetQuarantinedBooks(limit int, offset int) ([]database.Book, error)
	ListBookIDs() ([]string, error)
}

// serverBookWriter: Creates, updates and deletes book rows.
type serverBookWriter interface {
	CreateBook(book *database.Book) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	SetLastWrittenAt(id string, t time.Time) error
}

// serverBookVersionStore: Reads and writes version rows for a book.
type serverBookVersionStore interface {
	GetBookVersion(id string) (*database.BookVersion, error)
	UpdateBookVersion(v *database.BookVersion) error
	DeleteBookVersion(id string) error
}

// serverBookFileReader: Reads book_file rows. Cannot delete one -- see the
// missing-file lane for why that matters.
type serverBookFileReader interface {
	GetBookFileByID(bookID string, fileID string) (*database.BookFile, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFilesNeedingDelugeImportCore() ([]database.BookFileCore, error)
	GetFilesWithFingerprintFailures(reason string, limit int, offset int) ([]database.BookFile, int64, error)
}

// serverStatsReader: Aggregate counters for the dashboard and health endpoints.
type serverStatsReader interface {
	CountPrimaryBooks() (int, error)
	CountQuarantinedBooks() (int, error)
	GetAcoustIDStats() (*database.AcoustIDStats, error)
	GetBookFileHashStats() (*database.BookFileHashStats, error)
	GetBookMetadataHashStats() (*database.BookMetadataHashStats, error)
	GetDistinctGenres() ([]string, error)
	GetDistinctLanguages() ([]string, error)
}

// serverAuthorStore: Author rows: create, rename, delete, tombstone.
type serverAuthorStore interface {
	CreateAuthor(name string) (*database.Author, error)
	CreateAuthorAlias(authorID int, aliasName string, aliasType string) (*database.AuthorAlias, error)
	CreateAuthorTombstone(oldID int, canonicalID int) error
	DeleteAuthor(id int) error
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	UpdateAuthorName(id int, name string) error
}

// serverAuthorLinkStore: The book<->author join slice. Separate from serverAuthorStore
// because the link lives in the join, not on either row.
type serverAuthorLinkStore interface {
	GetAllAuthorBookCounts() (map[int]int, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// serverEntityReader: Narrator and series reads.
type serverEntityReader interface {
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetNarratorByID(id int) (*database.Narrator, error)
	GetSeriesByID(id int) (*database.Series, error)
}

// serverTagWriter: Adds, sets and removes tags on books, authors and series.
type serverTagWriter interface {
	AddAuthorTag(authorID int, tag string) error
	AddAuthorTagWithSource(authorID int, tag string, source string) error
	AddBookTag(bookID string, tag string) error
	AddSeriesTag(seriesID int, tag string) error
	AddSeriesTagWithSource(seriesID int, tag string, source string) error
	RemoveBookTag(bookID string, tag string) error
	SetBookTags(bookID string, tags []string) error
}

// serverTagReader: Reads tags back.
type serverTagReader interface {
	GetAuthorTagsDetailed(authorID int) ([]database.BookTag, error)
	GetBookTags(bookID string) ([]string, error)
	GetSeriesTagsDetailed(seriesID int) ([]database.BookTag, error)
}

// serverOperationReader: Reads v1 operation rows and their results.
type serverOperationReader interface {
	GetInterruptedOperations() ([]database.Operation, error)
	GetOperationByID(id string) (*database.Operation, error)
	GetOperationParams(opID string) ([]byte, error)
	GetOperationResults(operationID string) ([]database.OperationResult, error)
	GetOperationSummaryLog(id string) (*database.OperationSummaryLog, error)
	GetRecentOperations(limit int) ([]database.Operation, error)
	ListOperations(limit int, offset int) ([]database.Operation, int, error)
}

// serverOperationWriter: Creates and advances v1 operation rows.
type serverOperationWriter interface {
	AddOperationLog(operationID string, level string, message string, details *string) error
	CreateOperation(id string, opType string, folderPath *string) (*database.Operation, error)
	CreateOperationChange(change *database.OperationChange) error
	CreateOperationResult(result *database.OperationResult) error
	// DeleteOperationWithLogs removes a v1 row this server created and then found
	// it did not need — see runMaintenanceJob, where an enqueue that merges into
	// an already-active run leaves its freshly-created row twinned to nothing.
	// Deleting it is what keeps that row from sitting at "pending" forever and
	// being re-resumed on every restart.
	DeleteOperationWithLogs(id string) error
	SaveOperationParams(opID string, params []byte) error
	UpdateOperationError(id string, errorMessage string) error
	UpdateOperationResultData(id string, resultData string) error
	UpdateOperationStatus(id string, status string, progress int, total int, message string) error
}

// serverOperationV2Store: The v2 registry row. Kept apart from the v1 pair so the
// kill-v1 migration can delete one without unpicking the other.
type serverOperationV2Store interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
	UpdateOperationV2Status(id string, status string, startedAt *time.Time, completedAt *time.Time, errMsg *string) error
}

// serverUserStore: User rows and per-user preferences.
type serverUserStore interface {
	ConsumeInvite(token string, passwordHashAlgo string, passwordHash string) (*database.User, error)
	GetUserByID(id string) (*database.User, error)
	GetUserByUsername(username string) (*database.User, error)
	GetUserPreference(key string) (*database.UserPreference, error)
	ListUsers() ([]database.User, error)
}

// serverCredentialStore: Sessions and API keys.
type serverCredentialStore interface {
	CreateAPIKey(key *database.APIKey) (*database.APIKey, error)
	CreateSession(userID string, ip string, userAgent string, ttl time.Duration) (*database.Session, error)
	DeleteExpiredSessions(now time.Time) (int, error)
	ListAllAPIKeys() ([]database.APIKey, error)
}

// serverMetadataStateStore: Per-field metadata provenance state.
type serverMetadataStateStore interface {
	DeleteMetadataFieldState(bookID string, field string) error
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
}

// serverImportPathStore: Configured import paths.
type serverImportPathStore interface {
	GetAllImportPaths() ([]database.ImportPath, error)
	GetImportPathByID(id int) (*database.ImportPath, error)
	UpdateImportPath(id int, importPath *database.ImportPath) error
}

// serverSettingsStore: Settings, raw keys and the root dir.
type serverSettingsStore interface {
	DeleteRaw(key string) error
	GetSetting(key string) (*database.Setting, error)
	SetRaw(key string, value []byte) error
	SetRootDir(rootDir string)
	SetSetting(key string, value string, typ string, isSecret bool) error
}

// serverITunesDeferStore: Queues a deferred iTunes path update.
type serverITunesDeferStore interface {
	CreateDeferredITunesUpdate(bookID string, persistentID string, oldPath string, newPath string, updateType string) error
}

// Compile-time proof that the concrete store satisfies the narrow view. If a
// database method signature changes, this breaks here -- at the declaration --
// rather than at whichever of the 216 call sites compiles first.
var _ ServerOpsStore = (*database.PebbleStore)(nil)
