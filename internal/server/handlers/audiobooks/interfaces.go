// file: internal/server/handlers/audiobooks/interfaces.go
// version: 1.3.0
// guid: 110386de-3e07-4ef3-b0e0-2e717a249e91
// last-edited: 2026-08-18

// Narrow dependency interfaces for the audiobooks-domain HTTP handlers (the
// main library list / CRUD domain: list, count, facets, soft-delete /
// restore / purge, cover art, get, segments, book files, track-info extract,
// relocate, segment tags, metadata + path history, field states, undo,
// external IDs, user tags, alternative titles, batch update / operations,
// changelog, changes; 36 handlers total).
//
// Each interface lists only what the handlers actually call so package
// audiobookshandler stays decoupled from the concrete store / service
// implementations and never imports package server (which would create an
// import cycle).
//
// The package is named audiobookshandler (not "audiobooks") to avoid clashing
// with the existing internal/audiobooks package, which is imported here as
// audiobookspkg for its shared ListFilters / FieldFilter / IsPerUserField /
// AudiobookDetail types.
//
// Helpers that the handlers share with files that STAY in package server
// (library_list_warmer.go, server_maintenance_deps.go, server_lifecycle.go)
// are NOT abstracted here — they are relocated to
// internal/server/audiobooks_helpers.go (package server) and the controller
// injects thin func closures that wrap them. See handler.go's injected func
// fields (buildListResponse, isProtectedPath, enrichBook, getFieldStates,
// getExternalIDStore, publishEvent).

package audiobookshandler

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// AudiobookBookStore reads and updates the book row.
type AudiobookBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// AudiobookFileStore covers the book's files.
type AudiobookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
	UpsertBookFile(file *database.BookFile) error
}

// AudiobookHistoryStore covers change and path history.
type AudiobookHistoryStore interface {
	RecordMetadataChange(record *database.MetadataChangeRecord) error
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
	GetMetadataChangeHistory(bookID, field string, limit int) ([]database.MetadataChangeRecord, error)
	GetBookPathHistory(bookID string) ([]database.BookPathChange, error)
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// AudiobookTitleStore covers alternative titles.
type AudiobookTitleStore interface {
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
	AddBookAlternativeTitle(bookID, title, source, language string) error
	RemoveBookAlternativeTitle(bookID, title string) error
}

// AudiobookTagStore reads detailed tags.
type AudiobookTagStore interface {
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
}

// AudiobookFacetStore reads library-wide facets.
type AudiobookFacetStore interface {
	GetDistinctGenres() ([]string, error)
	GetDistinctLanguages() ([]string, error)
}

// AudiobookCreditStore reads authors and narrators.
type AudiobookCreditStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	GetNarratorByID(id int) (*database.Narrator, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
}

// AudiobookSyncStore records write-out timestamps.
type AudiobookSyncStore interface {
	SetLastWrittenAt(bookID string, t time.Time) error
}

// AudiobooksStore is the narrow database.Store subset the audiobooks handlers
// require for direct store access (i.e. the calls the handlers made through
// s.Store() that are part of the stable database.Store contract). The concrete
// database.Store implementations satisfy it. Resolved lazily through a provider
// closure (getStore) so a router-integration test that swaps server.store
// post-wire is still observed (mirrors the dedup / duplicates / system handler
// getStore seam).
//
// NOTE: the handlers ALSO probe the live store for optional/decorator-only
// methods via inline type assertions (ListBooksWithFileErrors,
// GetAllBookIDsForQuickQuery, GetBookFilesForIDsCore, Unwrap, InvalidateLibraryStats).
// Those are intentionally NOT listed here — they resolve against the dynamic
// type of the value the provider returns (s.Store(), the concrete store), so
// they keep working as long as getStore returns the un-stripped store.
//
// Split into the 8 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type AudiobooksStore interface {
	AudiobookBookStore
	AudiobookFileStore
	AudiobookHistoryStore
	AudiobookTitleStore
	AudiobookTagStore
	AudiobookFacetStore
	AudiobookCreditStore
	AudiobookSyncStore
}

// AudiobookReader AudiobookReader is the plain read side: fetch one book, its tag comparison view, and the library count.
type AudiobookReader interface {
	GetAudiobook(ctx context.Context, id string) (*database.Book, error)
	GetAudiobookTags(ctx context.Context, id, compareID, snapshotTS string) (map[string]any, error)
	CountAudiobooks(ctx context.Context) (int, error)
}

