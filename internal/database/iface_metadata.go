// file: internal/database/iface_metadata.go
// version: 1.10.0
// guid: 4c6267a6-b5ae-4e10-bce6-94b362c33a3f
// last-edited: 2026-10-09
//
// METADATA-CACHED-MATCHER: storage surface for the per-book
// metadata-candidate cache. Cache lives under PebbleDB key prefix
// "metadata_cache:<book_id>". One JSON blob per book holds the top-N
// candidates returned by the last fetch chain run. 30-day TTL.

package database

import (
	"encoding/json"
	"strings"
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
	// LastEmptyFetchAt records the most recent search that ran against these
	// same inputs and came back with nothing. It exists so that "we looked and
	// found nothing" can be written down WITHOUT erasing candidates an earlier
	// search did find — see metafetch.cacheSearchResponse.
	//
	// FetchedAt cannot carry this. FetchedAt dates the candidates, and moving
	// it on an empty result would relabel month-old candidates as fresh. But
	// without a second timestamp a preserved entry stays permanently past the
	// TTL, so every stale-refetch pass would pick the same never-matchable
	// books again forever. This field is what lets a caller distinguish
	// "nobody has looked at this in 30 days" from "we looked an hour ago and
	// the providers have nothing".
	LastEmptyFetchAt *time.Time `json:"last_empty_fetch_at,omitempty"`
	// SearchFingerprint identifies the questions the search that wrote this
	// entry asked the providers: ladder version, book title, and the RESOLVED
	// title/author/narrator (metafetch searchInputs.fingerprint). SourceHash
	// hashes the caller's hints and misses an author resolved from AuthorID;
	// this does not. The batch fetch trusts the entry only when the book's
	// current fingerprint matches it. Empty on entries written before
	// 2026-09-19, which are therefore never trusted as a verdict.
	SearchFingerprint string `json:"search_fingerprint,omitempty"`
	// EmptyAnswers maps a metadata source to when it last ANSWERED these
	// inputs (SearchFingerprint) with nothing: its whole query ladder ran and
	// no rung errored, was throttled or was cancelled. A source that could
	// not be asked is absent: "could not ask" is not "has nothing". Each
	// answer ages on its own against MetadataKnownEmptyTTL, and only
	// providers without a valid answer are re-asked. Reset whenever the
	// fingerprint changes; nil whenever the entry holds fresh candidates.
	EmptyAnswers map[string]time.Time `json:"empty_answers,omitempty"`
	// FetchedForASIN is the ASIN the book carried when these candidates were
	// fetched: the fetch records it on every row it writes (empty when the
	// book had none), and a fetch that found nothing and kept the earlier
	// candidates keeps the earlier value. For a row written before
	// 2026-10-05, which has none, the store records the book's old ASIN the
	// first time it sees that ASIN replaced or cleared while the row is kept
	// (PebbleStore.updateBookLockedMode).
	//
	// It exists because an ASIN change keeps the cached candidates (it is not
	// a search-identity change, candidateSearchIdentityChanged), and the
	// gate's asin_conflict check cannot see the problem for a candidate that
	// carries no ASIN at all (Open Library, Google Books): after the book's
	// ASIN is REPLACED, such a candidate was found for a book identified by
	// another record. ASINReplaced is the reader.
	FetchedForASIN string `json:"fetched_for_asin,omitempty"`
	// FallbackAttempts records, per fallback provider (by display name, as
	// EmptyAnswers is keyed), the batch candidate fetch's last attempt to ask
	// it about these inputs (SearchFingerprint): Open Library and Google
	// Books are asked only when the rest of the chain found no usable
	// candidate (server candidate_fallback.go). Kept across writes for the
	// same fingerprint, dropped when it changes, like EmptyAnswers.
	//
	// It exists for two readers. The scheduled selection orders the books
	// Google Books still owes by the oldest attempt, so a book whose lookup
	// keeps being deferred does not hold its place at the head of a capped
	// day while others never get one. And a SETTLED attempt -- the provider
	// gave an answer that stands (candidates, which are merged into the row,
	// or a permanent refusal such as a 400) -- stops the fallback asking again
	// within MetadataKnownEmptyTTL, the way an EmptyAnswers entry does for an
	// empty answer. A deferral (budget spent, throttle hold, 429/5xx/timeout)
	// is an attempt but never settles.
	FallbackAttempts map[string]FallbackAttempt `json:"fallback_attempts,omitempty"`
	// Stale marks a row whose candidates were fetched for a search identity
	// the book no longer has (it was retitled or re-credited after the fetch).
	// This is NOT the TTL freshness of IsFresh() (the review page's existing
	// "stale" chip): it means "the book's search identity changed after these
	// candidates were fetched". Every apply gate refuses a candidate from such
	// a row (metafetch.CandidateIdentityStale) and the candidate fetch picks
	// the book for refetch; a refetch writes a new row, which clears the flag
	// by replacement. Existing rows decode as false.
	Stale bool `json:"stale,omitempty"`
	// StaleQuestionFP is the SearchFingerprint of the question the row was
	// stale against, for an operator reading a refusal. Empty unless Stale.
	StaleQuestionFP string `json:"stale_question_fp,omitempty"`
}

