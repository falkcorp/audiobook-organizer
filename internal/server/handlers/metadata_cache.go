// file: internal/server/handlers/metadata_cache.go
// version: 1.28.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-04

// Package handlers contains extracted HTTP handler types for the audiobook
// organizer server. MetadataCacheHandler covers the persistent metadata-cache
// query endpoints (cached candidates list, cache review, batch-apply-cached,
// clear-no-match).

package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// reviewListConcurrency bounds the concurrent cached-candidate reads when
// building the review listing. These are store reads, not network calls, and
// this runs inline on a user-facing request — a small fixed pool, not
// runtime.NumCPU(), so one large listing cannot starve the rest of the server.
const reviewListConcurrency = 8

// defaultReviewPageSize bounds a review-listing request that does not explicitly
// ask for everything. limit=0/absent used to mean "return every row", measured
// at 34.8s and 18.4s in production while a library.scan ran concurrently; a
// caller that forgets to page (or a stray curl) must not pay for the whole cache.
// A caller that genuinely needs the whole set sends all=true.
const defaultReviewPageSize = 200

// Values of `bucket` on GET /metadata/cache/review, and the per-row statuses
// the unreviewable bucket serves. The statuses name WHY a row has nothing to
// review, one per counter in the summary, so a client can filter the list by
// the same cause the chip it clicked counts:
//
//	no_candidates          -> unreviewable_by_cause.no_candidates
//	resolved_no_candidates -> resolved_no_candidates
//	decode_error           -> errors (and unreviewable_by_cause.decode_errors)
//
// Orphaned rows (the book is gone) are never listed: there is no book to show.
const (
	reviewBucketReviewable   = "reviewable"
	reviewBucketUnreviewable = "unreviewable"

	unreviewableStatusNoCandidates         = "no_candidates"
	unreviewableStatusResolvedNoCandidates = "resolved_no_candidates"
	unreviewableStatusDecodeError          = "decode_error"
)

// reviewViewLabel maps the request's view flag onto the bounded
// review_index_request_seconds{view} label.
func reviewViewLabel(indexView bool) string {
	if indexView {
		return metrics.ReviewViewIndex
	}
	return metrics.ReviewViewFull
}

// slowReviewListing is the handler-time threshold past which
// GetCacheReviewResults logs a WARN with enough context to correlate the slow
// request with what else the server was doing.
const slowReviewListing = 5 * time.Second

// MetadataCacheBookStore is the narrow persistence interface required by
// MetadataCacheHandler. It also satisfies metabatch.BookFilesGetter so that
// BuildCandidateBookInfo can be called with the same store value.
type MetadataCacheBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	// GetBooksByIDs fetches many books in one store call, preserving input
	// order. The review listing is served over the whole pending set (the lane
	// sends all=true) and previously did two GetBookByID point reads per entry.
	GetBooksByIDs(ids []string) ([]database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
	// ModifyBook is the column-scoped read-modify-write ClearMetadataNoMatch
	// uses (database.BookMutator.ModifyBook).
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	// GetBookFilesForIDsCore is the batch file read the review listing's
	// loader uses (loadCacheRows): one call per chunk of books instead of a
	// GetBookFiles range scan per book, read once and shared by the legacy
	// filter, the search-title resolver and the row's book info.
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
	// SearchQueryReader is everything metabatch.ResolveCandidateSearchQuery
	// reads to decide whether a stale row is one a refetch would actually
	// search (cacheRowStale): the book's files (also metabatch.BookFilesGetter
	// for BuildCandidateBookInfo), its live authors, the other rows in its
	// folder (metabatch.SkipKindSiblingPart), and the import paths, read from
	// THIS store so a folder that is an import root is never listed.
	metabatch.SearchQueryReader
}

// ActiveOpsLister is the shape of database.Store's ListActiveOperationsV2. It
// is a func, not an interface method on MetadataCacheBookStore, because the
// only consumer is a diagnostic log attribute: the wiring closes over the real
// store instead of widening the handler's persistence interface for it.
type ActiveOpsLister func() ([]database.OperationV2Row, error)

// LibraryScanActive reports whether a "library.scan" op is currently queued or
// running. Diagnostic only: a nil lister or a lookup error is reported as "not
// scanning" rather than aborting or erroring the caller.
//
// No status filter here: ListActiveOperationsV2's contract already restricts
// its rows to queued/running, and re-checking would let this silently diverge
// from the store if that contract ever changed.
func LibraryScanActive(list ActiveOpsLister) bool {
	if list == nil {
		return false
	}
	active, err := list()
	if err != nil {
		return false
	}
	for _, op := range active {
		if op.DefID == "library.scan" {
			return true
		}
	}
	return false
}

// ScanActiveLogAttrs returns the slog key/value pair for the slow-request
// WARN's library_scan_active attribute, or nil when scanActive is nil so the
// attribute is omitted rather than reported as a false "not scanning".
func ScanActiveLogAttrs(scanActive func() bool) []any {
	if scanActive == nil {
		return nil
	}
	return []any{"library_scan_active", scanActive()}
}

// MetadataCacheFetchService is the narrow interface required for the
// metadata cache query and apply operations.
type MetadataCacheFetchService interface {
	ListCachedSummaries(ctx context.Context) ([]metafetch.MetadataCacheSummary, error)
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
	ApplyMetadataCandidate(id string, candidate metafetch.MetadataCandidate, fields []string) (*metafetch.FetchMetadataResponse, error)
	InvalidateCachedCandidates(bookID string) error

	// ApplyMetadataFileIO runs the slow post-apply file work: cover-art
	// embedding, tag writing and (when enabled) renaming. Gated in prod by
	// auto_write_tags_on_apply / auto_rename_on_apply.
	//
	// A non-nil error means the file work did not fully land -- most often the
	// rename failed. It does NOT mean nothing happened: rows for renames that
	// did succeed are already persisted, so callers report the database apply
	// as successful and flag only the file side.
	ApplyMetadataFileIO(id string) error
	// WriteBackMetadataForBook writes the book's current DB metadata into the
	// audio files themselves and returns the number of files written.
	WriteBackMetadataForBook(id string, segmentFilter ...[]string) (int, error)
}

