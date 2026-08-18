// file: internal/database/iface_metadata.go
// version: 1.1.0
//
// METADATA-CACHED-MATCHER: storage surface for the per-book
// metadata-candidate cache. Cache lives under PebbleDB key prefix
// "metadata_cache:<book_id>". One JSON blob per book holds the top-N
// candidates returned by the last fetch chain run. 30-day TTL.

package database

import (
	"encoding/json"
	"time"
)

// MetadataCandidateCache is the persisted top-N metadata candidates
// returned by the last fetch for a book, keyed by book_id under the
// "metadata_cache:" PebbleDB namespace. Cache entries are replace-only
// (no merging) and have a 30-day staleness flag — see IsFresh().
//
// This is the canonical read source for the metadata-review UI.
// OperationResult rows for "metadata_candidate_fetch" remain for
// progress UI but are not consulted on read.
type MetadataCandidateCache struct {
	BookID string `json:"book_id"`
	// Candidates is the top-10 list from the last fetch, in score order.
	// The element type is opaque to the storage layer — handlers
	// JSON-decode into metafetch.MetadataCandidate at the boundary.
	Candidates []json.RawMessage `json:"candidates"`
	FetchedAt  time.Time         `json:"fetched_at"`
	// SourceHash captures the search inputs (title, author, narrator,
	// series, isbn10/13, asin) so v2 can detect "book metadata mutated
	// since cache" without parsing candidates. Diagnostic only in v1.
	SourceHash string `json:"source_hash"`
}

// MetadataCacheTTL is the freshness window. Entries older than this
// are still readable but the UI flags them and offers a Refresh.
const MetadataCacheTTL = 30 * 24 * time.Hour

// Age returns how long ago the cache was written.
func (c *MetadataCandidateCache) Age() time.Duration {
	if c == nil {
		return 0
	}
	return time.Since(c.FetchedAt)
}

// IsFresh reports whether the cache is younger than MetadataCacheTTL.
// Stale caches are still returned to callers; freshness is informational.
func (c *MetadataCandidateCache) IsFresh() bool {
	return c != nil && c.Age() < MetadataCacheTTL
}

// MetadataCacheSummary is the lightweight per-entry record returned
// by ListMetadataCacheKeys for the Review popup enumeration.
type MetadataCacheSummary struct {
	BookID         string    `json:"book_id"`
	FetchedAt      time.Time `json:"fetched_at"`
	CandidateCount int       `json:"candidate_count"`
}

// MetadataCacheStore is the persistence layer for the per-book
// metadata-candidate cache.
type MetadataCacheStore interface {
	// GetMetadataCache returns the cached entry for a book, or (nil, nil)
	// when no entry exists. A non-nil entry past MetadataCacheTTL is
	// still returned — staleness is the caller's call.
	GetMetadataCache(bookID string) (*MetadataCandidateCache, error)
	// PutMetadataCache replaces the cache entry for entry.BookID.
	// Idempotent. Always overwrites — there is no merge semantics.
	PutMetadataCache(entry *MetadataCandidateCache) error
	// DeleteMetadataCache removes the entry for bookID. Missing-key is
	// not an error.
	DeleteMetadataCache(bookID string) error
	// ListMetadataCacheKeys returns one summary per cached entry,
	// ordered by FetchedAt descending. Caller paginates.
	ListMetadataCacheKeys() ([]MetadataCacheSummary, error)
}

// Split out of iface_misc.go on 2026-08-18, which held 27 interface
// declarations in one file. A file named `misc` is where wide interfaces go to
// avoid review: BookFileStore reached 27 methods while living there.

// MetadataFieldStateStore tracks per-field metadata state.
type MetadataFieldStateStore interface {
	GetMetadataFieldStates(bookID string) ([]MetadataFieldState, error)
	UpsertMetadataFieldState(state *MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
}

// MetadataChangeStore records and reads metadata change history.
type MetadataChangeStore interface {
	RecordMetadataChange(record *MetadataChangeRecord) error
	GetMetadataChangeHistory(bookID string, field string, limit int) ([]MetadataChangeRecord, error)
	GetBookChangeHistory(bookID string, limit int) ([]MetadataChangeRecord, error)
}

// AlternativeTitleStore covers alternative titles for a book.
type AlternativeTitleStore interface {
	GetBookAlternativeTitles(bookID string) ([]BookAlternativeTitle, error)
	AddBookAlternativeTitle(bookID, title, source, language string) error
	RemoveBookAlternativeTitle(bookID, title string) error
	SetBookAlternativeTitles(bookID string, titles []BookAlternativeTitle) error
}

// MetadataStore covers MetadataFieldState, change history, and
// alternative titles.
//
// Split into the 3 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it, because every implementation -- PebbleStore (496 methods)
// and database.MockStore (399) among them -- fails to compile on a dropped or
// re-signatured method.
type MetadataStore interface {
	MetadataFieldStateStore
	MetadataChangeStore
	AlternativeTitleStore
}

// RejectedMetadataStore records candidates that were rejected (user action,
// below-threshold, duration mismatch, wrong language) so auditing is possible.
type RejectedMetadataStore interface {
	AddMetadataRejection(r MetadataRejection) error
	GetMetadataRejections(bookID string) ([]MetadataRejection, error)
	DeleteMetadataRejections(bookID string) error
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
