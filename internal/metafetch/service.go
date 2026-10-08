// file: internal/metafetch/service.go
// version: 5.48.0
// guid: e5f6a7b8-c9d0-e1f2-a3b4-c5d6e7f8a9b0
// last-edited: 2026-10-07

package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/foldernames"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// WriteBackEnqueuer is satisfied by server.WriteBackBatcher.
type WriteBackEnqueuer interface {
	Enqueue(bookID string)
}

// forwardedStores is what metafetch hands its store to rather than calls
// itself. Every entry is another package's declared parameter type, so this is
// the literal forwarding requirement, and it is kept separate from the groups
// below because that is the distinction the compiler probe reports: a direct
// call fails as "has no field or method", a forwarding requirement as "does not
// implement".
type forwardedStores interface {
	// database.EnsureSingletonBookTag, from the tag write-back path.
	database.BookTagSingletonStore
	// The tagger deps struct built in service_writeback.go.
	database.BookFileHashUpdater
	// database.{Get,Put}CachedMetadataFetch, the fetch/search response cache.
	database.RawKVStore
	// hasCheckpoint / setCheckpoint / clearCheckpoints in the write-back
	// phase gate, which forward to organizer.
	database.UserPreferenceStore
	// newPathOrganizer, for computing an organized destination path.
	organizer.OrganizerStore
	// organizer.NewService, for the organize service ensureLibraryCopy routes
	// a protected book's library copy through (OrganizeOneBook +
	// CreateOrganizedVersion). Adds six methods over OrganizerStore's four
	// (GetAllBooksCore, DeleteBook, BatchCreateBookFiles, CreateOperationChange,
	// SaveOperationParams, DeleteOperationState); every store handed to
	// NewService already had them.
	organizer.Store
	// foldernames.IsRealSeries, the search parse's series evidence
	// (nameEvidence): tells a real series row from author junk.
	foldernames.PointStore
}

// metadataCacheStore is the per-book candidate cache in cache.go.
type metadataCacheStore interface {
	GetMetadataCache(bookID string) (*database.MetadataCandidateCache, error)
	PutMetadataCache(entry *database.MetadataCandidateCache) error
	ListMetadataCacheKeys() ([]database.MetadataCacheSummary, error)
	DeleteMetadataCache(bookID string) error
}

// metadataFieldStateStore is the per-field manual-override state plus the
// change-history record written whenever a fetched value is applied.
type metadataFieldStateStore interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	UpsertMetadataFieldState(state *database.MetadataFieldState) error
	DeleteMetadataFieldState(bookID, field string) error
	RecordMetadataChange(record *database.MetadataChangeRecord) error
	// GetBookChangeHistory is read by UndoLastApply to find the last apply's
	// batch of rows.
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
}

// metafetchBookStore is the book entity: lookup, create, update, plus the two
// duplicate-detection views and the flag their match writes.
type metafetchBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	CreateBook(book *database.Book) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetBookTags(bookID string) ([]string, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
	FlagMetadataHashDuplicate(primaryID, duplicateID string) error
}

// metafetchFileStore is everything about bytes on disk: the file rows, the
// move/rename bookkeeping the write-back path records, and the import roots a
// destination path is checked against.
//
// Split into the two halves below on 2026-09-07, when the collision resolver's
// GetBookFileByID took it to 9 declared entries and over the interfacebloat
// limit of 8. The name is retained as their composition so the method set stays
// byte-identical and no consumer moves.
type metafetchFileStore interface {
	metafetchFileReader
	metafetchFileWriter
	// PatchBookFileFields is the field-level book_file write the segment-titles
	// pass uses instead of a whole-row UpdateBookFile of a stale read.
	PatchBookFileFields(bookID, fileID string, patch database.BookFileFieldPatch) (*database.BookFile, *database.BookFile, error)
}

// metafetchFileReader looks up file rows and the import roots a destination
// path is checked against.
type metafetchFileReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	// GetBookFileByID rehydrates the FULL record. The collision resolver
	// (organizer.CollisionStore) needs it because UpdateBookFile is a
	// full-record replacement: repointing from a partial record would wipe the
	// fingerprint, transcript and tags on the row.
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	GetAllImportPaths() ([]database.ImportPath, error)
	// GetChaptersForBook is the chapter-table fallback versionprimary's
	// Loader reads when MATCH-4 ranks a single m4b's content tier.
	database.ChapterReader
}

// metafetchFileWriter creates and mutates file rows and records the
// move/rename bookkeeping the write-back path produces.
type metafetchFileWriter interface {
	CreateBookFile(file *database.BookFile) error
	UpdateBookFile(id string, file *database.BookFile) error
	RecordPathChange(change *database.BookPathChange) error
	SetLastWrittenAt(id string, t time.Time) error
	MarkNeedsRescan(bookID string) error
}

// metafetchContributorStore is the get-or-create pass service_apply.go runs when
// a fetched result names an author or series that may not exist yet.
type metafetchContributorStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	// ModifyBookAuthors is the atomic read-merge-write the fill-only apply
	// uses (applyAuthorCredit), so concurrent applies cannot drop each other's
	// added author.
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
}

// metafetchNarratorStore is the narrator half. Separate from the contributor
// group above because the apply path never creates a narrator by name -- it
// only reads and rewrites the join rows.
type metafetchNarratorStore interface {
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error
	GetNarratorByID(id int) (*database.Narrator, error)
}