// MetadataCacheWriteBackEnqueuer is an alias for the shared WriteBackEnqueuer;
// kept here so existing call sites continue to compile without change.
type MetadataCacheWriteBackEnqueuer = WriteBackEnqueuer

// MetadataCacheHandler handles the persistent metadata-cache HTTP endpoints.
type MetadataCacheHandler struct {
	store   MetadataCacheBookStore
	svc     MetadataCacheFetchService
	batcher WriteBackEnqueuer // may be nil — iTunes library sync, NOT audio tags
	// fileIOPool schedules the audio-tag / cover-art file work off the request
	// path. May be nil; when it is, BatchApplyFromCache logs at warn rather
	// than skipping silently (see the comment in BatchApplyFromCache).
	fileIOPool FileIOPool
	// ops enqueues the background apply op. When nil, BatchApplyFromCache
	// reports 503 rather than silently falling back to an inline apply: an
	// inline fallback would be a second implementation that only ever ran in
	// tests, so the tested path and the shipped path would diverge.
	ops OpEnqueuer
	// scanActive reports whether a library.scan is queued or running, for the
	// slow-request WARN in GetCacheReviewResults. May be nil; the attribute is
	// then omitted. Injected at wiring time as a closure over the real store so
	// MetadataCacheBookStore does not grow a method only a log line reads.
	scanActive func() bool
	// reviewSnap holds the expensive part of the review listing between
	// requests (metadata_cache_snapshot.go). nil on a handler built as a
	// struct literal; GetCacheReviewResults then loads the rows per request.
	reviewSnap *reviewSnapshotCache
	// reviewBuilder builds the review snapshot, incrementally over the
	// previous one when the store's change logs allow.
	reviewBuilder *reviewSnapshotBuilder
	// booksChangedSince is the store's library change log
	// (database.BooksChangedSince), through which a review request learns
	// which of the snapshot's books to re-read; nil when the store has none,
	// and every request then re-reads every book.
	booksChangedSince changedSinceFunc
}

// OpEnqueuer is the slice of the v2 operations registry this handler needs:
// enqueue a definition with params, get an op id back immediately.
type OpEnqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// NewMetadataCacheHandler constructs a MetadataCacheHandler.
//
// fileIOPool may be nil (tests, or a server built without a pool). Callers must
// pass a nil INTERFACE, not a typed-nil pointer, or the nil guards below become
// false-negatives — see the wiring in wire_handlers.go.
//
// scanActive may be nil; the slow-request WARN then omits library_scan_active.
func NewMetadataCacheHandler(store MetadataCacheBookStore, svc MetadataCacheFetchService, batcher WriteBackEnqueuer, fileIOPool FileIOPool, ops OpEnqueuer, scanActive func() bool) *MetadataCacheHandler {
	h := &MetadataCacheHandler{store: store, svc: svc, batcher: batcher, fileIOPool: fileIOPool, ops: ops, scanActive: scanActive}
	if store != nil && svc != nil {
		b := newReviewSnapshotBuilder(store, svc)
		if g, ok := database.AsCapability[metadataCacheGenerationReader](store); ok {
			b.cacheGen = g.MetadataCacheGeneration
			metadataCacheLog.Info("review snapshot: metadata-cache write counter resolved; cache writes trigger a rebuild")
		} else {
			metadataCacheLog.Info("review snapshot: store has no metadata-cache write counter; only apply/clear marks, idle and age trigger a rebuild")
		}
		if c, ok := database.AsCapability[database.MetadataCacheChangeLogProvider](store); ok {
			b.cacheChangedSince = c.MetadataCacheChangedSince
		}
		if g, tracked := database.LibraryGenerationOf(store); tracked {
			b.bookGen = g.Value
		}
		if c, ok := database.AsCapability[database.BookChangeLogProvider](store); ok {
			b.booksChangedSince = c.BooksChangedSince
			h.booksChangedSince = c.BooksChangedSince
		}
		if b.cacheChangedSince != nil && b.booksChangedSince != nil {
			metadataCacheLog.Info("review snapshot: both change logs resolved; rebuilds are incremental and requests re-read only changed books")
		} else {
			metadataCacheLog.Info("review snapshot: a change log is missing (cache=%v books=%v); every rebuild is full and every request re-reads every book", b.cacheChangedSince != nil, b.booksChangedSince != nil)
		}
		h.reviewBuilder = b
		h.reviewSnap = newReviewSnapshotCache(b.build, b.cacheGen)
	}
	return h
}

// metadataCacheGenerationReader is database.PebbleStore's metadata-cache
// generation, resolved through any store decorators (AsCapability). The
// review snapshot rebuilds when it moves.
type metadataCacheGenerationReader interface {
	MetadataCacheGeneration() uint64
}

// reviewSnapshot returns the review rows: the cached snapshot when the
// handler has one (see reviewSnapshotCache.get), else a fresh full load.
func (h *MetadataCacheHandler) reviewSnapshot(ctx context.Context) (*reviewSnapshot, error) {
	if h.reviewSnap != nil {
		return h.reviewSnap.get(ctx)
	}
	return newReviewSnapshotBuilder(h.store, h.svc).build(ctx, nil)
}

// WarmReviewSnapshot builds the review snapshot now, under ctx (the server's
// lifetime context, which also parents every later background rebuild), so
// the first visit after a restart does not wait for it. A no-op on a handler
// without a snapshot cache.
func (h *MetadataCacheHandler) WarmReviewSnapshot(ctx context.Context) error {
	if h.reviewSnap == nil {
		return nil
	}
	return h.reviewSnap.warm(ctx)
}

