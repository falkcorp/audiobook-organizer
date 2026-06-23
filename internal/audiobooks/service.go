// file: internal/audiobooks/service.go
// version: 1.33.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-23

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
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// audiobookStore is the narrow slice of database.Store that
// AudiobookService actually needs — both for its own method calls
// and for the helpers it forwards the store to (asExternalIDStore,
// NewMetadataStateService). Declared as a named composite so the
// service's dependency surface is inspectable in one place.
type audiobookStore interface {
	database.BookStore
	database.AuthorStore
	database.SeriesStore
	database.NarratorStore
	database.BookFileStore
	database.HashBlocklistStore
	database.TagStore
	// Transitively required — audiobook_service forwards svc.store to
	// NewMetadataStateService for change history tracking and to
	// asExternalIDStore for tombstone cleanup.
	database.MetadataStore
	database.UserPreferenceStore
	// Per-user filter pass on the listing endpoint reads UserBookState
	// to evaluate read_status / progress_pct / last_played.
	database.UserPositionStore
	// Needed by isProtectedPath to compare absolute paths against
	// configured import roots (SERVER-GLOBAL-STORE-AUDIT phase 6).
	// Single method inline rather than database.ImportPathStore so
	// the narrower audiobookUpdateStore adapter doesn't have to
	// implement the full ImportPath CRUD surface.
	GetAllImportPaths() ([]database.ImportPath, error)
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
	return &AudiobookService{
		store: store,
		// MAYDEPLOY-I4: cap entry count via LRU so 24h TTL doesn't allow
		// unbounded growth of full Book payloads.
		bookCache: cache.NewWithLimit[*database.Book]("book", 24*time.Hour, 5000),
		listCache: cache.NewWithLimit[[]database.Book]("audiobook_list", 24*time.Hour, 500),
	}
}

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

// cmpTime compares two *time.Time values, treating nil as zero time.
func cmpTime(a, b *time.Time) int {
	ta := time.Time{}
	tb := time.Time{}
	if a != nil {
		ta = *a
	}
	if b != nil {
		tb = *b
	}
	if ta.Before(tb) {
		return -1
	}
	if ta.After(tb) {
		return 1
	}
	return 0
}
