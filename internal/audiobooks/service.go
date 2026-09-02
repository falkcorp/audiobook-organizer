// file: internal/audiobooks/service.go
// version: 1.38.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-09-02

// Package audiobooks provides the core business logic for managing audiobooks,
// including CRUD operations, metadata management, search, deduplication, and
// user tag management. The AudiobookService is the central type; all public
// methods are defined across the following focused sub-files:
//
//   - service_types.go     — request/response types, filter structs, type helpers
//   - service_filtering.go — sort/filter helpers, pushdown builders, file aggregation
//   - service_query.go     — GetAudiobooks, CountAudiobooksFiltered, Enrich*, searchWithBleve
//   - service_single.go    — GetAudiobook, GetAudiobookTags, GetDuplicateBooks, lifecycle ops
//   - service_mutation.go  — UpdateAudiobook, DeleteAudiobook, iTunes helpers, ApplyOverride
//   - service_tags.go      — ListAllUserTags, GetBookUserTags, SetBookUserTags, batch tag ops
package audiobooks

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// The seven interfaces below are the measured dependency surface of
// AudiobookService, grouped by the entity each one reads or writes. Every
// method was enumerated with an empty-interface compiler probe run under
// -gcflags=-e: emptying audiobookStore makes the compiler list each reached
// method by name, plus each function the store is forwarded to that it no
// longer satisfies. 44 direct calls and 3 forwarding constraints; the union
// below is exactly that set and nothing else.
//
// They are declared as groups rather than one flat list because interfacebloat
// counts DECLARED ENTRIES, not transitive methods — a flat list of 50 would be
// a 50-entry declaration and score far worse than the 10 database.* embeds it
// replaced. Each group is independently under the limit as well, so the width
// is genuinely gone rather than pushed down a level.

// bookReader is the read side of the book entity: lookups, listings, and the
// legacy Store.SearchBooks fallback used when the Bleve index is not wired.
type bookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByIDs(ids []string) ([]database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error)
	GetBookAtVersion(id string, ts time.Time) (*database.Book, error)
	CountPrimaryBooks() (int, error)
	SearchBooks(query string, limit, offset int) ([]database.Book, error)
}

// bookWriter is the write side of the book entity, including the soft-delete
// lifecycle (tombstone create/delete plus the soft-deleted listing).
type bookWriter interface {
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	CreateBookTombstone(book *database.Book) error
	DeleteBookTombstone(id string) error
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
}

// contributorResolver is the get-or-create pass UpdateAudiobook runs when a
// payload names an author, narrator, or series by string rather than by ID.
// Each entity follows the same lookup-then-create shape, and the two Set*
// calls rewrite the book's join rows once the IDs are resolved.
type contributorResolver interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	GetNarratorByName(name string) (*database.Narrator, error)
	CreateNarrator(name string) (*database.Narrator, error)
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
}

// contributorHydrator is the batch side used when enriching a page of results:
// the ByIDs calls resolve display names for a whole page in one hit rather than
// per row, and the ByXIDCore calls back the author/series drill-down filters.
type contributorHydrator interface {
	GetAuthorsByIDs(ids []int) (map[int]*database.Author, error)
	GetSeriesByIDs(ids []int) (map[int]*database.Series, error)
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
}

// bookTagStore backs service_tags.go plus the tag filter in service_query.go
// and service_filtering.go.
type bookTagStore interface {
	ListAllTags() ([]database.TagWithCount, error)
	GetBookTags(bookID string) ([]string, error)
	SetBookTags(bookID string, tags []string) error
	AddBookTag(bookID, tag string) error
	RemoveBookTag(bookID, tag string) error
	GetBooksByTag(tag string) ([]string, error)
}

// bookFileStore covers everything the service asks about bytes on disk: the
// file rows for a book, the two duplicate views, the delete path's hash
// blocklist, and the import roots isProtectedPath compares against.
//
// GetAllImportPaths is also what satisfies importPathLister when the store is
// forwarded to isProtectedPath.
type bookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetDuplicateBooks() ([][]database.Book, error)
	GetFolderDuplicatesCore() ([][]database.BookCore, error)
	AddBlockedHash(hash, reason string) error
	GetAllImportPaths() ([]database.ImportPath, error)
}

// perUserStateStore is the per-user and per-field state the listing endpoint
// and UpdateAudiobook read: the read_status / progress_pct / last_played filter
// pass, the manual-override field states, and the change-history record.
//
// RecordMetadataChange is also what satisfies metadataStateStore when the store
// is forwarded to newMetadataStateSvc.
type perUserStateStore interface {
	GetUserPreference(key string) (*database.UserPreference, error)
	// DeleteUserPreference retires a book's pre-migration state blob once its
	// per-field rows have been written (database.DeleteLegacyMetadataState).
	DeleteUserPreference(key string) error
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
	RecordMetadataChange(record *database.MetadataChangeRecord) error
}

// audiobookStore is the dependency surface of AudiobookService, declared as the
// union of the groups above.
//
// It previously embedded ten database.* interfaces wholesale — 172 transitive
// methods to reach the 50 measured here. authorSeriesStore is embedded by name
// rather than inlined because that is the literal requirement: the service
// forwards svc.store to resolveAuthorAndSeriesNames, which takes that type.
type audiobookStore interface {
	authorSeriesStore

	bookReader
	bookWriter
	contributorResolver
	contributorHydrator
	bookTagStore
	bookFileStore
	perUserStateStore
}