// SetBackgroundRunner makes ctx (the server's lifetime context) the parent of
// the review snapshot's background rebuilds, and run the way they start --
// the server passes its tracked background group, so shutdown waits for a
// rebuild rather than closing the store under it.
//
// run reports whether it started fn; it refuses once the server is stopping,
// and the build then fails with errReviewBuildNotStarted instead of hanging.
func (h *MetadataCacheHandler) SetBackgroundRunner(ctx context.Context, run func(fn func()) bool) {
	if h.reviewSnap == nil {
		return
	}
	h.reviewSnap.setBackground(ctx, run)
}

// InvalidateReviewSnapshot marks the review listing's snapshot dirty, so the
// next review request starts a rebuild. Book-level changes (status, apply,
// deletion) never need it -- every request re-reads the books -- but a
// change to what the metadata cache holds does.
func (h *MetadataCacheHandler) InvalidateReviewSnapshot() {
	h.reviewSnap.invalidate()
}

// ListCachedCandidates handles GET /api/v1/audiobooks/metadata/cached.
//
// Optional query params: status=pending|matched, limit, offset.
// limit=0 means "return all rows". GetCacheReviewResults below no longer
// matches this: it caps an unpaged request unless all=true is sent.
func (h *MetadataCacheHandler) ListCachedCandidates(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}

	// limit and offset were accepted-and-ignored until 2026-09-09: the handler
	// never read them, so `?limit=5` returned all 40,485 rows and a 7.35 MB body.
	//
	// The default is 0 = "return all rows" rather than some page size. That was
	// originally justified by "the only caller sends no limit and consumes the
	// whole list", which stopped being true hours later: the sole caller now
	// asks for limit=1 and reads only `total`. The default stays anyway, for a
	// reason that does not depend on any caller — this is a published endpoint
	// whose historical contract is "unpaged", and quietly capping it would turn
	// an existing client's complete list into a silently truncated one with no
	// error to notice. New callers should pass an explicit limit.
	limit := httputil.ParseQueryInt(c, "limit", 0)
	offset := httputil.ParseQueryInt(c, "offset", 0)
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}

	summaries, err := h.svc.ListCachedSummaries(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to list metadata cache", err)
		return
	}

	statusFilter := c.Query("status")
	freshCutoff := time.Now().Add(-database.MetadataCacheTTL)

	// Fetch every book in ONE batch read instead of a GetBookByID per summary,
	// the same way GetCacheReviewResults does.
	//
	// This is required for CORRECTNESS, not just speed, and stays required even
	// though a measurement on 2026-09-09 showed the point reads were a small part
	// of this endpoint's cost. review_status lives on the BOOK, not on the cache
	// summary, so a filtered page cannot be assembled without resolving every
	// candidate row's book first: `status=pending&limit=5` must return five
	// PENDING rows, not five rows of which some happen to be pending. Filtering
	// before paginating means the per-book read now runs over the whole set on
	// every call, which is exactly the shape that must not be an N+1.
	bookIDs := make([]string, 0, len(summaries))
	for _, sum := range summaries {
		bookIDs = append(bookIDs, sum.BookID)
	}
	booksByID := make(map[string]*database.Book, len(summaries))
	if fetched, berr := h.store.GetBooksByIDs(bookIDs); berr == nil {
		for i := range fetched {
			booksByID[fetched[i].ID] = &fetched[i]
		}
	} else {
		slog.Warn("ListCachedCandidates batch book fetch failed; falling back to per-book reads", "err", berr)
	}

	// lookupBook serves from the batch result and falls back to a point read only
	// when the batch missed the row (or the batch call itself failed), so a
	// partial batch degrades in behavior-preserving fashion rather than dropping
	// entries.
	lookupBook := func(id string) *database.Book {
		if b, ok := booksByID[id]; ok {
			return b
		}
		b, err := h.store.GetBookByID(id)
		if err != nil || b == nil {
			return nil
		}
		booksByID[id] = b
		return b
	}

	// Filter first, then paginate. `total` below is the size of the FILTERED set,
	// not of the returned page -- a UI rendering "showing 5 of N" needs N to be
	// what it could page through. It was len(out) before, which was correct only
	// because there was no paging to tell the two apart.
	filtered := make([]gin.H, 0, len(summaries))
	for _, sum := range summaries {
		book := lookupBook(sum.BookID)
		if book == nil {
			// A cache row that outlived its book. Dropped, as before.
			continue
		}
		var reviewStatus string
		if book.MetadataReviewStatus != nil {
			reviewStatus = *book.MetadataReviewStatus
		}
		switch statusFilter {
		case "pending":
			if reviewStatus != "" && reviewStatus != "pending" {
				continue
			}
		case "matched":
			if reviewStatus != "matched" {
				continue
			}
		}
		filtered = append(filtered, gin.H{
			"book_id":         sum.BookID,
			"fetched_at":      sum.FetchedAt,
			"candidate_count": sum.CandidateCount,
			"is_fresh":        sum.FetchedAt.After(freshCutoff),
			"title":           book.Title,
			"review_status":   reviewStatus,
		})
	}

	total := len(filtered)

	// Page the filtered set. The order is the one ListMetadataCacheKeys
	// establishes (FetchedAt descending, book id breaking ties), which is total
	// and stable -- without that tiebreak two rows sharing a timestamp could swap
	// between calls and a paging client would see one twice and miss the other.
	page := filtered
	if offset >= len(page) {
		page = nil
	} else {
		page = page[offset:]
	}
	if limit > 0 && limit < len(page) {
		page = page[:limit]
	}
	if page == nil {
		page = []gin.H{}
	}

	httputil.RespondWithOK(c, gin.H{"entries": page, "total": total})
}