// Store is the dependency surface of Service, measured with an
// empty-interface compiler probe run under -gcflags=-e: 36 direct calls and
// five forwarding constraints, 64 distinct methods in total.
//
// It was previously database.Store -- 398 methods. The probe could not report
// this set until two things below it were narrowed, because both of them took
// database.Store and so re-imposed the union on anything that forwarded to
// them: database.EnsureSingletonBookTag, and the three checkpoint helpers in
// pipeline_checkpoint.go (which forward to organizer, where the same three
// functions already declared database.UserPreferenceStore -- the metafetch copy
// was an unnarrowed duplicate of an already-narrowed twin).
//
// Exported, unlike most consumer-side interfaces here, because a caller that
// forwards its own store into NewService has to be able to name this
// requirement -- see audiobooks.NewOrganizeService, which composes it with
// organizer.Store. organizer.Store is exported for the same reason.
type Store interface {
	forwardedStores

	metadataCacheStore
	metadataFieldStateStore
	metafetchBookStore
	metafetchFileStore
	metafetchContributorStore
	metafetchNarratorStore
}

type Service struct {
	db              Store
	olStore         *openlibrary.OLStore
	overrideSources []metadata.MetadataSource // for testing
	// asinLookupOverride replaces the live Audible/Audnexus ASIN clients in
	// lookupASIN (tests only; nil in production).
	asinLookupOverride func(ctx context.Context, providerID, asin string) (*metadata.BookMetadata, error)
	// asinBackfillQueue is set by the metafetch plugin's PostInit while the
	// server may already be applying metadata, so it is read and written
	// atomically.
	asinBackfillQueue atomic.Pointer[ASINBackfillQueue]
	activityService   *activity.Service
	dedupEngine       *dedup.Engine
	metadataScorer    ai.MetadataCandidateScorer // optional; nil = fallback to F1
	llmScorer         ai.MetadataCandidateScorer // optional; nil = no LLM rerank tier
	writeBackBatcher  WriteBackEnqueuer
	// safeWriteDeps guards tag/cover writes against Deluge-protected paths.
	// Zero-value = no guard (writes proceed in-place). Set via SetSafeWriteDeps.
	safeWriteDeps tagger.SafeWriteDeps

	// Memoized metadata-source chain. BuildSourceChain builds the client chain
	// (each source wrapped in a circuit breaker) ONCE and reuses it across every
	// per-book fetch so the Hardcover 60-rpm limiter (its requestLog) and the
	// per-source circuit breakers accumulate/persist across a whole batch instead
	// of being recreated per book. cachedChainFP is a fingerprint of the metadata
	// source config; the chain is rebuilt only when that changes (settings edit),
	// so runtime config changes are still honored. Guarded by chainMu. The chain
	// itself is safe for concurrent use by the worker pools that share it: every
	// source client is stateless (http.Client + immutable config), and the
	// Hardcover limiter and CircuitBreaker each carry their own mutex.
	chainMu       sync.Mutex
	cachedChain   []metadata.MetadataSource
	cachedChainFP string

	// The organize service ensureLibraryCopy routes through, built on first
	// use by libraryOrganizeService.
	organizeOnce sync.Once
	organizeSvc  *organizer.Service

	// tagWriter replaces the audio-tag write in tests so they can count it
	// (see writeTags). Nil in production.
	tagWriter func(id string) (int, error)

	// coverDownload replaces metadata.DownloadCoverArt in tests (its SSRF guard
	// refuses loopback, so an httptest server cannot stand in). Nil in production.
	coverDownload func(coverURL, destDir, bookID string) (string, error)

	// libraryCopyMaker replaces the organizer's copy creation in
	// ensureLibraryCopy for tests, which cannot stand up a real organize (it is
	// asked only once no usable copy exists). Nil in production.
	libraryCopyMaker func(book *database.Book) *database.Book

	// fileWorkScheduler runs auto-fetch's file work through the server's
	// file-I/O pool (SetFileWorkScheduler). Nil means no pool is wired --
	// organize's per-call service, tests -- and auto-fetch then downloads the
	// cover only and touches no audio file.
	fileWorkScheduler FileWorkScheduler

	// pathLock is the server's per-path write lock (SetPathLocker). The file
	// work takes it itself, on the path it is about to write (fileWorkTarget,
	// lockWriteTarget). Nil means no lock: tests and organize's per-call service.
	pathLock func(path string) func()

	// fileWriteSlot is a NON-BLOCKING take on the server's process-wide
	// write-back gate (SetFileWriteGate). The per-file tag writes of one book
	// use it to add writers beyond the first; see runFileWrites. Nil means no
	// extra writers: every book's files are written one at a time.
	fileWriteSlot func() (release func(), ok bool)

	// fileTagWrite replaces the per-file safe tag write (backup + WriteTagsSafe)
	// in tests so they can observe per-file concurrency. Nil in production.
	fileTagWrite func(path string, tagMap map[string]any) error
}

type FetchMetadataResponse struct {
	Message         string
	Book            *database.Book
	Source          string
	FetchedCount    int
	PendingCoverURL string // set by ApplyMetadataCandidate for background download
	// SkippedLockedFields lists the lock keys (database.UserLockableFields) the
	// apply refused to write because the user had locked or overridden them.
	// Nil when nothing was skipped. Callers surface it in op summaries and
	// responses so a locked field is visibly "kept", never silently dropped.
	SkippedLockedFields []string
}