// ITunesEnqueuer is the narrow surface AudiobookService uses to push
// iTunes mutations from delete/purge paths. Satisfied by
// *itunesservice.WriteBackBatcher. Optional — nil-safe.
type ITunesEnqueuer interface {
	EnqueueRemove(pid string)
}

// AudiobookService handles all audiobook business logic
type AudiobookService struct {
	store           audiobookStore
	bookCache       *cache.Cache[*database.Book]
	listCache       *cache.Cache[[]database.Book]
	activityService *activity.Service
	// searchIndex is the Bleve index for full-text search. When nil
	// the service falls back to the legacy Store.SearchBooks path.
	// Wired in by the Server after Bleve opens in Start(), which is
	// after NewAudiobookService runs in NewServer.
	searchIndex *search.BleveIndex
	// itunesEnqueuer is wired by the Server after the WriteBackBatcher
	// is constructed. Nil-safe — when nil the delete/purge paths skip
	// the iTunes side-effect (e.g. tests, iTunes disabled in config).
	itunesEnqueuer ITunesEnqueuer
	// libGen scopes listCache keys to the store's book-mutation counter, so a
	// created/updated/deleted book puts every previously cached page out of
	// reach. Resolved from the store once at construction; never nil.
	//
	// This cache sits UNDER the server's HTTP list cache: the handler's cached
	// gin.H response is built from GetAudiobooks, which reads this. Keying only
	// the HTTP cache would have left deleted books to be served straight back
	// out of here, because InvalidateBookCaches skips listCache unless
	// config.CacheInvalidateOnBookUpdate is on — and it is off by default.
	libGen *cache.Generation
}

// SetActivityService wires the activity service for snapshot fallback in GetAudiobookTags.
func (svc *AudiobookService) SetActivityService(as *activity.Service) {
	svc.activityService = as
}

// SetSearchIndex wires the Bleve index for Bleve-backed search.
// Calling with nil reverts to the Store.SearchBooks fallback.
func (svc *AudiobookService) SetSearchIndex(idx *search.BleveIndex) {
	svc.searchIndex = idx
}

// SetITunesEnqueuer wires (or re-wires) the iTunes write-back batcher.
// Nil disables iTunes side-effects in delete/purge paths.
func (svc *AudiobookService) SetITunesEnqueuer(e ITunesEnqueuer) {
	svc.itunesEnqueuer = e
}

// NewAudiobookService creates a new AudiobookService instance
func NewAudiobookService(store audiobookStore) *AudiobookService {
	libGen, resolved := database.LibraryGenerationOf(store)
	if !resolved {
		slog.Warn("audiobook list cache: store exposes no library generation counter, "+
			"list entries will only expire by TTL",
			"store_type", fmt.Sprintf("%T", store))
	}
	return &AudiobookService{
		store: store,
		// MAYDEPLOY-I4: cap entry count via LRU so 24h TTL doesn't allow
		// unbounded growth of full Book payloads.
		bookCache: cache.NewWithLimit[*database.Book]("book", 24*time.Hour, 5000),
		// TTL cut from 24h to listCacheTTL: with generation keying a book
		// mutation already puts stale pages out of reach, so the TTL only has
		// to bound mutation paths that bypass the store's three book-level
		// writes. See listCacheTTL.
		listCache: cache.NewWithLimit[[]database.Book]("audiobook_list", listCacheTTL, 500),
		libGen:    libGen,
	}
}

// listCacheTTL bounds how long a cached library page can outlive a change that
// did not go through CreateBook / UpdateBook / DeleteBook — a direct memdb
// write, a batch path, or an index-only edit. Generation keying handles the
// three store writes precisely, so this is a backstop rather than the primary
// mechanism, and 24 hours was far too long to serve that role: the phantom
// merged books stayed on the library page for a full day because nothing
// invalidated the entry and the LRU never evicted it (library-page keys are the
// most recently used, so they are the last candidates for capacity eviction).
//
// Ten minutes keeps the warm-cache benefit for normal browsing while capping
// the blast radius of an unknown-path mutation at a single-digit number of
// minutes.
const listCacheTTL = 10 * time.Minute

// InvalidateBookCaches clears all book-related caches. Called after any
// mutation (create, update, delete) to keep reads consistent.
//
// Order matters: invalidate bookCache first, then listCache. If we did it the
// other way around, a concurrent reader could miss the list cache (just
// cleared), re-fetch a fresh list from the DB, but still hit stale individual
// book entries that haven't been invalidated yet. By clearing individual books
// first, any concurrent reader that re-fetches the list will also get fresh
// individual books on subsequent lookups.
//
// When config.CacheInvalidateOnBookUpdate is false (the default), only the
// per-book cache is cleared; the list/facets caches are left warm so metadata
// fetches and write-back operations do not reset library page performance.
func (svc *AudiobookService) InvalidateBookCaches() {
	svc.bookCache.InvalidateAll()
	if config.AppConfig.CacheInvalidateOnBookUpdate {
		svc.listCache.InvalidateAll()
	}
}

// derefStr safely dereferences a *string, returning "" for nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefInt safely dereferences a *int, returning 0 for nil.
func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// derefInt64 safely dereferences a *int64, returning 0 for nil.
func derefInt64(i *int64) int64 {
	if i == nil {
		return 0
	}
	return *i
}