// GetCacheReviewResults handles GET /api/v1/audiobooks/metadata/cache/review.
//
// Returns a paginated list of CandidateResult items sourced from the
// persistent metadata cache.
//
// Query params: limit, offset, all, bucket. A positive limit is honoured as sent. A
// zero or absent limit is capped to defaultReviewPageSize unless all=true, which
// returns every reviewable row. The response reports `truncated` (the rows
// returned are not the whole reviewable set) and the `limit` actually applied
// (0 when all=true), and total_count is always the size of the whole set.
//
// Unlike ListCachedCandidates above, whose unpaged default is kept because
// quietly capping it would turn a client's complete list into a silently
// truncated one, this cap is not silent: the response says it truncated, the
// server logs when the default did it, and the one caller that needs every row
// (useMetadataLane) sends all=true.
//
// bucket=unreviewable swaps what `results` holds: instead of the reviewable
// rows it lists the books the summary counts but the default list drops --
// no stored candidate (pending or already ruled on) and undecodable candidate
// -- so the review rail's chips can show the individual books behind their
// counts (owner request 2026-09-27, "the 11324 with no candidates"). Each row
// carries the book, its review status and its age; its `status` names the
// bucket (see unreviewableStatus*). The summary fields are identical to the
// default response, total_count is the size of the bucket, and limit/offset/all
// page it the same way. Book info comes from the one batched book read, with no
// per-row file read (metabatch.BuildCandidateBookInfoNoFiles). The default
// (absent or bucket=reviewable) response is unchanged.
//
// view=index serves the review page's index: the same summary and every row
// of the bucket, with each candidate's description dropped -- the one large
// field nothing but the visible row reads. The page filters, groups, counts
// and selects over the index exactly as it did over the full list, and
// fetches full rows for the rows it shows with ids=.
//
// ids=a,b,c (comma separated) restricts `results` to those books, in bucket
// order, ignoring limit and offset. Only those books are re-read, so the
// summary fields of an ids= response count only the asked rows: it is a
// detail lookup for the page, not a summary.
//
// Rows come from a snapshot of the per-row work (cache reads, the legacy
// filter, the resolver, file facts, the candidate decode) held between
// requests (metadata_cache_snapshot.go), with every book re-read live. A
// whole-cache load measured 119 s on production; a request served from the
// snapshot does none of it.
func (h *MetadataCacheHandler) GetCacheReviewResults(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	began := time.Now()

	limit := httputil.ParseQueryInt(c, "limit", 0)
	offset := httputil.ParseQueryInt(c, "offset", 0)
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	all := httputil.ParseQueryBool(c, "all", false)
	indexView := c.Query("view") == "index"
	// Handler latency by view, every exit path from here on (the WARN below
	// only fires past slowReviewListing; the histogram sees every request).
	defer func(view string) {
		metrics.ObserveReviewIndexRequest(view, time.Since(began))
	}(reviewViewLabel(indexView))
	var wantIDs map[string]bool
	if raw := strings.TrimSpace(c.Query("ids")); raw != "" {
		wantIDs = map[string]bool{}
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" {
				wantIDs[id] = true
			}
		}
	}
	bucket := c.DefaultQuery("bucket", reviewBucketReviewable)
	if bucket != reviewBucketReviewable && bucket != reviewBucketUnreviewable {
		httputil.RespondWithBadRequest(c, "bucket must be reviewable or unreviewable")
		return
	}
	// defaulted records that the cap below, not the caller, chose the page size,
	// so a truncation it causes can be logged as the caller's missing limit.
	defaulted := false
	if limit == 0 && !all {
		limit = defaultReviewPageSize
		defaulted = true
	}

	// One loader for the summaries, their books (ONE batch read, with a
	// per-book fallback) and every row's cached candidates, shared with
	// StaleCachedBookIDs so the `stale` count below and the refetch-all-stale
	// set are read the same way (metadata_cache_stale.go).
	//
	// The candidate reads cover EVERY row, not just the requested page, and
	// that decides what is reviewable. This ordering is the fix for a real
	// reporting bug. The counts used to be tallied over every row whose BOOK
	// resolved, while `results` additionally dropped any row with no cached
	// candidates or an undecodable one. On production that was 10,952 counted
	// against 5,774 returned, so the review rail advertised "10730 matched"
	// over a list that could never hold more than 5,774 rows, and `errors` was
	// hardcoded 0 so nothing hinted at the ~5,178 missing. A count that
	// includes rows the caller cannot be given is not a summary, it is a lie
	// with a number on it.
	//
	// Doing this for all rows rather than one page also makes `total_count`
	// correct for pagination. It is also why capping the default page size does
	// NOT bound this fan-out: it runs over every row whatever `limit` is. In
	// the real call path the lane sends all=true, so its page is every row
	// anyway; that describes the current caller, and is not an argument that
	// the full fan-out is cheap.
	//
	// Phase timings: snapshot (get, a cold build included), overlay (the
	// changed books' reads), prepare (status, sort, buckets, counts) and
	// encode (the JSON write), logged with the slow-listing WARN below after
	// the response is written, so a slow request says WHICH phase was slow.
	var phase struct{ snapshot, overlay, prepare, encode time.Duration }
	snap, err := h.reviewSnapshot(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "failed to list metadata cache", err)
		return
	}
	phase.snapshot = time.Since(began)
	set, err := overlayLiveBooks(snap, h.store, wantIDs, h.booksChangedSince)
	if err != nil {
		httputil.InternalError(c, "failed to read the review books", err)
		return
	}
	phase.overlay = time.Since(began) - phase.snapshot
	if set.rebuild {
		// The overlay read every book, or more changed ones than it will per
		// request: refresh the snapshot so the next request does not.
		h.reviewSnap.requestRebuild()
	}
	lookupBook := set.book
	// orphaned counts cache rows that outlived their book. loadCacheRows counts
	// it where the row is dropped: that is the only place that still knows WHY
	// the row is going away, and a subtraction at the end cannot tell it apart
	// from a book that simply has no candidates stored.
	orphaned := set.orphaned
	// respond writes the response and then, past slowReviewListing, logs the
	// phase timings. Handler time, not client-observed latency: this turns
	// "sometimes slow" into a line that can be correlated with a concurrent
	// library.scan or an apply op.
	respond := func(summary gin.H, totalReviewable, returned int) {
		phase.prepare = time.Since(began) - phase.snapshot - phase.overlay
		httputil.RespondWithOK(c, summary)
		phase.encode = time.Since(began) - phase.snapshot - phase.overlay - phase.prepare
		if d := time.Since(began); d > slowReviewListing {
			attrs := []any{
				"duration", d.Round(time.Millisecond),
				"threshold", slowReviewListing,
				"snapshot_ms", phase.snapshot.Milliseconds(),
				"overlay_ms", phase.overlay.Milliseconds(),
				"prepare_ms", phase.prepare.Milliseconds(),
				"encode_ms", phase.encode.Milliseconds(),
				"overlay_books_read", len(set.live),
				"rebuild_requested", set.rebuild,
				"total_reviewable", totalReviewable,
				"returned", returned,
				"all", all,
			}
			attrs = append(attrs, ScanActiveLogAttrs(h.scanActive)...)
			slog.Warn("GetCacheReviewResults exceeded slow-request threshold", attrs...)
		}
	}

	type entryWithStatus struct {
		row    snapshotRow
		sum    metafetch.MetadataCacheSummary
		status string // "matched" | "no_match" | "applied"
		// reviewed distinguishes "a human (or the audio-confirm pass) has ruled
		// on this book" from "nobody has looked at it yet". It cannot be derived
		// from status: status defaults to "matched" for an UNREVIEWED book, so
		// "matched" means pending-review, not reviewed. Without this flag a row
		// that has already been ruled on is indistinguishable from a fresh one
		// once its candidates are gone, and it gets filed under "unreviewable"
		// forever.
		reviewed bool
	}

	prepared := make([]entryWithStatus, 0, len(set.rows))
	for _, row := range set.rows {
		book := row.book
		// "matched" is the PENDING-review default, not a verdict — a book nobody
		// has ruled on lands here.
		st := "matched"
		reviewed := false
		if book.MetadataReviewStatus != nil {
			switch *book.MetadataReviewStatus {
			case "no_match":
				st, reviewed = "no_match", true
			case "matched":
				st, reviewed = "applied", true
			case "audio_confirmed":
				// Written by metafetch/service_apply.go when the candidate title
				// matched the book's own transcribed audio. That is a verdict —
				// a stronger one than a human eyeballing a title — but this
				// switch did not list it until 2026-09-08, so those books fell
				// to the "matched" default and were reported as still awaiting
				// review. The doc comment on database.Book.MetadataReviewStatus
				// still described the vocabulary as `null, "no_match",
				// "matched"` and nothing re-checked it when the apply path
				// started writing a fourth value.
				st, reviewed = "applied", true
			}
		}
		prepared = append(prepared, entryWithStatus{row: row, sum: row.sum, status: st, reviewed: reviewed})
	}
	// Stable sort: matched (pending review) first, then no_match, then applied.
	statusRank := map[string]int{"matched": 0, "no_match": 1, "applied": 2}
	sort.SliceStable(prepared, func(i, j int) bool {
		return statusRank[prepared[i].status] < statusRank[prepared[j].status]
	})

	// reviewable is every row this endpoint can actually hand back, in the
	// sorted order established above. Counts and pagination both derive from
	// it, so they cannot disagree with each other or with `results`.
	type reviewableRow struct {
		sum    metafetch.MetadataCacheSummary
		status string
		cand   metafetch.MetadataCandidate
		hash   string
		files  metabatch.BookFileFacts
		// lastChecked is when this book was last searched for, which is >=
		// sum.FetchedAt when the last search came back empty and the older
		// candidates were kept. It drives is_fresh so a row the UI offers a
		// "Refresh" on is one a refresh would actually change.
		lastChecked time.Time
		// stale is cacheRowStale for this row, served as the row's `stale`.
		stale bool
	}
	// ONE clock read for every freshness decision below -- the summary counts and
	// the per-row is_fresh flag. Reading time.Now() twice for one predicate lets
	// a row sitting on the boundary be counted stale by the summary and reported
	// fresh by its own flag: "the chip says 5,771 but I count 5,772 icons", from
	// a race no test can reproduce because both reads land in the same
	// millisecond under test.
	freshCutoff := time.Now().Add(-database.MetadataCacheTTL)

	reviewable := make([]reviewableRow, 0, len(prepared))
	// unreviewableRows is every non-orphaned row the reviewable list drops,
	// with the cause it was counted under. Only collected when the caller asked
	// for the bucket; the counters below are kept either way.
	type unreviewableRow struct {
		sum         metafetch.MetadataCacheSummary
		status      string
		errMsg      string
		lastChecked time.Time
		// stale is cacheRowStale for this row, served as the row's `stale`.
		stale bool
	}
	wantUnreviewable := bucket == reviewBucketUnreviewable
	var unreviewableRows []unreviewableRow
	var decodeErrors int
	// The no-candidate rows split by whether the book has already been ruled on.
	// They used to share one counter, and lumping them together is what put
	// already-resolved books in the "unreviewable" bucket permanently: a book
	// whose candidates were destroyed by an empty refetch kept its verdict but
	// lost its evidence, and nothing downstream could tell it apart from a book
	// nobody has ever fetched for.
	var noCandidatesPending, noCandidatesReviewed int
	// stale is counted across every non-orphaned row, NOT just the reviewable
	// ones. Counting it inside the reviewable loop -- which is what this did
	// until 2026-09-08 -- structurally hid every stale row that had no
	// candidates, and those are exactly the rows a refetch would help. On
	// production the chip read "11 stale" while 2,658 stale zero-candidate rows
	// sat in the unreviewable bucket, understating the backlog 242x.
	var stale int
	for _, p := range prepared {
		// cacheRowStale is the predicate StaleCachedBookIDs applies too, so
		// this count is the size of the set the refetch-all-stale button sends.
		rowStale := cacheRowStale(p.row.loadedCacheRow, freshCutoff)
		if rowStale {
			stale++
		}
		if p.row.candidateCount == 0 {
			// No cached candidate means nothing to review. Not an error.
			st := unreviewableStatusNoCandidates
			if p.reviewed {
				noCandidatesReviewed++
				st = unreviewableStatusResolvedNoCandidates
			} else {
				noCandidatesPending++
			}
			if wantUnreviewable {
				unreviewableRows = append(unreviewableRows, unreviewableRow{
					sum: p.sum, status: st, lastChecked: p.row.lastChecked, stale: rowStale,
				})
			}
			continue
		}
		if p.row.cand == nil {
			// Logged once per snapshot build (buildReviewSnapshot), not per request.
			err := p.row.decodeErr
			decodeErrors++
			if wantUnreviewable {
				unreviewableRows = append(unreviewableRows, unreviewableRow{
					sum:         p.sum,
					status:      unreviewableStatusDecodeError,
					errMsg:      "stored candidate will not decode: " + err.Error(),
					lastChecked: p.row.lastChecked,
					stale:       rowStale,
				})
			}
			continue
		}
		cand := *p.row.cand
		if indexView {
			cand.Description = ""
		}
		reviewable = append(reviewable, reviewableRow{
			sum:         p.sum,
			status:      p.status,
			cand:        cand,
			hash:        p.row.hash,
			files:       p.row.files,
			lastChecked: p.row.lastChecked,
			stale:       rowStale,
		})
	}

	var matched, noMatch, applied int
	for _, r := range reviewable {
		switch r.status {
		case "no_match":
			noMatch++
		case "applied":
			applied++
		default:
			matched++
		}
	}

	summary := gin.H{
		"matched":  matched,
		"no_match": noMatch,
		// Real decode failures, not a hardcoded zero. A row counted here is one
		// the cache holds but nobody can review until it is repaired.
		// Book reads that failed this request (set.readErrors) are errors too:
		// the row could not be served, but it is not orphaned.
		"errors":        decodeErrors + set.readErrors,
		"total_applied": applied,
		// Cache summaries that exist but are not reviewable. Surfaced so the gap
		// between "the cache has 14,306 entries" and "you can review 5,774" is
		// visible instead of being discovered by subtracting two numbers that
		// never agreed.
		//
		// This is the same value `total - len(reviewable)` produced, since every
		// dropped row passes through exactly one of these three counters -- but
		// summed from the causes rather than inferred, so the causes can be
		// reported alongside it. Knowing the number is 8,532 tells an operator
		// nothing about what to DO; knowing 3,354 of it is rows whose book is
		// gone points straight at a reaper, and the rest at a refetch.
		//
		// noCandidatesReviewed is deliberately NOT part of this sum. Those books
		// have a verdict; there is nothing for a reviewer to do with them, so
		// filing them under "unreviewable" reported settled work as a backlog.
		// They are reported separately as `resolved_no_candidates`.
		"unreviewable": orphaned + noCandidatesPending + decodeErrors + set.readErrors,
		// Rows whose last search is past MetadataCacheTTL, counted over every
		// non-orphaned row (reviewable or not), minus books the owner marked
		// "no match" (the candidate fetch never searches those, so no refetch
		// could clear them). Staleness is informational, per the TTL's
		// contract -- but a reviewer applying month-old metadata should be
		// told. This is exactly the set POST batch-fetch-candidates
		// {stale:true} refetches (cacheRowStale / StaleCachedBookIDs).
		"stale": stale,
		"unreviewable_by_cause": gin.H{
			// The book the row points at no longer resolves. Only a cleanup
			// pass fixes these; refetching cannot.
			"orphaned": orphaned,
			// The book is fine, nobody has ruled on it, and the cache holds no
			// candidate for it. A refetch is the fix for these.
			"no_candidates": noCandidatesPending,
			// Stored, but the JSON would not decode. Also counted in `errors`;
			// this repeats it so the three causes sum to `unreviewable`.
			"decode_errors": decodeErrors,
			// The book's live read failed on this request (a store fault, not a
			// missing book). Also counted in `errors`.
			"book_read_errors": set.readErrors,
		},
		// Books already ruled on that have no candidate left to show. Not a
		// backlog and not an error -- reported so the number is visible rather
		// than silently missing from every bucket.
		//
		// Before 2026-09-08 an empty refetch overwrote a book's candidates while
		// leaving its verdict intact, which is how these are produced;
		// metafetch.cacheSearchResponse no longer does that, so this count is
		// now a fixed backlog of historical damage rather than a growing one.
		"resolved_no_candidates": noCandidatesReviewed,
		// The bulk apply fail-safe (applycap, setting bulk_apply_max_items),
		// served so the review page can refuse an oversized "Apply selected"
		// up front instead of splitting it into requests that each fit under
		// the cap -- chunking must never be a way around it.
		"bulk_apply_max_items": applycap.Effective(config.AppConfig.BulkApplyMaxItems),
	}

	if wantUnreviewable {
		start := min(offset, len(unreviewableRows))
		end := len(unreviewableRows)
		if limit > 0 {
			end = min(start+limit, len(unreviewableRows))
		}
		pageRows := unreviewableRows[start:end]
		if wantIDs != nil {
			start, end = 0, len(unreviewableRows)
			pageRows = nil
			for _, u := range unreviewableRows {
				if wantIDs[u.sum.BookID] {
					pageRows = append(pageRows, u)
				}
			}
		}
		rows := make([]metabatch.CandidateResult, 0, len(pageRows))
		for _, u := range pageRows {
			book := lookupBook(u.sum.BookID)
			if book == nil {
				continue
			}
			fetchedAt := u.sum.FetchedAt
			// The same age rule, and the same clock read, as the stale
			// counter above. The rows marked stale here plus the stale
			// reviewable rows add up to `stale` once owner-marked "no match"
			// books are left out (cacheRowStale); is_fresh itself stays a
			// statement about age.
			isFresh := u.lastChecked.After(freshCutoff)
			reviewStatus := ""
			if book.MetadataReviewStatus != nil {
				reviewStatus = *book.MetadataReviewStatus
			}
			rows = append(rows, metabatch.CandidateResult{
				Book:         metabatch.BuildCandidateBookInfoNoFiles(book),
				Status:       u.status,
				Error:        u.errMsg,
				FetchedAt:    &fetchedAt,
				IsFresh:      &isFresh,
				Stale:        &u.stale,
				ReviewStatus: reviewStatus,
			})
		}
		summary["results"] = rows
		summary["bucket"] = reviewBucketUnreviewable
		summary["total_count"] = len(unreviewableRows)
		summary["truncated"] = end-start < len(unreviewableRows)
		summary["limit"] = limit
		respond(summary, len(reviewable), len(rows))
		return
	}

	start := min(offset, len(reviewable))
	end := len(reviewable)
	if limit > 0 {
		end = min(start+limit, len(reviewable))
	}
	page := reviewable[start:end]
	if wantIDs != nil {
		// An id lookup is an answer about those books, not a page: it is never
		// "truncated", and limit/offset do not apply.
		start, end = 0, len(reviewable)
		page = nil
		for _, r := range reviewable {
			if wantIDs[r.sum.BookID] {
				page = append(page, r)
			}
		}
	}
	// truncated follows GET /operations/timeline's convention (operations_v2.go:
	// `matched > len(resp)`): true when this response is not the whole
	// reviewable set -- rows exist before the offset or after the page. It is
	// computed from the slice bounds, not len(results), because the build loop
	// below drops a row whose book vanished mid-request, and that must not make a
	// complete answer read as a truncated one.
	truncated := end-start < len(reviewable)
	if defaulted && truncated {
		slog.Warn("GetCacheReviewResults capped an unpaged request to the default page size; send limit/offset to page, or all=true for every row",
			"limit", limit,
			"offset", offset,
			"returned", end-start,
			"total_reviewable", len(reviewable),
		)
	}

	// BuildCandidateBookInfo runs for the PAGE only. It is the one genuinely
	// per-row-expensive call here, and a paginated caller must not pay for rows
	// it did not ask for.
	results := make([]metabatch.CandidateResult, 0, len(page))
	for i := range page {
		book := lookupBook(page[i].sum.BookID)
		if book == nil {
			continue
		}
		cand := page[i].cand
		// Age travels with the row. MetadataCacheTTL's contract is that stale
		// entries stay readable and the UI flags them -- but this endpoint sent
		// no age at all, so the review surface could not honour it. On the live
		// library that meant 5,771 of 5,774 reviewable rows were past the TTL
		// and every one of them was presented as though freshly fetched.
		fetchedAt := page[i].sum.FetchedAt
		// Dated off lastChecked, not fetchedAt: a book whose providers came back
		// empty this morning is not one a "Refresh" would improve, even though
		// the candidates it still shows are older than the TTL.
		isFresh := page[i].lastChecked.After(freshCutoff)
		results = append(results, metabatch.CandidateResult{
			Book:      metabatch.BuildCandidateBookInfoWithFacts(book, page[i].files),
			Candidate: &cand,
			Status:    page[i].status,
			FetchedAt: &fetchedAt,
			IsFresh:   &isFresh,
			Stale:     &page[i].stale,
			// The same hash the apply recomputes from the same cache row:
			// every review-page apply button echoes it back in its pin.
			// Hashed over the full stored candidate (with its description),
			// so the index view's pin matches what the apply recomputes.
			CandidateHash: page[i].hash,
		})
	}

	summary["results"] = results
	summary["total_count"] = len(reviewable)
	// Whether `results` is the whole reviewable set, and the page size that
	// was applied (0 = all=true, no cap). A caller that sent no limit is
	// capped to defaultReviewPageSize and must be able to tell.
	summary["truncated"] = truncated
	summary["limit"] = limit
	respond(summary, len(reviewable), len(results))
}