// MetadataCandidate represents a single search result for manual metadata matching.
type MetadataCandidate struct {
	Title          string `json:"title"`
	Author         string `json:"author"`
	Narrator       string `json:"narrator,omitempty"`
	Series         string `json:"series,omitempty"`
	SeriesPosition string `json:"series_position,omitempty"`
	Year           int    `json:"year,omitempty"`
	Publisher      string `json:"publisher,omitempty"`
	ISBN           string `json:"isbn,omitempty"`
	ISBN10         string `json:"isbn10,omitempty"`
	ISBN13         string `json:"isbn13,omitempty"`
	ASIN           string `json:"asin,omitempty"`
	// Genre is carried so an apply can write it: ApplyMetadataToBook has always
	// written meta.Genre, but the candidate had no field for it, so a manual or
	// batch apply never set a genre.
	Genre       string `json:"genre,omitempty"`
	CoverURL    string `json:"cover_url,omitempty"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	Source      string `json:"source"`
	// FromCatalog marks a candidate read from the local author catalog
	// (BrowseSearch) rather than asked of the provider live. Source stays the
	// provider's name ("Audible"), which the apply path keys provenance on.
	FromCatalog bool `json:"from_catalog,omitempty"`

	// Content-matcher SIGNAL fields carried through the candidate so the review
	// UI and the metadata_cache sidecar (which the Phase 4 matcher reads) retain
	// them. Not user-served fields.
	Abridged                *bool   `json:"abridged,omitempty"`
	Subtitle                string  `json:"subtitle,omitempty"`
	PageCount               int     `json:"page_count,omitempty"`
	SeriesSecondary         string  `json:"series_secondary,omitempty"`
	SeriesSecondaryPosition string  `json:"series_secondary_position,omitempty"`
	Score                   float64 `json:"score"`
	// ScoreBreakdown is the ordered derivation of Score, for the review UI's
	// evidence panel. Replaying its steps reproduces Score -- asserted as a
	// property in service_scoring_breakdown_test.go, not merely hoped for.
	//
	// Nil when the candidate did not come from a scoring path (e.g. a
	// hand-constructed candidate in a test or a calibration harness). Nil means
	// "no derivation recorded"; it never means the score was zero.
	ScoreBreakdown *ScoreBreakdown `json:"score_breakdown,omitempty"`
	// DurationSec is the runtime from the metadata source (Audible: runtime_length_min × 60).
	// Zero means the source did not provide a duration.
	DurationSec int `json:"duration_sec,omitempty"`
	// DurationDeltaSec is abs(candidate_duration - book_duration) in seconds.
	// Zero means either side had no duration, or they matched exactly.
	// Non-zero lets the review UI flag candidates whose runtime diverges significantly.
	DurationDeltaSec int `json:"duration_delta_sec,omitempty"`
	// CategoryTags holds Audible category ladder node names (e.g. "Science Fiction").
	// Only populated for Audible-sourced candidates. Applied as book_tags on apply.
	CategoryTags []string `json:"category_tags,omitempty"`
	// DurationMismatch is true when DurationDeltaSec exceeds 600 s (10 min).
	// The review UI already renders a warning chip when duration_delta_sec > 600;
	// this flag makes the threshold decision explicit in the API response.
	DurationMismatch bool `json:"duration_mismatch,omitempty"`
	// DurationScore is the additive score component from the duration signal.
	// Positive when the candidate runtime closely matches the local file duration;
	// negative when the runtimes diverge significantly (wrong edition / abridged).
	// Zero when either side lacks duration data.
	// Scoring bands (delta ratio = |candidate_dur - book_dur| / book_dur):
	//   < 5%  → +20,  < 10% → +15,  < 20% → +10,
	//   > 50% → -10,  > 100% → -20.
	DurationScore float64 `json:"duration_score,omitempty"`
	// TranscriptionBoosted is true when this candidate's title, author, or
	// narrator matched the book's Whisper-transcribed intro fields, causing a
	// score multiplier to be applied. Lets the review UI surface a
	// "matched on transcription" filter so users can focus on books where
	// audio-derived metadata was the deciding factor.
	TranscriptionBoosted bool `json:"transcription_boosted,omitempty"`
	// AudibleRatingOverall is the Audible overall star rating (1–5 scale).
	// Zero means the source did not provide a rating.
	AudibleRatingOverall float64 `json:"audible_rating_overall,omitempty"`
	// AudibleRatingCount is the number of Audible star ratings.
	AudibleRatingCount int `json:"audible_rating_count,omitempty"`
	// GoogleRatingAverage is the Google Books average rating (1–5 scale).
	// Zero means the source did not provide a rating.
	GoogleRatingAverage float64 `json:"google_rating_average,omitempty"`
	// GoogleRatingCount is the number of Google Books ratings.
	GoogleRatingCount int `json:"google_rating_count,omitempty"`
	// IdentityEvidence records what vouched for this candidate's identity
	// when the book's stored title could not: set by the certainty gate
	// (internal/applygate) on the copy it judges, never by a provider and
	// never written back into the candidate cache, so a review pin's content
	// hash is unaffected. It is set only when the gate's verdict is not
	// identity_stale, owner_manual_only or owner_manual_check_failed. The
	// bulk-apply op log reads it, and the API and bulk-apply preview JSON
	// carry it; no web view renders it yet.
	IdentityEvidence *CandidateIdentityEvidence `json:"identity_evidence,omitempty"`
}

// IdentityEvidenceTranscribedTitle is CandidateIdentityEvidence.Kind for a
// candidate found by searching the book's transcribed (audio intro) title,
// whose own title matches that transcription.
const IdentityEvidenceTranscribedTitle = "transcribed_title"

// CandidateIdentityEvidence is one piece of identity evidence the certainty
// gate accepted for a candidate (MetadataCandidate.IdentityEvidence).
type CandidateIdentityEvidence struct {
	Kind string `json:"kind"`
	// Query is the text the candidate was found by (the transcribed title),
	// and Source where it came from (a metabatch.SearchQuerySource* value).
	Query  string `json:"query"`
	Source string `json:"source,omitempty"`
	// Detail is a one-line human explanation for the review UI and op log.
	Detail string `json:"detail"`
}

// SearchMetadataResponse is returned by SearchMetadataForBook.
type SearchMetadataResponse struct {
	Results       []MetadataCandidate `json:"results"`
	Query         string              `json:"query"`
	SourcesTried  []string            `json:"sources_tried"`
	SourcesFailed map[string]string   `json:"sources_failed,omitempty"`
	// SourcesAnswered names the sources whose query ladder completed without
	// an error, throttle or cancel (a fetch-cache hit counts). Internal: it is
	// what cacheSearchResponse records as a provider's "nothing" answer.
	SourcesAnswered []string `json:"-"`
	// SourcesAsked names the sources the search sent at least one question
	// (live or from the fetch cache). Internal: noSourceAnswered uses it so a
	// source that had nothing to be asked (Audnexus with no ASIN), which is
	// "answered" for the per-provider bookkeeping, cannot by itself make a
	// search whose every asked source failed look answered.
	SourcesAsked []string `json:"-"`
	// InputFingerprint identifies the questions this search asked (see
	// searchInputs.fingerprint). Internal, stored on the cache entry.
	InputFingerprint string `json:"-"`
	// BookASIN is the book's ASIN (trimmed) when the search read it, "" when
	// it had none. Internal: cacheSearchResponse records it on the cache row
	// as FetchedForASIN.
	BookASIN string `json:"-"`
	// LegacyFingerprint is the fingerprint the searchInputVersion "1" ladder
	// recorded for the same inputs (searchInputs.legacyFingerprint). Internal:
	// cacheSearchResponse keeps it on candidates carried over from such a row,
	// so they are never relabelled as answers to this version's questions.
	LegacyFingerprint string `json:"-"`
	// carryFilter drops, from legacy candidates cacheSearchResponse carries
	// over an empty refetch, the ones this search's position rules refuse
	// (strongCriteria.filterCarried).
	carryFilter func([]json.RawMessage) []json.RawMessage
	// sourceErrs is SourcesFailed with the error VALUES, so a caller can tell
	// a spent daily budget, a throttle hold or a 5xx from a permanent 4xx
	// (errors.Is / errors.As) instead of parsing strings. noSourceAnswered
	// joins them into the error it returns; SourceErrors reads them.
	sourceErrs map[string]error
	// mergeCached is SearchOptions.MergeWithCached, for cacheSearchResponse.
	mergeCached bool
	// mergeRank is SearchOptions.MergeRank, for cacheSearchResponse.
	mergeRank func(MetadataCandidate) int
	// carryFromHash and carryFromRow are SearchOptions.CarryFromSourceHash
	// and CarryFromRow, for cacheSearchResponse.
	carryFromHash string
	carryFromRow  bool
}

// SourceErrors returns the error each failed source returned (keyed like
// SourcesFailed), nil when none failed.
func (r *SearchMetadataResponse) SourceErrors() map[string]error {
	if r == nil {
		return nil
	}
	return r.sourceErrs
}

// CarryFrom makes the search's cache write carry row's candidates as a row
// for its own inputs' would be (CarryFromSourceHash, CarryFromRow). row is
// one Service.VouchedCachedRow returned; nil clears the carry.
func (o *SearchOptions) CarryFrom(row *MetadataCandidateCache) {
	if row == nil {
		o.CarryFromSourceHash, o.CarryFromRow = "", false
		return
	}
	o.CarryFromSourceHash, o.CarryFromRow = row.SourceHash, true
}

// SearchOptions carries optional per-request flags for SearchMetadataForBook.
// Adding a new option never breaks existing callers — they can keep using the
// zero-value or the simpler variadic signature.
type SearchOptions struct {
	// UseRerank asks the LLM rerank tier to run on the top candidates (if
	// MetadataLLMScoringEnabled is true on the server). When false, only
	// the base scorer tier runs.
	UseRerank bool

	// OnlySources, when non-empty, restricts the search to the named sources
	// (metadata.MetadataSource.Name()). The batch candidate fetch uses it to
	// re-ask only the providers that have no valid answer for the book's
	// current inputs, instead of every provider again.
	OnlySources []string

	// BypassProviderThrottle lets this ONE search call a provider that is
	// globally throttled.
	//
	// The rule the user set: automatic and bulk paths respect the hold; an
	// explicit, user-initiated lookup on a single book goes through anyway.
	// Bazarr draws the same line -- its throttle governs automatic searches,
	// not the button a human just pressed. One extra request cannot re-trigger
	// a quota block, and if it SUCCEEDS the registry clears the hold, so a
	// provider that recovered early is released by evidence instead of waiting
	// out a timer.
	//
	// Set it only where a human asked for exactly one book. Setting it on a
	// batch path silently deletes the whole feature.
	//
	// The same "a person is waiting" marks the search's provider lookups
	// INTERACTIVE for the daily quota budgets (dailyquota): such a search may
	// use the Google Books quota a background lookup leaves reserved.
	BypassProviderThrottle bool

	// MergeWithCached makes an answer WITH results merge into the book's
	// cached candidates for the same inputs instead of replacing them (every
	// other search replaces). The batch candidate fetch's provider fallback
	// sets it: it asks Open Library or Google Books only because the chain's
	// candidates were not usable, and replacing would silently drop those
	// candidates from the review list. The union is re-ranked by score.
	MergeWithCached bool

	// CarryFromSourceHash names the SourceHash of the book's row the caller
	// read and vouched for (Service.VouchedCachedRow) when that row was hashed
	// from other inputs than this search's: a row hashed under the raw author
	// credit before 2026-10-06's cleaning, or a pre-2026-09-28 no-author row.
	// cacheSearchResponse treats it exactly as a row for this search's own
	// inputs, on EVERY write path, not only a merge:
	//   - a merge (MergeWithCached) merges into its candidates;
	//   - an answer with no results keeps its candidates (preserve-on-empty);
	//   - a replacing answer keeps the fallback providers' candidates it did
	//     not re-ask.
	// Without it such a row is not the same inputs, and any write replaces
	// it: an empty scheduled or forced chain refetch wrote Candidates: [] over
	// a row the batch fetch had just vouched for, and every below-floor or
	// asin_conflict candidate the owner could still review was lost.
	// It counts only with CarryFromRow set, and may then be "": a legacy row
	// written before SourceHash existed, which the apply gate also accepts
	// (ValidateCachedIdentity fails open on it). Set both with CarryFrom.
	// Pass them only for a row the caller checked belongs to the book as it
	// is now.
	CarryFromSourceHash string
	// CarryFromRow turns CarryFromSourceHash on. A flag rather than a
	// non-empty hash, because "" is a real row's hash (the legacy hashless
	// row) and must not also mean "no row".
	CarryFromRow bool

	// MergeRank ranks the candidates of a write that unions fresh and
	// carried rows (a MergeWithCached answer, or a chain refetch keeping the
	// fallback providers' candidates): a lower rank comes first, whatever the
	// score, and the score orders candidates of one rank. The row is capped at
	// its top 10 AFTER ranking, so the worst-ranked rows are the ones evicted
	// when the union is larger. metabatch.MergeRanker is the batch fetch's:
	// usable candidates the gate may apply unattended, then usable review-only
	// ones (Open Library, Google Books), then refused-but-reviewable ones
	// (below the floor, asin_conflict), then owner-rejected ones -- so the
	// row's first candidate, the one the review list, bulk apply and the
	// transcription auto-apply read, is the best one there is, and an
	// owner-rejected candidate is the first to go. nil ranks review-only
	// candidates after every other, then by score (reviewOnlyLastRank).
	MergeRank func(MetadataCandidate) int

	// BypassFetchCache skips the per-source fetch-cache READ, so every
	// selected provider is asked again. Fresh non-empty results are still
	// WRITTEN, replacing the row. Set it wherever the caller asked to force or
	// refresh: the batch candidate fetch's force (a stale-refetch implies it)
	// and the search dialog's ?refresh=true. Without it a "forced" refetch
	// skipped the candidate-cache verdict only to be answered, provider by
	// provider, from the fetch cache's rows for the same identity, so force
	// re-asked nobody until those rows aged out.
	BypassFetchCache bool
}

// embedCoverInBookFiles embeds cover art into all audio files for a book.
// Always overwrites existing cover art. Before overwriting, extracts the old
// cover and saves it as a timestamped version in covers/history/ so it can be
// restored later via the changelog.
//
// book is the file-work target its caller resolved and locked (fileWorkTarget:
// the library copy, for a protected book). It used to resolve the copy again
// here, under a copy policy, which is how a step could write a copy its job
// had not locked; now it only refuses a protected book outright.
func (mfs *Service) embedCoverInBookFiles(book *database.Book, coverPath string) {
	if book == nil || book.FilePath == "" || coverPath == "" {
		return
	}

	// A TagLib CAPABILITY list — which containers can carry an embedded cover
	// picture — not supported_extensions. It stays narrow on purpose: .wav and
	// .aiff have no standard cover atom, and .aax/.aaxc are DRM-encrypted.
	coverEmbeddableExts := map[string]bool{
		".mp3": true, ".m4b": true, ".m4a": true, ".aac": true,
		".ogg": true, ".flac": true,
	}

	if mfs.isProtectedPath(book.FilePath) {
		slog.Warn("cannot embed cover: book is under a protected path; embeds go to its library copy",
			"book_id", logger.SanitizeLogValue(book.ID), "protected_path", logger.SanitizeLogValue(book.FilePath))
		return
	}

	// collectFiles gathers all audio files that need cover embedding
	var files []string
	ext := strings.ToLower(filepath.Ext(book.FilePath))
	if coverEmbeddableExts[ext] {
		files = append(files, book.FilePath)
	} else {
		// Multi-file book
		bookFiles, err := mfs.db.GetBookFiles(book.ID)
		if err != nil {
			slog.Warn("failed to list book files for cover embedding",
				"book_id", book.ID, "book_title", book.Title, "error", err)
			return
		}
		for _, bf := range bookFiles {
			if bf.Missing {
				continue
			}
			if mfs.isProtectedPath(bf.FilePath) {
				continue
			}
			bfExt := strings.ToLower(filepath.Ext(bf.FilePath))
			if coverEmbeddableExts[bfExt] {
				files = append(files, bf.FilePath)
			}
		}
	}

	if len(files) == 0 {
		return
	}

	newCoverData, _ := os.ReadFile(coverPath)
	newHash := ""
	if len(newCoverData) > 0 {
		newHash = fmt.Sprintf("%x", sha256.Sum256(newCoverData))[:12]
	}

	// Every file of a multi-file book must carry the artwork. The skip check is
	// therefore PER FILE.
	//
	// It used to compare only files[0] and return for the whole book on a match,
	// which is all-or-nothing in the wrong direction: a book whose first file
	// already had the cover skipped the embed for every remaining file, so those
	// files stayed permanently artwork-less and no amount of re-running fixed it.
	// Comparing per file both closes that hole and keeps the saving, since a file
	// that already matches is still skipped — and skipping is what matters, as an
	// embed is a full rewrite of the audio file.
	embedded, skipped, skippedProtected, failed := 0, 0, 0, 0
	embedLog := logger.New("metafetch-cover-embed")
	archived := false
	for _, f := range files {
		if newHash != "" {
			if existingData, _, _ := metadata.ExtractCoverArtBytes(f); len(existingData) > 0 {
				if fmt.Sprintf("%x", sha256.Sum256(existingData))[:12] == newHash {
					skipped++
					continue
				}
			}
		}

		// Archive the cover we are about to overwrite, once per book, from the
		// first file we actually touch.
		if !archived {
			mfs.archiveExistingCover(book.ID, f)
			archived = true
		}

		// EmbedCoverArtSafe refuses a protected path (the deps carry no importer;
		// see SetSafeWriteDeps), so a seeding file is skipped, never copied.
		// The embed rewrites the whole file, so it records the new hashes on the
		// file's book_file row; otherwise the next rescan saw a changed hash and
		// treated the file as replaced.
		// tagger looks the row up after resolving a protected-path redirect,
		// so a write sent to a library copy records on the copy's row.
		deps := mfs.safeWriteDeps
		if mfs.db != nil {
			deps.HashStore = mfs.db
		}
		err := tagger.EmbedCoverArtSafe(context.Background(), f, coverPath, deps)
		switch {
		case errors.Is(err, tagger.ErrProtectedPathWrite):
			// The guard refused a protected file (no library copy to write
			// to). Left alone on purpose, so not a failure.
			embedLog.Info("cover art embed skipped protected file %s for book %s: %v",
				logger.SanitizeLogValue(f), logger.SanitizeLogValue(book.ID), err)
			skippedProtected++
		case err != nil:
			embedLog.Warn("cover art embedding failed for file %s (book %s %q, cover %s): %v",
				logger.SanitizeLogValue(f), logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(book.Title),
				logger.SanitizeLogValue(coverPath), err)
			failed++
		default:
			embedded++
		}
	}
	if embedded > 0 || failed > 0 || skippedProtected > 0 {
		embedLog.Info("cover art embed complete for book %s: embedded=%d skipped_unchanged=%d skipped_protected=%d failed=%d files=%d",
			logger.SanitizeLogValue(book.ID), embedded, skipped, skippedProtected, failed, len(files))
	} else if skipped > 0 {
		slog.Debug("cover art already present in every file, nothing to embed",
			"id", book.ID, "files", len(files))
	}
}

// archiveExistingCover extracts the current embedded cover art from an audio
// file and saves it as a timestamped version in covers/history/{bookID}/ so it
// can be restored later. Records a metadata change for changelog tracking.
func (mfs *Service) archiveExistingCover(bookID string, audioFilePath string) {
	data, mimeType, err := metadata.ExtractCoverArtBytes(audioFilePath)
	if err != nil || len(data) == 0 {
		return // no existing cover to archive
	}

	// Determine extension from MIME type
	ext := ".jpg"
	switch {
	case strings.Contains(mimeType, "png"):
		ext = ".png"
	case strings.Contains(mimeType, "webp"):
		ext = ".webp"
	case strings.Contains(mimeType, "gif"):
		ext = ".gif"
	}

	// Hash the cover data for deduplication
	coverHash := fmt.Sprintf("%x", sha256.Sum256(data))

	// Check if we already have this exact image archived (by hash)
	dedupDir := filepath.Join(config.AppConfig.RootDir, "covers", "dedup")
	if err := os.MkdirAll(dedupDir, 0775); err != nil {
		slog.Warn("failed to create cover dedup dir", "error", err)
		return
	}

	dedupPath := filepath.Join(dedupDir, coverHash+ext)
	if _, err := os.Stat(dedupPath); err != nil {
		// New unique image — save to dedup store
		if err := os.WriteFile(dedupPath, data, 0664); err != nil {
			slog.Warn("failed to write dedup cover for", "id", bookID, "error", err)
			return
		}
	}

	// Create a history entry that references the dedup hash instead of storing a copy
	historyDir := filepath.Join(config.AppConfig.RootDir, "covers", "history", bookID)
	if err := os.MkdirAll(historyDir, 0775); err != nil {
		slog.Warn("failed to create cover history dir", "error", err)
		return
	}

	ts := time.Now().Format("20060102-150405")
	// History entry is a symlink to the dedup store to avoid duplicate storage
	archivePath := filepath.Join(historyDir, ts+ext)
	if err := os.Symlink(dedupPath, archivePath); err != nil {
		// Symlink failed (cross-device, Windows, etc.) — fall back to hardlink or copy
		if err := os.Link(dedupPath, archivePath); err != nil {
			// Hardlink also failed — just copy
			if err := os.WriteFile(archivePath, data, 0664); err != nil {
				slog.Warn("failed to archive old cover for", "id", bookID, "error", err)
				return
			}
		}
	}
	slog.Info("archived old cover art (hash)", "path", archivePath, "hash", coverHash[:12])

	// Record in metadata change history so it appears in the changelog
	now := time.Now()
	summaryJSON := jsonEncodeString(fmt.Sprintf("cover_art: archived previous cover to %s", filepath.Base(archivePath)))
	record := &database.MetadataChangeRecord{
		BookID:     bookID,
		Field:      "cover_art",
		NewValue:   &summaryJSON,
		ChangeType: "cover-archive",
		Source:     "system",
		ChangedAt:  now,
	}
	if err := mfs.db.RecordMetadataChange(record); err != nil {
		slog.Warn("failed to record cover archive history for", "id", bookID, "error", err)
	}
	// Dual-write to unified activity log
	if mfs.activityService != nil {
		_ = mfs.activityService.Record(database.ActivityEntry{
			Tier:    "change",
			Type:    "metadata_apply",
			Level:   "info",
			Source:  "background",
			BookID:  bookID,
			Summary: fmt.Sprintf("Archived cover art to %s", filepath.Base(archivePath)),
		})
	}
}

// looksLikeASIN checks if a string looks like an Amazon ASIN (10 alphanumeric chars, typically starts with B0).
func looksLikeASIN(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 10 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// extractASIN finds an ASIN-like pattern (B0 followed by 8 alphanumeric chars) anywhere in the string.
func extractASIN(s string) string {
	s = strings.TrimSpace(s)
	// Split on whitespace and check each token
	for word := range strings.FieldsSeq(s) {
		word = strings.Trim(word, ",.;:!?()[]{}\"'")
		if looksLikeASIN(word) {
			return word
		}
	}
	return ""
}

// metadataCanonicalID extracts the canonical external identifier from a
// MetadataCandidate for use in the metadata_source_hash computation.
// Priority: ASIN > ISBN-13 > ISBN-10 > ISBN. Returns "" if none present.
func metadataCanonicalID(c MetadataCandidate) string {
	if c.ASIN != "" {
		return c.ASIN
	}
	if c.ISBN != "" && len(c.ISBN) == 13 {
		return c.ISBN
	}
	if c.ISBN != "" {
		return c.ISBN
	}
	return ""
}

// CandidateSourceHash is the metadata_source_hash an apply of c records:
// sha256("{source}:{canonical_id}") in hex, the dedup key MATCH-4 elects on.
// "" when c carries no canonical id (no ASIN and no ISBN), in which case the
// apply records none. It lets a caller recover WHICH cached candidate a book
// was applied from by comparing it with the book's MetadataSourceHash.
func CandidateSourceHash(c MetadataCandidate) string {
	id := metadataCanonicalID(c)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(c.Source+":"+id)))
}

// audioFilesInDir returns the audio files found directly inside dir.
// It globs for common audiobook extensions. Returns nil if dir is not a
// directory or contains no matching files.
// virtualBookFiles is the single-file (row-less) book's stand-in for its
// book_file rows: one entry built from book.FilePath, or nil when FilePath is
// empty or names a directory. It carries the book row's recorded size because
// that is the only identity a stranded-temp resume can check against
// (organizer.strandedTempMismatch refuses a size-less entry forever) — 12,525
// prod books have no rows, so dropping the size here parked their renames for
// good.
func virtualBookFiles(id string, book *database.Book) []database.BookFile {
	if book == nil || book.FilePath == "" {
		return nil
	}
	ext := strings.TrimPrefix(filepath.Ext(book.FilePath), ".")
	if ext == "" {
		return nil
	}
	vf := database.BookFile{
		ID:       "virtual-" + id,
		BookID:   id,
		FilePath: book.FilePath,
		Format:   ext,
	}
	if book.FileSize != nil {
		vf.FileSize = *book.FileSize
	}
	return []database.BookFile{vf}
}

// RunApplyPipelineRenameOnly runs only the rename portion of the apply pipeline.
// Used by the "Save to Files" button and the bulk write-back to rename files
// without re-writing tags (tags are written separately).
//
// It takes the apply file work's locks, in the same order (see lockBook): the
// book's own lock; its library copy's, through lockLibraryCopy, which makes a
// protected book's copy under the version-group lock; then the path lock on
// the files it renames. Until 2026-09-12 it took none and made the copy with
// no lock, so it could make a second copy alongside an apply of another
// version, or rename files an apply of the same book was writing. A caller
// must not hold any key from the same lock table around it.
//
// The caller's book is not used: it was read before these locks, and a job
// that held them may have moved the files since. The book is re-read under
// them instead.
func (mfs *Service) RunApplyPipelineRenameOnly(ctx context.Context, id string, _ *database.Book) error {
	releaseBook := mfs.lockBook(id)
	defer releaseBook()
	targetID, releaseCopy, err := mfs.lockLibraryCopy(id, createLibraryCopy, nil)
	if err != nil {
		return err
	}
	defer releaseCopy()
	book, err := mfs.db.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("rename files: load book %s: %w", id, err)
	}
	if book == nil {
		return fmt.Errorf("rename files: book %s not found", id)
	}
	target, err := mfs.fileWorkTarget(book, targetID)
	if err != nil {
		return fmt.Errorf("rename files: %w", err)
	}
	if target == nil {
		return fmt.Errorf("no library copy for protected book %s", id)
	}
	id, book = target.ID, target
	release := mfs.lockPath(book.FilePath)
	defer release()

	bookFiles, err := mfs.db.GetBookFiles(id)
	if err != nil {
		return fmt.Errorf("list book files: %w", err)
	}
	bookFiles = dedupeBookFilesByPath(id, bookFiles)

	// For single-file books with no book files, create a virtual entry from book.FilePath
	if len(bookFiles) == 0 {
		bookFiles = virtualBookFiles(id, book)
	}
	if len(bookFiles) == 0 {
		return nil
	}

	// Plan the rename through the Organizer, so it resolves author/series and
	// expands the naming patterns exactly the way the organize path does. This
	// block used to hand-roll its own FormatVars and read path_format — a
	// separate builder that disagreed with organize by two directory levels and
	// pulled every book back and forth between the two answers.
	entries, err := newPathOrganizer(mfs.db).ComputeTargetPaths(book, bookFiles)
	if err != nil {
		// A broken naming pattern must NOT fall through to a rename: the target
		// would be built from a half-substituted template and would relocate the
		// library somewhere no scan expects.
		return fmt.Errorf("compute target paths for book %s: %w", id, err)
	}
	entries = mfs.dropProtectedRenameEntries(id, entries)
	if len(entries) == 0 {
		return nil
	}

	// Same collision policy as runApplyPipeline: this is the same rename
	// reached from the "Save to Files" button, and an occupied target failed
	// forever here in exactly the same way.
	//
	// The durable-failure skip is deliberately NOT applied on this path. It is
	// user-initiated: someone pressing the button has asked for this book now,
	// and silently doing nothing because a background run recorded a failure
	// would be a button that does nothing with no explanation. The record is
	// still written on failure, and still cleared on success, so the two paths
	// share one state.
	renameResult, renameErr := RenameFiles(entries, applyCollisionPolicy(mfs.db, id))
	// Even when RenameFiles returns an error, entries in renameResult.Succeeded
	// have physically moved on disk — their DB paths MUST still be updated
	// below, or the library loses track of files that did move. The error is
	// returned after the DB sync + empty-dir cleanup.

	// Update book file records with new paths. A DB write that fails here
	// leaves a file that moved on disk recorded at its old path, so every such
	// failure is collected and returned (after the cleanup below), not dropped.
	var dbSyncErrs []error
	bfMap := make(map[string]*database.BookFile, len(bookFiles))
	for i := range bookFiles {
		bfMap[bookFiles[i].ID] = &bookFiles[i]
	}
	for _, entry := range renameResult.Succeeded {
		if strings.HasPrefix(entry.SegmentID, "virtual-") {
			// Virtual entry = single-file book. Update book.FilePath directly to the new file path.
			book.FilePath = entry.TargetPath
			// Keep in-memory virtual BookFile in sync so ITunesPath can be computed below.
			if len(bookFiles) > 0 && bookFiles[0].ID == entry.SegmentID {
				bookFiles[0].FilePath = entry.TargetPath
			}
			if err := mfs.persistRenamedBookPath(ctx, id, book, entry.SourcePath, entry.TargetPath); err != nil {
				dbSyncErrs = append(dbSyncErrs, err)
			} else {
				renameSyncLog.Info("renamed single-file book %s to %s", id, entry.TargetPath)
			}
		} else if bf, ok := bfMap[entry.SegmentID]; ok {
			bf.FilePath = entry.TargetPath
			bf.ITunesPath = ComputeITunesPath(entry.TargetPath)
			if err := mfs.writeMovedBookFile(ctx, id, bf, entry.SourcePath); err != nil {
				dbSyncErrs = append(dbSyncErrs, err)
			}
		}
		// Record path change for each successful rename
		if entry.SourcePath != entry.TargetPath {
			_ = mfs.db.RecordPathChange(&database.BookPathChange{
				BookID:     id,
				OldPath:    entry.SourcePath,
				NewPath:    entry.TargetPath,
				ChangeType: "rename",
			})
			// Dual-write to unified activity log
			if mfs.activityService != nil {
				_ = mfs.activityService.Record(database.ActivityEntry{
					Tier:    "change",
					Type:    "rename",
					Level:   "info",
					Source:  "background",
					BookID:  id,
					Summary: fmt.Sprintf("Moved: %s → %s", filepath.Base(entry.SourcePath), filepath.Base(entry.TargetPath)),
					Details: map[string]any{"old_path": entry.SourcePath, "new_path": entry.TargetPath},
				})
			}
		}
	}

	// Update book file_path for multi-segment books (directory path)
	if len(renameResult.Succeeded) > 0 && !strings.HasPrefix(renameResult.Succeeded[0].SegmentID, "virtual-") {
		newBookPath := filepath.Dir(renameResult.Succeeded[0].TargetPath)
		if newBookPath != book.FilePath {
			if err := mfs.persistRenamedBookPath(ctx, id, book, book.FilePath, newBookPath); err != nil {
				dbSyncErrs = append(dbSyncErrs, err)
			} else {
				renameSyncLog.Info("renamed book files for %s to %s", id, newBookPath)
			}
		}
	}

	// Always ensure itunes_path is set on each BookFile if a mapping exists.
	for i := range bookFiles {
		if bookFiles[i].ITunesPath == "" {
			if itunesPath := ComputeITunesPath(bookFiles[i].FilePath); itunesPath != "" {
				bookFiles[i].ITunesPath = itunesPath
				if !strings.HasPrefix(bookFiles[i].ID, "virtual-") {
					if err := mfs.db.UpdateBookFile(bookFiles[i].ID, &bookFiles[i]); err != nil {
						slog.Warn("failed to update itunes_path for book file", "id", bookFiles[i].ID, "error", err)
					}
				}
			}
		}
	}

	// Clean up empty directories left after rename
	for _, entry := range renameResult.Succeeded {
		oldDir := filepath.Dir(entry.SourcePath)
		if oldDir != filepath.Dir(entry.TargetPath) {
			removeEmptyDirs(oldDir, config.AppConfig.RootDir)
		}
	}

	// A file that moved on disk but whose new path did not reach the database
	// is a failed rename as far as the library is concerned: surface it.
	if len(dbSyncErrs) > 0 {
		if renameErr != nil {
			recordRenameCollisionFailure(mfs.db, id, renameResult, renameErr)
		}
		return fmt.Errorf("rename files: moved on disk but not recorded in the database: %w",
			errors.Join(append(dbSyncErrs, renameErr)...))
	}

	// Now that DB paths for every succeeded rename are persisted, surface the
	// rename failure (skipping dedup/writeback follow-ups for the failed run).
	// An unresolved collision is recorded durably so the BACKGROUND apply stops
	// re-attempting it; this user-initiated path itself is never skipped.
	if renameErr != nil {
		recordRenameCollisionFailure(mfs.db, id, renameResult, renameErr)
		return fmt.Errorf("rename files: %w", renameErr)
	}
	organizer.ClearApplyRenameFailure(mfs.db, id)

	// Trigger dedup check after metadata apply
	if mfs.dedupEngine != nil {
		go func() {
			if _, err := mfs.dedupEngine.CheckBook(context.Background(), id); err != nil {
				slog.Warn("dedup re-check failed for book after metadata apply", "id", logger.SanitizeLogValue(id), "error", logger.SanitizeLogValue(err.Error()))
			}
		}()
	}

	// Enqueue iTunes writeback so location changes from the rename
	// propagate to iTunes. Callers (bulk write-back) also enqueue,
	// the batcher dedupes.
	if mfs.writeBackBatcher != nil {
		mfs.writeBackBatcher.Enqueue(id)
	}

	return nil
}

// truncateActivity shortens s to maxLen runes, appending "..." if truncated.
func truncateActivity(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// bookFileLister and rejectedKeyScanner are the two one-method surfaces the
// free functions in batch.go need. Each took database.Store.
type bookFileLister interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

type rejectedKeyScanner interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
}