// FallbackDeferred reports whether a fallback provider's last attempt for
// this row was DEFERRED (budget spent, throttle hold, a passing failure):
// the book is waiting on a later run. The review page's "deferred" chip.
func (c *MetadataCandidateCache) FallbackDeferred() bool {
	if c == nil {
		return false
	}
	for _, a := range c.FallbackAttempts {
		if !a.Settled && a.Outcome == FallbackOutcomeDeferred {
			return true
		}
	}
	return false
}

// FallbackOutcomeDeferred is FallbackAttempt.Outcome for a deferred attempt
// (the same string as metabatch.FallbackDeferred).
const FallbackOutcomeDeferred = "deferred"

// FallbackAttempt is one fallback provider's last attempt for a cache row
// (MetadataCandidateCache.FallbackAttempts).
type FallbackAttempt struct {
	At time.Time `json:"at"`
	// Settled marks an answer that stands for MetadataKnownEmptyTTL.
	Settled bool `json:"settled,omitempty"`
	// Outcome is the fallback step's outcome (matched, no_match, error,
	// deferred), for an operator reading the row.
	Outcome string `json:"outcome,omitempty"`
}

// ASINReplaced reports whether the book's ASIN (bookASIN, its current value)
// differs from the ASIN these candidates were fetched for (FetchedForASIN),
// and returns that ASIN. An empty FetchedForASIN is "fetched for a book with
// no ASIN, or not recorded": filling an empty ASIN never makes a candidate
// wrong, so it never reads as replaced. A cleared ASIN does: the record the
// candidates were matched against was taken off the book. Case and
// surrounding space are ignored.
func (c *MetadataCandidateCache) ASINReplaced(bookASIN *string) (string, bool) {
	if c == nil {
		return "", false
	}
	was := strings.TrimSpace(c.FetchedForASIN)
	if was == "" {
		return "", false
	}
	now := ""
	if bookASIN != nil {
		now = strings.TrimSpace(*bookASIN)
	}
	return was, !strings.EqualFold(was, now)
}

// MetadataCacheTTL is the freshness window. Entries older than this
// are still readable but the UI flags them and offers a Refresh.
const MetadataCacheTTL = 30 * 24 * time.Hour

// MetadataKnownEmptyTTL bounds a "every provider answered with nothing" verdict:
// provider catalogs add releases, so after this the batch fetch asks again.
const MetadataKnownEmptyTTL = 90 * 24 * time.Hour

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
	// Stale mirrors MetadataCandidateCache.Stale so the candidate fetch
	// selection can pick stale rows without decoding every entry.
	Stale bool `json:"stale,omitempty"`
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

// AIJobsStore is the subset of Store used by internal/ai/aijobs. It is
// composed from a reader and a writer to stay under the interfacebloat limit.
type AIJobsStore interface {
	AIJobReader
	AIJobWriter
}

// AIJobReader reads ai_jobs rows and their payloads.
type AIJobReader interface {
	GetAIJob(id string) (AIJob, error)
	GetAIJobByBatchID(batchID string) (AIJob, error)
	GetAIJobPayload(id string) ([]byte, error)
	ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]AIJob, error)
}

// AIJobWriter creates ai_jobs rows and moves them through their lifecycle.
type AIJobWriter interface {
	CreateAIJob(job AIJob, payloadJSON []byte) error
	MarkAIJobSubmitted(id, batchID string) error
	MarkAIJobCompleted(id, status string, successCount, errorCount int, rowErrors []AIJobRowError) error
	MarkAIJobFailed(id, errMsg string) error
	// MarkAIJobApplyFailed records a failed attempt to apply a job's results:
	// status "apply_failed", ApplyAttempts+1, LastApplyError, LastApplyAt. It
	// returns the updated row so the caller can decide whether to give up.
	MarkAIJobApplyFailed(id, errMsg string) (AIJob, error)
	// MarkAIJobApplied records that the callback applied the results, with the
	// outcome counts, without changing status. Written before MarkAIJobCompleted
	// so a failed completion mark never re-runs the callback.
	MarkAIJobApplied(id string, successCount, errorCount int, rowErrors []AIJobRowError) error
}