// BatchApplyFromCache handles POST /api/v1/audiobooks/metadata/batch-apply-cached.
//
// This is a DISPATCHER. It enqueues the "metadata.batch-apply-cached" op and
// returns 202 with an op id; it applies nothing itself.
//
// It used to apply the whole batch inline. That was already parallel and
// already pushed the file work to a pool, so the problem was never a missing
// worker pool — it was the REQUEST DURATION. A 250-book apply measured 2m0s on
// production. Go's HTTP server does not kill a handler when the client
// disconnects, and ApplyMetadataCandidate takes no context, so the browser
// timed out, the UI reported "session expired, nothing was applied", and the
// server went on applying for another minute. The user was told the opposite of
// what happened.
//
// The per-book logic now lives in applyCachedCandidateForBook
// (internal/server/batch_apply_one.go), which the op calls. There is
// deliberately NO inline fallback here: a fallback would be a second
// implementation reachable only when the registry is absent, so the tested path
// and the shipped path would diverge.
//
// FILE I/O — KEEP IN STEP WITH THE SINGLE-BOOK PATH. The sibling is
// applyAudiobookMetadataImpl in internal/server/handlers/metadata/handler.go.
// The two drifted apart once: the sibling wrote tags and embedded cover art
// while this path only updated the database and enqueued h.batcher — which is
// the *iTunes* library batcher, not the tag writer. Applied metadata never
// reached the files and nothing logged a failure. If you add file-side work to
// either path, add it to both.
func (h *MetadataCacheHandler) BatchApplyFromCache(c *gin.Context) {
	if h.store == nil || h.svc == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	if h.ops == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	var body struct {
		BookIDs []string `json:"book_ids" binding:"required"`
		// WriteBack defaults to TRUE when absent — identical semantics to the
		// single-book path's body.WriteBack.
		WriteBack *bool `json:"write_back"`
		// DryRun defaults to TRUE when absent: a caller that does not say
		// dry_run:false gets the metadata.bulk-apply-preview report (candidate,
		// certainty-gate verdict, field changes, rename) and nothing is
		// applied. The web UI's Apply button sends dry_run:false.
		DryRun *bool `json:"dry_run"`
		// Pins maps book id -> the candidate the reviewer was looking at when
		// they clicked Apply. Every apply button on the review page sends
		// them (owner ruling 2026-09-27): the single-row Apply with origin
		// "row", the bulk buttons with origin "review_bulk", each with the
		// candidate_hash the review list served. An owner-review pin that
		// still matches the top cached candidate is applied as owner-reviewed;
		// any pin that no longer matches is refused as stale_candidate. A bulk
		// button's book the lane had no hash for carries the hashless
		// "review_bulk" marker (metafetch.CandidatePin.IsUnseenOwnerReview),
		// owner-reviewed without a staleness check. A book with no pin, or a
		// pin of another origin, gets the ordinary hard gate. The dry run
		// ignores pins: it never forwards them to the preview op.
		Pins map[string]metafetch.CandidatePin `json:"pins"`
		// Mode is the review page's bulk toggle (owner ruling 2026-09-27):
		// "fill" (the default, also "") or "replace". Replace makes a book
		// carrying a "review_bulk" pin overwrite filled fields; it changes
		// nothing for a row pin (which already overwrites) or for a book
		// without an owner-review pin (still fill-only and fully gated). Any
		// other value is a 400. The dry run ignores it, like pins.
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	mode, modeOK := metafetch.NormalizeBulkApplyMode(body.Mode)
	if !modeOK {
		httputil.RespondWithBadRequest(c, `invalid mode: want "fill" or "replace"`)
		return
	}

	shouldWriteBack := body.WriteBack == nil || *body.WriteBack

	if body.DryRun == nil || *body.DryRun {
		// No apply cap: a preview writes nothing, and "preview everything that
		// would be applied" is the point.
		opID, err := h.ops.EnqueueOp(c.Request.Context(), "metadata.bulk-apply-preview", map[string]any{
			"book_ids":   body.BookIDs,
			"write_back": shouldWriteBack,
			"source":     "cache",
		})
		if err != nil {
			httputil.InternalError(c, "failed to enqueue metadata apply preview", err)
			return
		}
		h.InvalidateReviewSnapshot()
		c.JSON(http.StatusAccepted, gin.H{
			"data": gin.H{
				"op_id":       opID,
				"dry_run":     true,
				"requested":   len(body.BookIDs),
				"write_back":  shouldWriteBack,
				"results_url": "/api/v1/metadata/bulk-apply-preview/" + opID,
				"note":        "dry run: nothing was applied. Send dry_run:false to apply.",
			},
		})
		return
	}

	// Fail-safe cap (internal/applycap): refuse before enqueueing so the caller
	// gets a 422 now instead of an op that fails a moment later. The op's Run
	// re-checks, because it can be reached without this handler.
	if ex := applycap.Refuse("batch-apply-cached", len(body.BookIDs), config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
		return
	}

	params := map[string]any{
		"book_ids":   body.BookIDs,
		"write_back": shouldWriteBack,
	}
	if len(body.Pins) > 0 {
		params["pins"] = body.Pins
	}
	// Only replace is forwarded: fill is the op's default, and leaving it out
	// keeps a fill request byte-identical to one queued before the toggle
	// existed, so the two still merge.
	if mode == metafetch.BulkApplyModeReplace {
		params["mode"] = mode
	}
	opID, err := h.ops.EnqueueOp(c.Request.Context(), "metadata.batch-apply-cached", params)
	if err != nil {
		httputil.InternalError(c, "failed to enqueue metadata apply", err)
		return
	}

	// 202 with an op id, NOT a completed result. The response no longer carries
	// applied_ids/skipped because nothing has been applied yet — the caller polls
	// the op and then re-reads the review list, which is the only description of
	// what actually happened that cannot go stale.
	h.InvalidateReviewSnapshot()
	c.JSON(http.StatusAccepted, gin.H{
		"data": gin.H{
			"op_id":      opID,
			"requested":  len(body.BookIDs),
			"write_back": shouldWriteBack,
		},
	})
}

// ClearMetadataNoMatch handles POST /api/v1/audiobooks/:id/clear-no-match.
//
// Clears a book's MetadataReviewStatus back to null so it re-surfaces in the
// Review dialog. Does not create a rejection record.
func (h *MetadataCacheHandler) ClearMetadataNoMatch(c *gin.Context) {
	id := c.Param("id")
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	// ModifyBook, not GetBookByID -> UpdateBook(book): the whole-row write
	// reverted any column another writer committed between the read and the
	// write. MetadataReviewStatus is the only column this endpoint owns; a
	// book that already has no status is not written at all.
	book, err := h.store.ModifyBook(id, func(b *database.Book) error {
		if b.MetadataReviewStatus == nil {
			return database.ErrSkipBookWrite
		}
		b.MetadataReviewStatus = nil
		return nil
	})
	if err != nil {
		httputil.InternalError(c, "failed to clear review status", err)
		return
	}
	if book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}
	h.InvalidateReviewSnapshot()
	httputil.RespondWithOK(c, gin.H{"message": "Review status cleared"})
}
