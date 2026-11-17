// file: internal/books/store/iface_metadata.go
// version: 1.13.0
// guid: 4c6267a6-b5ae-4e10-bce6-94b362c33a3f
// last-edited: 2026-11-17
//
// METADATA-CACHED-MATCHER: storage surface for the per-book
// metadata-candidate cache. Cache lives under PebbleDB key prefix
// "metadata_cache:<book_id>". One JSON blob per book holds the top-N
// candidates returned by the last fetch chain run. 30-day TTL.

package store

import (
	"encoding/json"
	"slices"
	"sort"
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
	// OwedSources records the chain sources (not the fallback providers:
	// FallbackAttempts is theirs) that could not be asked, or could not be
	// heard from, when the last search wrote this row -- Audnexus on a 5xx,
	// a timeout or a breaker hold -- keyed by display name like EmptyAnswers.
	// "Could not ask" is not "has nothing", and a row that holds candidates
	// from the OTHER sources still owes that source its answer.
	//
	// It exists because the candidates' 30-day freshness (MetadataCacheTTL)
	// says nothing about a source that never answered: without it a book
	// whose Audnexus lookup failed in the run that found its Open Library or
	// Google Books candidate was parked on those review-only candidates for a
	// month, and Audnexus -- the one source that answers for the book's own
	// ASIN, with a runtime and narrators that can be bulk-applied -- was
	// never asked again. The verdict (metafetch.CachedBatchVerdict) and the
	// scheduled selection read it: a source that is owed AND due
	// (OwedSource.Due) is re-asked, alone, on a bounded backoff
	// (OwedBackoff) that ends at OwedMaxAsks asks.
	//
	// Kept across writes for the same fingerprint and ASIN, dropped when
	// either changes (the question it owes is another one), and cleared per
	// source the moment that source answers. A row written before this field
	// existed decodes to nil: it owes nothing, as before.
	OwedSources map[string]OwedSource `json:"owed_sources,omitempty"`
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

// OwedSource is one chain source's unanswered question on a cache row
// (MetadataCandidateCache.OwedSources).
type OwedSource struct {
	// Since is when the source first failed to answer these inputs.
	Since time.Time `json:"since"`
	// LastAttempt is when it was last asked and did not answer. The backoff
	// counts from here.
	LastAttempt time.Time `json:"last_attempt"`
	// Attempts counts the asks that reached the provider and failed. A hold
	// (an open breaker, a throttle) sent nothing, so it moves LastAttempt and
	// not this: an outage of the provider's own making must not use up the
	// book's asks.
	Attempts int `json:"attempts,omitempty"`
	// Cause is why the last ask failed (metadata.Cause* word), for an operator
	// reading the row.
	Cause string `json:"cause,omitempty"`
}

// OwedBackoff is the wait before the re-ask that follows the n-th failed ask
// (index n-1): 6 h, then 24 h, 3 d, 7 d. A fifth failure ends the re-asks
// (OwedMaxAsks), 270 h = 11.25 days after the first failure (6 + 24 + 72 + 168
// h) -- inside the row's 30-day TTL, and short of "every tick, forever" for an
// ASIN the provider will never answer.
// A source's asks are the only ones this spends: the fallback providers, and
// so the Google Books budget, are never re-asked on its account.
var OwedBackoff = [...]time.Duration{6 * time.Hour, 24 * time.Hour, 72 * time.Hour, 168 * time.Hour}

// OwedMaxAsks is the most asks (the first, which left the source owed, and the
// re-asks) a source gets for one row before it is left alone.
const OwedMaxAsks = len(OwedBackoff) + 1

// Exhausted reports that the source has used all OwedMaxAsks asks: the
// scheduled selection and the verdict no longer re-ask it. What starts it over:
//   - a forced refetch (an explicit owner retry) that asks the source and sees it
//     fail again restarts its schedule at the first ask (Attempts 1, 6 h), not
//     one more step on a spent one;
//   - a refetch of the row after its TTL does not carry the debt: a source that
//     fails in it is recorded as a first failure, with a fresh schedule;
//   - a change of the book's search inputs or ASIN drops the debt with the row's
//     questions, and the batch fetch asks everything again.
func (o OwedSource) Exhausted() bool { return o.Attempts >= OwedMaxAsks }

// NextAttempt is when the source is next due, false when it is exhausted.
func (o OwedSource) NextAttempt() (time.Time, bool) {
	if o.Exhausted() {
		return time.Time{}, false
	}
	i := max(o.Attempts, 1) - 1
	return o.LastAttempt.Add(OwedBackoff[i]), true
}

// Due reports whether the source should be re-asked at now.
func (o OwedSource) Due(now time.Time) bool {
	at, ok := o.NextAttempt()
	return ok && !now.Before(at)
}

// OwedDue returns, sorted, the sources of active that the row still owes an
// answer and whose backoff has run out at now: the ones to re-ask, alone. A
// nil row owes nothing.
func (c *MetadataCandidateCache) OwedDue(now time.Time, active []string) []string {
	if c == nil || len(c.OwedSources) == 0 {
		return nil
	}
	var due []string
	for _, name := range active {
		if o, ok := c.OwedSources[name]; ok && o.Due(now) {
			due = append(due, name)
		}
	}
	sort.Strings(due)
	return slices.Compact(due)
}

// OwedPending reports whether any owed source can still be re-asked (is not
// exhausted), whatever its backoff says now. It is the cheap row-level flag
// MetadataCacheSummary.Owed mirrors.
func (c *MetadataCandidateCache) OwedPending() bool {
	if c == nil {
		return false
	}
	for _, o := range c.OwedSources {
		if !o.Exhausted() {
			return true
		}
	}
	return false
}

// OwedPendingSources returns, sorted, the sources the row owes an answer that
// can still be re-asked (not exhausted). The review lane's "still owed" state.
func (c *MetadataCandidateCache) OwedPendingSources() []string { return c.owedSources(false) }

// OwedExhaustedSources returns, sorted, the sources that used all OwedMaxAsks
// asks and are left alone: books that gave up on a source. The review lane's
// "gave up" state.
func (c *MetadataCandidateCache) OwedExhaustedSources() []string { return c.owedSources(true) }

func (c *MetadataCandidateCache) owedSources(exhausted bool) []string {
	if c == nil {
		return nil
	}
	var out []string
	for name, o := range c.OwedSources {
		if o.Exhausted() == exhausted {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
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
	// Owed mirrors MetadataCandidateCache.OwedPending: the row owes a chain
	// source an answer it can still be asked for, so the candidate fetch
	// selection reads the row (and its backoff) for a book that holds
	// candidates and has no fallback provider enabled.
	Owed bool `json:"owed,omitempty"`
	// OwedExhausted mirrors "any owed source used all its asks"
	// (MetadataCandidateCache.OwedExhaustedSources): the candidate fetch
	// selection counts the books that gave up on a source.
	OwedExhausted bool `json:"owed_exhausted,omitempty"`
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

// AlternativeTitleStore reads alternative titles for a book. Nothing writes
// them any more: the HTTP routes that did were retired (01-P81d) and the
// writers deleted with them.
type AlternativeTitleStore interface {
	GetBookAlternativeTitles(bookID string) ([]BookAlternativeTitle, error)
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