// AudiobookTrashService AudiobookTrashService is the soft-delete lifecycle: list and count what is in the trash, restore one, delete one, and purge.
type AudiobookTrashService interface {
	GetSoftDeletedBooks(ctx context.Context, limit, offset int, olderThanDays *int) ([]database.Book, error)
	// CountSoftDeletedBooks is the exact trash size. It replaced a
	// fetch-10,000-and-len() whose count saturated and whose error was
	// swallowed into total:0 — callers must surface the error, never zero it.
	CountSoftDeletedBooks(ctx context.Context, olderThanDays *int) (int, error)
	PurgeSoftDeletedBooks(ctx context.Context, deleteFiles bool, olderThanDays *int) (*audiobookspkg.PurgeResult, error)
	RestoreAudiobook(ctx context.Context, id string) (*database.Book, error)
	DeleteAudiobook(ctx context.Context, id string, opts *audiobookspkg.DeleteAudiobookOptions) (map[string]any, error)
}

// AudiobookUserTagService AudiobookUserTagService is the user-tag surface: enumerate, read per book, and batch add/remove.
type AudiobookUserTagService interface {
	ListAllUserTags() ([]database.TagWithCount, error)
	GetBookUserTags(bookID string) ([]string, error)
	BatchUpdateUserTags(bookIDs, addTags, removeTags []string) (int, error)
}

// AudiobookViewDecorator AudiobookViewDecorator turns raw book rows into the detail shape the list endpoints return, and drops the caches that shape is built from.
type AudiobookViewDecorator interface {
	EnrichAudiobooksWithNames(books []database.Book) []audiobookspkg.AudiobookDetail
	InvalidateBookCaches()
}

// AudiobookService is the narrow *audiobookspkg.AudiobookService subset the
// moved handlers use. The four list-pipeline-only methods (GetAudiobooks,
// FetchBookFilesForBooks, EnrichAudiobooksWithNamesAndFiles,
// CountAudiobooksFiltered) are NOT here: they are only called by
// buildAudiobookListResponse, which stays in package server (the relocated
// audiobooks_helpers.go calls the concrete service) and is reached from the
// listAudiobooks handler via the injected buildListResponse closure.
//
// Split into the 4 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type AudiobookService interface {
	AudiobookReader
	AudiobookTrashService
	AudiobookUserTagService
	AudiobookViewDecorator
}

// AudiobookUpdater is the narrow *audiobookspkg.AudiobookUpdateService subset
// used by updateAudiobook. UpdateAudiobook performs the full-column replacement
// and returns the updated book.
type AudiobookUpdater interface {
	UpdateAudiobook(ctx context.Context, id string, payload map[string]any) (*database.Book, error)
}

// WriteBackEnqueuer is the narrow *itunesservice.WriteBackBatcher subset used by
// updateAudiobook / undoLastApply / batchUpdateAudiobooks / batchOperations to
// queue books for iTunes auto write-back. Only Enqueue is used.
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
}

// MetadataStateService is the narrow *metafetch.MetadataStateService subset used
// by undoMetadataChange / undoLastApply. LoadMetadataState is NOT here — it
// returns an unexported map type, so getAudiobookFieldStates reaches it through
// the injected getFieldStates closure instead.
type MetadataStateService interface {
	SetOverride(bookID, field string, value any, locked bool) error
	ClearOverride(bookID, field string) error
}

// MetadataFetchService is the narrow *metafetch.Service subset used by
// undoMetadataChange / undoLastApply for cache invalidation after a revert.
type MetadataFetchService interface {
	InvalidateCachedCandidates(bookID string) error
}

// BatchService is the narrow *batch.BatchService subset used by
// batchUpdateAudiobooks / batchOperations.
type BatchService interface {
	UpdateAudiobooks(req *batch.BatchUpdateRequest) *batch.BatchResponse
	ExecuteOperations(req *batch.BatchOperationsRequest) *batch.BatchResponse
}

// ChangelogService is the narrow *activity.ChangelogService subset used by
// getBookChangelog.
type ChangelogService interface {
	GetBookChangelog(bookID string) ([]activity.ChangeLogEntry, error)
}

// ExternalIDStore is the narrow external-ID lookup used by getAudiobookExternalIDs.
// The server-side asExternalIDStore(s.Store()) adapter produces a value that
// satisfies this (it returns nil when the store doesn't implement external IDs).
// Reached through the injected getExternalIDStore closure so this package does
// not depend on the server-package adapter.
type ExternalIDStore interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}
