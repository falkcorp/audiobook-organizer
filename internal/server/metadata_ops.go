// file: internal/server/metadata_ops.go
// version: 1.13.0
// guid: fba55738-5898-4950-8e79-3ee008ad0c70
// last-edited: 2026-09-02
//
// Async-operation machinery for the metadata domain, relocated verbatim from
// metadata_handlers.go (ADR-003 Phase 4) when the 19 metadata HTTP handlers
// moved into internal/server/handlers/metadata. This code is NOT HTTP handlers:
// it stays in package server on the *Server receiver because it is referenced by
// 15+ server-resident files and must keep its exact signatures so every existing
// caller compiles unchanged.
//
//   - registryProgressAdapter (+ UpdateProgress/Log/IsCanceled) — used by every
//     *_ops.go to bridge registry.Reporter → operations.ProgressReporter.
//   - runBulkMetadataFetchAll / runBulkMetadataFetchForBookIDs — the resumable
//     full-library / by-ID metadata fetch cores (the v2 op Run dispatches to them).
//   - runBulkWriteBack — used by duplicates_ops.go / library_writeback_op.go /
//     server_maintenance_deps.go.
//   - runIsbnEnrichment / runMetadataRefreshScan — used by server_maintenance_deps.go.
//   - resolveFilterToBookIDs — used by metadata_batch_candidates.go.
//   - RegisterBulkMetadataFetchOp + init() — register the v2 OperationDef.
//   - bulkMetadataFetchV2Params alias.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	ulid "github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"
)

// perProviderFetchCap bounds the number of concurrent live search calls issued
// to any single metadata provider during a bulk fetch. It is a FIXED internal
// constant (INIT-3-T3, reviewed): the ProtectedSource circuit breaker and the
// per-provider limiters (e.g. Hardcover's 60-rpm limiter) sit beneath it, so no
// per-deployment tuning is needed. Deliberately NOT config.
const perProviderFetchCap = 2

// bulkFetchContinuationMargin is how much of the operation's timeout budget is
// reserved for winding down and enqueueing a successor. It must comfortably
// exceed the slowest single book (a full source chain of timing-out providers),
// or the run is killed mid-wind-down and the successor is never queued.
const bulkFetchContinuationMargin = 10 * time.Minute

// nextContinuationParams builds the successor's params for a fetch chain.
//
// Extracted so the property that keeps a chain alive is testable on the real
// code path rather than restated in a test: the successor MUST carry the
// predecessor's run key (it is the ledger key -- a fresh one resumes nothing)
// and MUST differ from the predecessor's params in at least one serialized
// byte, because EnqueueOp returns the existing op id for byte-identical params
// while one is active, silently dropping the successor.
func nextContinuationParams(p handlers.BulkMetadataFetchV2Params, runKey string) handlers.BulkMetadataFetchV2Params {
	next := p
	next.RunKey = runKey
	next.Continuation = p.Continuation + 1
	return next
}

// maxBulkFetchContinuations bounds a chain so a bug that always reports work
// remaining cannot queue links forever. Exceeding it fails loudly rather than
// silently stopping, because a chain that ends quietly is indistinguishable
// from one that finished.
const maxBulkFetchContinuations = 64

// defaultBulkFetchWorkers is the fallback outer-loop pool size when
// config.AppConfig.MetadataScoring.BulkFetchWorkers is unset (<=0). Network-bound,
// deliberately smaller than NumCPU per the CLAUDE.md concurrency mandate.
const defaultBulkFetchWorkers = 4

// bulkFetchWorkers resolves the configured outer-loop worker count, falling back
// to defaultBulkFetchWorkers when unset. Kept trivially greppable so TASK-02 can
// promote the value cleanly.
func bulkFetchWorkers() int {
	if w := config.AppConfig.MetadataScoring.BulkFetchWorkers; w > 0 {
		return w
	}
	return defaultBulkFetchWorkers
}

// defaultWriteBackWorkers is the fallback pool size for the write-back paths
// (runBulkWriteBack, metadata.batch-save) when
// config.AppConfig.MetadataScoring.WriteBackWorkers is unset (<=0). These
// workers are disk/TagLib-bound rather than provider-bound, so the ceiling is
// the library filesystem, not a remote rate limit. 4 replaces the previous
// hardcoded 2.
const defaultWriteBackWorkers = 4

// maxWriteBackWorkers prevents a malformed or overly optimistic configuration
// from saturating the shared filesystem/TagLib resource. The process-wide gate
// below applies this same ceiling across concurrently running operations.
const maxWriteBackWorkers = 8

// writeBackWorkers resolves the configured write-back pool size, falling back to
// defaultWriteBackWorkers when unset. The `> 0` guard is load-bearing, not
// decorative: an unmarshalled config that never set the key yields 0, and a
// zero-sized pool would consume the job channel with nobody draining it.
func writeBackWorkers() int {
	if w := config.AppConfig.MetadataScoring.WriteBackWorkers; w > 0 {
		if w > maxWriteBackWorkers {
			return maxWriteBackWorkers
		}
		return w
	}
	return defaultWriteBackWorkers
}

// providerSemaphore bounds in-flight live search calls per source name. The map
// is built ONCE before dispatch and is read-only thereafter, so concurrent
// workers only ever touch a per-name buffered channel — never the map itself.
type providerSemaphore struct{ byName map[string]chan struct{} }

// newProviderSemaphore builds a per-source-name semaphore (capacity cap per name)
// from a source chain. Duplicate names share one channel.
func newProviderSemaphore(chain []metadata.MetadataSource, cap int) *providerSemaphore {
	m := make(map[string]chan struct{}, len(chain))
	for _, src := range chain {
		n := src.Name()
		if _, ok := m[n]; !ok {
			m[n] = make(chan struct{}, cap)
		}
	}
	return &providerSemaphore{byName: m}
}

// acquire blocks until a slot for name is free or ctx is done. An unknown name
// (no channel) is treated as unbounded and acquire is a no-op — release then
// matches. Returns ctx.Err() when the context is canceled while waiting.
func (p *providerSemaphore) acquire(ctx context.Context, name string) error {
	if p == nil {
		return nil
	}
	ch := p.byName[name]
	if ch == nil {
		return nil
	}
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns a slot previously taken by acquire. Paired with acquire via
// defer, so the channel always holds a token to drain; a no-op for unknown names.
func (p *providerSemaphore) release(name string) {
	if p == nil {
		return
	}
	if ch := p.byName[name]; ch != nil {
		<-ch
	}
}

// Ledger statuses recorded on OperationResult rows by the bulk-fetch paths.
// They are the resume ledger's vocabulary, so a later pass can select exactly
// the books worth retrying rather than re-walking the whole library.
const (
	fetchStatusCached          = "cached"
	fetchStatusNotFound        = "not_found"
	fetchStatusFetchError      = "fetch_error"
	fetchStatusSkippedFragment = "skipped_fragment"
)

// chainOutcome is the result of walking the metadata source chain for one book.
type chainOutcome struct {
	results    []metadata.BookMetadata
	sourceName string
	cacheHit   bool

	// err is the last live-call error seen while walking the chain, and
	// errSource the provider that produced it.
	//
	// Capturing this is what makes a THROTTLED (or broken, or circuit-broken)
	// provider distinguishable from a book that genuinely is not in any
	// catalog. Both end the walk with zero results, so collapsing them into
	// "not_found" -- which is what this code did until 2026-09-02 -- records a
	// false miss for every book that was merely rate-limited. The practical
	// cost is that "fetch the ones we are missing" becomes untrustworthy and
	// the only safe recovery is a full re-scan of the library.
	err       error
	errSource string
}

// status maps a completed walk onto the ledger status. Order matters: results
// win over an error (an earlier provider erroring does not invalidate a later
// provider's hit), and an error beats "not found".
func (o chainOutcome) status() string {
	if len(o.results) > 0 && o.sourceName != "" {
		return fetchStatusCached
	}
	if o.err != nil {
		return fetchStatusFetchError
	}
	return fetchStatusNotFound
}

// walkSourceChain runs the priority-ordered source chain for one book and stops
// at the first source that yields results. Cache is consulted per source before
// any live call. It returns a non-nil error ONLY for context cancellation; a
// provider failure is reported inside the outcome so a single bad book (or a
// single throttled provider) never fails the whole operation.
//
// Both bulk-fetch entry points call this. They previously carried near-identical
// private copies of the walk, and the copies had ALREADY DRIFTED: only the
// all-books copy retried with the untrimmed title when stripChapterFromTitle had
// changed it. Unifying them fixes that divergence in passing -- the by-IDs path
// gains the retry it was silently missing.
func walkSourceChain(
	ctx context.Context,
	store database.RawKVStore,
	sourceChain []metadata.MetadataSource,
	sem *providerSemaphore,
	bookID, bookTitle, author string,
	maxAge time.Duration,
) (chainOutcome, error) {
	var out chainOutcome
	searchTitle := stripChapterFromTitle(bookTitle)

	for _, src := range sourceChain {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		name := src.Name()

		if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, bookID, name, maxAge); cerr == nil && cached != nil {
			var cr []metadata.BookMetadata
			if jerr := json.Unmarshal(cached.Results, &cr); jerr == nil && len(cr) > 0 {
				out.results, out.sourceName, out.cacheHit = cr, name, true
				return out, nil
			}
		}

		// Live calls only: bound concurrency per provider. A ctx cancel while
		// waiting on the semaphore aborts the book with ctx.Err().
		if err := sem.acquire(ctx, name); err != nil {
			return out, err
		}
		hit := func() bool {
			defer sem.release(name)

			// Query ladder for this source, most specific first. The untrimmed
			// retries are appended only when trimming actually changed the
			// title, so an unchanged title is not searched twice.
			attempts := make([]func() ([]metadata.BookMetadata, error), 0, 4)
			add := func(title string) {
				if author != "" {
					attempts = append(attempts, func() ([]metadata.BookMetadata, error) {
						return src.SearchByTitleAndAuthor(ctx, title, author)
					})
				}
				attempts = append(attempts, func() ([]metadata.BookMetadata, error) {
					return src.SearchByTitle(ctx, title)
				})
			}
			add(searchTitle)
			if searchTitle != bookTitle {
				add(bookTitle)
			}

			for _, attempt := range attempts {
				res, err := attempt()
				if err != nil {
					// Remember it and keep trying: a later query or a later
					// source may still succeed, but if nothing does, this is
					// the difference between "not in the catalog" and "we
					// never got a usable answer".
					out.err, out.errSource = err, name
					continue
				}
				if len(res) > 0 {
					out.results, out.sourceName = res, name
					return true
				}
			}
			return false
		}()
		if hit {
			return out, nil
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

// runBookFetchPool runs processOne over indices [0,n) on a bounded errgroup pool
// (the CLAUDE.md-sanctioned worker pool — errgroup.Group + SetLimit). Dispatch
// stops promptly when ctx is canceled, and the first non-nil processOne error is
// returned. Per-book fetch errors must be handled INSIDE processOne (record a
// result row, return nil) so a single book never fails the whole op; processOne
// should return an error only for genuine context cancellation.
func runBookFetchPool(ctx context.Context, workers, n int, processOne func(context.Context, int) error) error {
	if workers <= 0 {
		workers = defaultBulkFetchWorkers
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i := range n {
		if gctx.Err() != nil {
			break // stop dispatching promptly on cancellation
		}
		i := i
		g.Go(func() error { return processOne(gctx, i) })
	}
	return g.Wait()
}

// runBulkMetadataFetchAll is the resumable core of the full-library metadata
// fetch. It ONLY fetches and caches — it never writes to book records.
// Results land in PutCachedMetadataFetch so the per-book review UI can show
// them immediately when the user clicks "apply". Idempotent: books with an
// existing OperationResult row are skipped on resume.
func (s *Server) runBulkMetadataFetchAll(
	ctx context.Context,
	opID string,
	params operations.BulkMetadataFetchParams,
	store bulkMetadataFetchStore,
	progress operations.ProgressReporter,
) (incomplete bool, err error) {
	// Create operation context for structured logging
	op := &logging.OpContext{
		ID:     opID,
		Type:   "metadata-fetch",
		Status: "pending",
	}
	ctx = logging.WithOp(ctx, op)

	// Total unknown until books load; use placeholder (0/1) to avoid 0/0.
	_ = progress.UpdateProgress(0, 1, "loading books (0/1 0.00%)")

	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		op.SetStatus("failed")
		logging.Error(ctx, "failed to load all books", "err", err)
		return false, fmt.Errorf("GetAllBooksCore: %w", err)
	}

	maxAge := time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour

	existingResults, _ := store.GetOperationResults(opID)
	done := make(map[string]bool, len(existingResults))
	for _, r := range existingResults {
		done[r.BookID] = true
	}

	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return false, fmt.Errorf("GetAllAuthors: %w", err)
	}
	authorByID := make(map[int]string, len(allAuthors))
	for _, a := range allAuthors {
		authorByID[a.ID] = a.Name
	}

	type bookWork struct {
		book       database.BookCore
		authorName string
	}
	var work []bookWork
	for i := range allBooks {
		b := &allBooks[i]
		if done[b.ID] || strings.TrimSpace(b.Title) == "" {
			continue
		}
		// skip_cached: skip books that already have a valid (non-expired) cache entry
		// from any source so we only hit the API for books with no cached data.
		if params.SkipCached {
			hasFreshCache := false
			for _, src := range s.metadataFetchService.BuildSourceChain() {
				if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, b.ID, src.Name(), maxAge); cerr == nil && cached != nil {
					hasFreshCache = true
					break
				}
			}
			if hasFreshCache {
				continue
			}
		}
		author := ""
		if b.AuthorID != nil {
			author = authorByID[*b.AuthorID]
		}
		work = append(work, bookWork{book: *b, authorName: author})
	}

	totalBooks := len(existingResults) + len(work)
	alreadyDone := len(existingResults)
	logging.Info(ctx, "bulk-metadata-fetch books total, already cached, to fetch", "totalBooks", totalBooks, "alreadyDone", alreadyDone, "work_count", len(work))

	// Track affected books in operation context
	for i := range work {
		op.AddEntity("books", work[i].book.ID)
	}
	_ = progress.UpdateProgress(alreadyDone, totalBooks,
		fmt.Sprintf("resuming: %d/%d already cached", alreadyDone, totalBooks))

	if len(work) == 0 {
		_ = progress.UpdateProgress(totalBooks, totalBooks, "all books already cached")
		return false, nil
	}

	sourceChain := s.metadataFetchService.BuildSourceChain()
	if len(sourceChain) == 0 {
		sourceChain = []metadata.MetadataSource{metadata.NewAudibleClient()}
	}
	// Move Audible to front of chain when preferred.
	if params.PreferAudible {
		audible := metadata.NewAudibleClient()
		var rest []metadata.MetadataSource
		for _, src := range sourceChain {
			if src.Name() != audible.Name() {
				rest = append(rest, src)
			}
		}
		sourceChain = append([]metadata.MetadataSource{audible}, rest...)
	}

	// Counters shared across the worker pool: completed (running total, seeded
	// with the already-cached count) plus found/notFound as atomics. The `done`
	// resume map above is fully built before dispatch and read-only inside workers.
	// Stop accepting new books shortly BEFORE the registry's def.Timeout and
	// hand the remainder to a queued successor.
	//
	// Hitting the wall is not a survivable state for this op: worker.go maps
	// context.DeadlineExceeded to the terminal status "canceled", which
	// ListResumableOperationsV2 excludes -- so a run that times out is dead and
	// every book it had left has no route back. Stopping early is what turns
	// "the 6h run died at 95%" into "the 6h run handed 5% to the next link".
	//
	// A book skipped here writes NO ledger row and bumps NO counter, so the
	// successor sees it as outstanding and picks it up.
	var stoppedEarly atomic.Bool
	deadline, hasDeadline := ctx.Deadline()

	completed := int64(alreadyDone)
	// errored counts books whose providers failed (throttled, broken, or
	// circuit-open) as distinct from books genuinely absent from every
	// catalog. Folding the two together is what made a rate-limited run
	// indistinguishable from a complete one.
	var found, notFound, errored atomic.Int64

	// Per-provider semaphore (fixed cap 2) shared by all workers so N pool workers
	// can never stampede a single provider. Built from the read-only sourceChain.
	sem := newProviderSemaphore(sourceChain, perProviderFetchCap)

	// progressMu serializes ProgressReporter calls — the reporter is not assumed
	// concurrency-safe (see runBulkWriteBack for the same precaution).
	var progressMu sync.Mutex
	reportProgress := func(current, total int, message string) {
		progressMu.Lock()
		_ = progress.UpdateProgress(current, total, message)
		progressMu.Unlock()
	}

	// processOne handles a single book. Its body is the former serial loop body,
	// unchanged except: counters use atomics, the live source calls are wrapped in
	// the per-provider semaphore (cache reads / result writes stay outside), and it
	// returns an error ONLY for genuine context cancellation. A per-book fetch
	// error records a not_found row and returns nil so it never fails the op.
	processOne := func(gctx context.Context, i int) error {
		if gctx.Err() != nil {
			return gctx.Err()
		}
		if hasDeadline && time.Until(deadline) < bulkFetchContinuationMargin {
			stoppedEarly.Store(true)
			return nil
		}
		w := work[i]
		bookID := w.book.ID
		currentAuthor := w.authorName

		// Skip obvious chapter fragments of shattered audiobooks (e.g. a book
		// titled "06 Chapter 6"). Searching the catalog for these confidently
		// matches a random entry, so we record a skipped status and never hit
		// the API or cache a bogus result.
		if metadata.IsLikelyChapterFragment(w.book.Title) {
			_ = store.CreateOperationResult(&database.OperationResult{
				OperationID: opID,
				BookID:      bookID,
				ResultJSON:  `{"status":"skipped_fragment","source":""}`,
				Status:      fetchStatusSkippedFragment,
			})
			notFound.Add(1)
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 || int(n) == totalBooks {
				reportProgress(int(n), totalBooks,
					fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d errors:%d", n, totalBooks, found.Load(), notFound.Load(), errored.Load()))
			}
			return nil
		}

		out, werr := walkSourceChain(gctx, store, sourceChain, sem, bookID, w.book.Title, currentAuthor, maxAge)
		if werr != nil {
			return werr
		}
		resultStatus := out.status()
		sourceName := out.sourceName
		cacheHit := out.cacheHit
		switch resultStatus {
		case fetchStatusCached:
			if !cacheHit {
				if blob, merr := json.Marshal(out.results); merr == nil {
					_ = database.PutCachedMetadataFetch(store, bookID, sourceName, blob, 0)
				}
			}
			found.Add(1)
		case fetchStatusFetchError:
			// Deliberately NOT counted as not_found. Nothing was cached for this
			// book, so a later "fetch only what is missing" pass will retry it --
			// but only if the ledger says the provider failed rather than that the
			// book is absent from every catalog.
			errored.Add(1)
			logging.Warn(gctx, "bulk-metadata-fetch: provider error; book left retryable",
				"book_id", bookID, "provider", out.errSource, "error", out.err)
		default:
			notFound.Add(1)
		}

		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: opID,
			BookID:      bookID,
			ResultJSON:  fmt.Sprintf(`{"status":%q,"source":%q}`, resultStatus, sourceName),
			Status:      resultStatus,
		})

		n := atomic.AddInt64(&completed, 1)
		if n%50 == 0 || int(n) == totalBooks {
			reportProgress(int(n), totalBooks,
				fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d errors:%d", n, totalBooks, found.Load(), notFound.Load(), errored.Load()))
		}

		// Rate-limit live API calls; cache hits are instant so skip the delay.
		// The condition covers a FAILED live call too (sourceName is empty then):
		// gating on success alone meant the pause was skipped exactly when a
		// provider was throttling us, which is when backing off matters most.
		if !cacheHit && (sourceName != "" || out.err != nil) && i < len(work)-1 {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		return nil
	}

	if err := runBookFetchPool(ctx, bulkFetchWorkers(), len(work), processOne); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	finalCount := atomic.LoadInt64(&completed)
	_ = progress.UpdateProgress(int(finalCount), totalBooks,
		fmt.Sprintf("complete — cached:%d not_found:%d errors:%d", found.Load(), notFound.Load(), errored.Load()))
	op.SetStatus("success")
	logging.Info(ctx, "bulk-metadata-fetch complete", "finalCount", finalCount, "found", found.Load(), "notFound", notFound.Load(),
		"errors", errored.Load(), "stopped_early", stoppedEarly.Load())
	return stoppedEarly.Load(), nil
}

// registryProgressAdapter bridges registry.Reporter → operations.ProgressReporter
// so runBulkMetadataFetchAll can be called from a v2 op Run function without changes.
//
// TODO(ADR-003 Phase 2): registryProgressAdapter cannot move to internal/server/handlers
// because it has methods and is used across 20+ *_ops.go files in internal/server.
// Extract it to internal/operations/registry or a dedicated adapter package in Phase 2.
type registryProgressAdapter struct{ r opsregistry.Reporter }

func (a registryProgressAdapter) UpdateProgress(current, total int, message string) error {
	return a.r.UpdateProgress(current, total, message)
}
func (a registryProgressAdapter) Log(level, message string, details *string) error {
	l := slog.LevelInfo
	switch level {
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	case "debug":
		l = slog.LevelDebug
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	return a.r.Log(l, message, attrs...)
}
func (a registryProgressAdapter) IsCanceled() bool { return a.r.IsCanceled() }

// bulkMetadataFetchV2Params aliases the canonical type from internal/server/handlers.
type bulkMetadataFetchV2Params = handlers.BulkMetadataFetchV2Params

// resolveFilterToBookIDs translates a FilterSpec into a concrete list of primary-
// version book IDs.  IsPrimaryVersion=true and quarantine exclusion are always
// applied.  If f.OnlyUnmatched is set, books that already have a "matched"
// candidate in the most-recent metadata_candidate_fetch result are removed.
// If f.OnlyParsedTranscription is set, books whose Whisper intro produced no
// parsed title (TranscribedTitle empty) are removed — this is safe against the
// memdb projection because stripBookForMemdb does not clear TranscribedTitle.
// Per-user FieldFilters are silently dropped (no user context in background ops).
func (s *Server) resolveFilterToBookIDs(ctx context.Context, f operations.FilterSpec) ([]string, error) {
	trueVal := true
	filters := ListFilters{
		IsPrimaryVersion: &trueVal,
		LibraryState:     f.LibraryState,
		Tag:              f.Tag,
		Tags:             f.Tags,
	}
	for _, ff := range f.FieldFilters {
		if IsPerUserField(ff.Field) {
			continue
		}
		// Refuse an empty value rather than resolving it. strings.Contains(x, "")
		// is always true, so an empty-valued filter matches EVERY book — and this
		// function resolves to concrete book IDs for a BACKGROUND OPERATION with
		// a limit of 100,000. Silently widening a targeted op to the whole
		// library is exactly the base64 op-params defect (#2309) one level down,
		// and unlike the list endpoint there is no HTTP boundary here to catch
		// it: params arrive already deserialized from the operation queue.
		if ff.Value == "" {
			return nil, fmt.Errorf("filter on %q has an empty value, which would match every "+
				"book and target the entire library; refusing to resolve it", ff.Field)
		}
		filters.FieldFilters = append(filters.FieldFilters, FieldFilter{
			Field:   ff.Field,
			Value:   ff.Value,
			Negated: ff.Negated,
		})
	}
	var authorID, seriesID *int
	if f.AuthorID != nil {
		v := int(*f.AuthorID)
		authorID = &v
	}
	if f.SeriesID != nil {
		v := int(*f.SeriesID)
		seriesID = &v
	}
	books, err := s.audiobookService.GetAudiobooks(ctx, 100000, 0, f.Search, authorID, seriesID, filters)
	if err != nil {
		return nil, fmt.Errorf("resolve filter: %w", err)
	}
	ids := make([]string, 0, len(books))
	for _, b := range books {
		if b.QuarantinedAt != nil {
			continue
		}
		if f.OnlyParsedTranscription &&
			(b.TranscribedTitle == nil || strings.TrimSpace(*b.TranscribedTitle) == "") {
			continue
		}
		ids = append(ids, b.ID)
	}
	if f.OnlyUnmatched {
		matched := metabatch.LatestMatchedBookIDs(s.Ops())
		filtered := ids[:0]
		for _, id := range ids {
			if !matched[id] {
				filtered = append(filtered, id)
			}
		}
		ids = filtered
	}
	return ids, nil
}

// RegisterBulkMetadataFetchOp registers the "library.bulk-metadata-fetch" v2
// OperationDef so that POST /api/v1/operations/v2 with def_id "bulk_metadata_fetch"
// shows in the bell, is resumable, and can be cancelled.
func (s *Server) RegisterBulkMetadataFetchOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.bulk-metadata-fetch",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Bulk Metadata Fetch",
		Description:     "Fetch and cache external metadata for a set of audiobooks. Nothing is written to book records — results appear in the per-book review UI.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		ResumePolicy:    opsregistry.ResumeRestart,
		ConcurrencyKey:  "library.bulk-metadata-fetch",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapNetworkGeneric, opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p bulkMetadataFetchV2Params
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("bulk_metadata_fetch: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("bulk_metadata_fetch: database not initialized")
			}

			// The ledger key for this fetch CHAIN. Every OperationResult row is
			// written under it and every continuation carries it forward, so a
			// successor reads the same ledger its predecessor wrote.
			//
			// This was ulid.Make() until 2026-09-02 -- random on every run,
			// directly beneath a comment asserting it was deterministic "so
			// OperationResult rows survive restarts". They never did:
			// GetOperationResults(opID) always missed, the `done` map was always
			// empty, and this operation has never once resumed anything.
			runKey := strings.TrimSpace(p.RunKey)
			if runKey == "" {
				runKey = ulid.Make().String()
			}
			opID := runKey

			if p.Continuation > maxBulkFetchContinuations {
				return fmt.Errorf("bulk_metadata_fetch: chain %s exceeded %d continuations; refusing to queue another",
					runKey, maxBulkFetchContinuations)
			}

			fetchParams := operations.BulkMetadataFetchParams{
				PreferAudible: p.PreferAudible,
				SkipCached:    p.ResolveSkipCached(),
			}

			progress := registryProgressAdapter{r: reporter}

			bookIDs, err := operations.ResolveBookIDs(p.Selection, func(f operations.FilterSpec) ([]string, error) {
				return s.resolveFilterToBookIDs(ctx, f)
			})
			if err != nil {
				return fmt.Errorf("bulk_metadata_fetch: resolve selection: %w", err)
			}

			var incomplete bool
			if len(bookIDs) > 0 {
				incomplete, err = s.runBulkMetadataFetchForBookIDs(ctx, opID, bookIDs, fetchParams, store, progress)
			} else {
				incomplete, err = s.runBulkMetadataFetchAll(ctx, opID, fetchParams, store, progress)
			}
			if err != nil {
				return err
			}
			if !incomplete {
				return nil
			}

			// Work was left behind because the timeout was approaching. Queue the
			// next link. ctx.Err() guards the case where the run stopped because
			// the USER canceled it -- continuing that chain would resurrect work
			// somebody deliberately killed.
			if ctx.Err() != nil {
				return nil
			}
			next := nextContinuationParams(p, runKey)
			nextID, eerr := reg.EnqueueOp(ctx, "library.bulk-metadata-fetch", next)
			if eerr != nil {
				// Not fatal to THIS run -- the books it did fetch are cached and
				// ledgered -- but it does end the chain, so say so loudly rather
				// than reporting a clean finish.
				logging.Error(ctx, "bulk-metadata-fetch: could not queue continuation; chain ends here",
					"run_key", runKey, "continuation", next.Continuation, "err", eerr)
				return fmt.Errorf("bulk_metadata_fetch: queue continuation %d: %w", next.Continuation, eerr)
			}
			if nextID == "" {
				return fmt.Errorf("bulk_metadata_fetch: continuation %d returned an empty op id", next.Continuation)
			}
			logging.Info(ctx, "bulk-metadata-fetch: queued continuation",
				"run_key", runKey, "continuation", next.Continuation, "next_op_id", nextID)
			_ = reporter.Log(slog.LevelInfo, "stopped before timeout; queued continuation",
				slog.String("next_op_id", nextID), slog.Int("continuation", next.Continuation))
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBulkMetadataFetchOp(reg) })
}

// runBulkMetadataFetchForBookIDs fetches and caches metadata for a specific set
// of books identified by ID. It shares resume semantics with runBulkMetadataFetchAll:
// books that already have an OperationResult row for this opID are skipped.
func (s *Server) runBulkMetadataFetchForBookIDs(
	ctx context.Context,
	opID string,
	bookIDs []string,
	params operations.BulkMetadataFetchParams,
	store bulkMetadataFetchByIDStore,
	progress operations.ProgressReporter,
) (incomplete bool, err error) {
	// Create operation context for structured logging
	op := &logging.OpContext{
		ID:     opID,
		Type:   "metadata-fetch-ids",
		Status: "pending",
	}
	ctx = logging.WithOp(ctx, op)
	// Track requested books in operation context
	op.AddEntity("books", bookIDs...)

	_ = progress.UpdateProgress(0, len(bookIDs), "loading books")

	maxAge := time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour

	existingResults, _ := store.GetOperationResults(opID)
	done := make(map[string]bool, len(existingResults))
	for _, r := range existingResults {
		done[r.BookID] = true
	}

	var authorByID map[int]string
	if len(bookIDs) >= 100 {
		allAuthors, _ := store.GetAllAuthors()
		authorByID = make(map[int]string, len(allAuthors))
		for _, a := range allAuthors {
			authorByID[a.ID] = a.Name
		}
	}

	type bookWork struct {
		book       database.Book
		authorName string
	}
	var work []bookWork
	for _, id := range bookIDs {
		if done[id] {
			continue
		}
		b, err := store.GetBookByID(id)
		if err != nil || b == nil || strings.TrimSpace(b.Title) == "" {
			continue
		}
		if params.SkipCached {
			hasFresh := false
			for _, src := range s.metadataFetchService.BuildSourceChain() {
				if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, id, src.Name(), maxAge); cerr == nil && cached != nil {
					hasFresh = true
					break
				}
			}
			if hasFresh {
				continue
			}
		}
		author := ""
		if b.AuthorID != nil {
			if authorByID != nil {
				author = authorByID[*b.AuthorID]
			} else if a, aerr := store.GetAuthorByID(*b.AuthorID); aerr == nil && a != nil {
				author = a.Name
			}
		}
		work = append(work, bookWork{book: *b, authorName: author})
	}

	alreadyDone := len(existingResults)
	totalBooks := alreadyDone + len(work)
	logging.Info(ctx, "bulk-metadata-fetch-ids total, done, to fetch", "totalBooks", totalBooks, "alreadyDone", alreadyDone, "work_count", len(work))
	_ = progress.UpdateProgress(alreadyDone, totalBooks,
		fmt.Sprintf("resuming: %d/%d already done", alreadyDone, totalBooks))

	sourceChain := s.metadataFetchService.BuildSourceChain()
	if params.PreferAudible {
		audible := metadata.NewAudibleClient()
		var rest []metadata.MetadataSource
		for _, src := range sourceChain {
			if src.Name() != audible.Name() {
				rest = append(rest, src)
			}
		}
		sourceChain = append([]metadata.MetadataSource{audible}, rest...)
	}

	// Counters shared across the worker pool: completed (running total, seeded with
	// the already-done count) plus found/notFound as atomics. The `done` resume map
	// above is fully built before dispatch and read-only inside workers.
	// Stop accepting new books shortly BEFORE the registry's def.Timeout and
	// hand the remainder to a queued successor.
	//
	// Hitting the wall is not a survivable state for this op: worker.go maps
	// context.DeadlineExceeded to the terminal status "canceled", which
	// ListResumableOperationsV2 excludes -- so a run that times out is dead and
	// every book it had left has no route back. Stopping early is what turns
	// "the 6h run died at 95%" into "the 6h run handed 5% to the next link".
	//
	// A book skipped here writes NO ledger row and bumps NO counter, so the
	// successor sees it as outstanding and picks it up.
	var stoppedEarly atomic.Bool
	deadline, hasDeadline := ctx.Deadline()

	completed := int64(alreadyDone)
	// errored counts books whose providers failed (throttled, broken, or
	// circuit-open) as distinct from books genuinely absent from every
	// catalog. Folding the two together is what made a rate-limited run
	// indistinguishable from a complete one.
	var found, notFound, errored atomic.Int64

	// Per-provider semaphore (fixed cap 2) shared by all workers, built from the
	// read-only sourceChain — see runBulkMetadataFetchAll.
	sem := newProviderSemaphore(sourceChain, perProviderFetchCap)

	var progressMu sync.Mutex
	reportProgress := func(current, total int, message string) {
		progressMu.Lock()
		_ = progress.UpdateProgress(current, total, message)
		progressMu.Unlock()
	}

	// processOne handles one book; body is the former serial loop body, unchanged
	// except counters are atomics and the live source calls are wrapped in the
	// per-provider semaphore (cache reads / result writes stay outside). Returns an
	// error only for genuine context cancellation; per-book fetch errors record a
	// not_found row and return nil.
	processOne := func(gctx context.Context, i int) error {
		if gctx.Err() != nil {
			return gctx.Err()
		}
		if hasDeadline && time.Until(deadline) < bulkFetchContinuationMargin {
			stoppedEarly.Store(true)
			return nil
		}
		w := work[i]
		bookID := w.book.ID

		// Skip obvious chapter fragments of shattered audiobooks (see
		// runBulkMetadataFetchAll for the rationale): never search or cache a
		// bogus catalog match for a title like "06 Chapter 6".
		if metadata.IsLikelyChapterFragment(w.book.Title) {
			_ = store.CreateOperationResult(&database.OperationResult{
				OperationID: opID,
				BookID:      bookID,
				ResultJSON:  `{"status":"skipped_fragment","source":""}`,
				Status:      fetchStatusSkippedFragment,
			})
			notFound.Add(1)
			n := atomic.AddInt64(&completed, 1)
			if n%50 == 0 || int(n) == totalBooks {
				reportProgress(int(n), totalBooks,
					fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d errors:%d", n, totalBooks, found.Load(), notFound.Load(), errored.Load()))
			}
			return nil
		}

		out, werr := walkSourceChain(gctx, store, sourceChain, sem, bookID, w.book.Title, w.authorName, maxAge)
		if werr != nil {
			return werr
		}
		resultStatus := out.status()
		sourceName := out.sourceName
		cacheHit := out.cacheHit
		switch resultStatus {
		case fetchStatusCached:
			if !cacheHit {
				if blob, merr := json.Marshal(out.results); merr == nil {
					_ = database.PutCachedMetadataFetch(store, bookID, sourceName, blob, 0)
				}
			}
			found.Add(1)
		case fetchStatusFetchError:
			// Deliberately NOT counted as not_found. Nothing was cached for this
			// book, so a later "fetch only what is missing" pass will retry it --
			// but only if the ledger says the provider failed rather than that the
			// book is absent from every catalog.
			errored.Add(1)
			logging.Warn(gctx, "bulk-metadata-fetch: provider error; book left retryable",
				"book_id", bookID, "provider", out.errSource, "error", out.err)
		default:
			notFound.Add(1)
		}

		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: opID,
			BookID:      bookID,
			ResultJSON:  fmt.Sprintf(`{"status":%q,"source":%q}`, resultStatus, sourceName),
			Status:      resultStatus,
		})

		n := atomic.AddInt64(&completed, 1)
		if n%50 == 0 || int(n) == totalBooks {
			reportProgress(int(n), totalBooks,
				fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d errors:%d", n, totalBooks, found.Load(), notFound.Load(), errored.Load()))
		}
		// The condition covers a FAILED live call too (sourceName is empty then):
		// gating on success alone meant the pause was skipped exactly when a
		// provider was throttling us, which is when backing off matters most.
		if !cacheHit && (sourceName != "" || out.err != nil) && i < len(work)-1 {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		return nil
	}

	if err := runBookFetchPool(ctx, bulkFetchWorkers(), len(work), processOne); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	finalCount := atomic.LoadInt64(&completed)
	_ = progress.UpdateProgress(int(finalCount), totalBooks,
		fmt.Sprintf("complete — cached:%d not_found:%d errors:%d", found.Load(), notFound.Load(), errored.Load()))
	op.SetStatus("success")
	logging.Info(ctx, "bulk-metadata-fetch-ids complete", "finalCount", finalCount, "found", found.Load(), "notFound", notFound.Load(),
		"errors", errored.Load(), "stopped_early", stoppedEarly.Load())
	return stoppedEarly.Load(), nil
}

// runBulkWriteBack writes tags (and optionally renames) for each book in bookIDs,
// starting at startIdx. Uses a parallel worker pool — cover embedding and tag
// writes both go through TagLib so there is no ffmpeg ordering constraint.
// Checkpoints every 10 completions so a restart can resume near where it left off.
func (s *Server) runBulkWriteBack(
	ctx context.Context,
	opID string,
	bookIDs []string,
	doRename bool,
	startIdx int,
	progress operations.ProgressReporter,
) error {
	workers := writeBackWorkers()

	store := s.storeForWiring()
	mfs := s.metadataFetchService
	total := len(bookIDs)

	if startIdx > 0 {
		_ = progress.Log("info", fmt.Sprintf("resuming bulk write-back from index %d/%d", startIdx, total), nil)
	}

	// The channel carries bare IDs. Previously it carried a pre-loaded
	// *database.Book because the PRODUCER did GetBookByID, isProtectedPath,
	// GetBookTags and RunApplyPipelineRenameOnly synchronously before handing
	// off — a full rename (the slowest step in the whole loop) ran on the single
	// producer goroutine while every consumer sat idle. All of that per-book work
	// now happens inside the workers.
	//
	// Buffer is 32x the pool rather than 2x. The old workers*2 buffer meant the
	// producer blocked as soon as it was a couple of items ahead, so a single
	// slow book stalled the feed for everyone; with only IDs in flight the buffer
	// is nearly free (a []string of a few hundred entries).
	jobCh := make(chan string, workers*32)
	var wg sync.WaitGroup
	var written, failed, skipped atomic.Int64

	// ckptMu guards ONLY SaveCheckpoint, which writes a shared state blob to the
	// store. It deliberately does NOT wrap progress.Log / progress.UpdateProgress:
	// the reporter serializes those internally on its own progressMu, so the mutex
	// that used to wrap them here was redundant and was serializing every worker
	// through the reporter for no benefit.
	//
	// Honesty note on the checkpoint value: with N workers finishing out of order,
	// the completion COUNT is no longer a valid resume INDEX — a resume from that
	// index can skip a straggler and re-do a book that already finished. Write-back
	// is idempotent (it rewrites the same tags from the same DB row), so re-doing is
	// harmless; skipping is the real cost, and it is bounded by the in-flight window
	// (at most `workers` books). Accepted deliberately: without a checkpoint at all,
	// a restart replays the entire library from zero.
	var ckptMu sync.Mutex

	// canceled is set by any worker (or the feeder) that observes cancellation so
	// the "canceled" log line is emitted exactly once rather than once per worker.
	var canceled atomic.Bool

	processOne := func(bookID string) {
		book, err := store.GetBookByID(bookID)
		if err != nil || book == nil {
			failed.Add(1)
			_ = progress.Log("warn", fmt.Sprintf("book %s: not found", bookID), nil)
			return
		}
		if s.isProtectedPath(book.FilePath) {
			skipped.Add(1)
			_ = progress.Log("info", fmt.Sprintf("book %s: skipping protected path", bookID), nil)
			return
		}
		if tags, tagErr := store.GetBookTags(bookID); tagErr == nil {
			if policy.EvaluatePolicy(tags).NoWriteback {
				skipped.Add(1)
				_ = progress.Log("info", fmt.Sprintf("book %s: skipping write-back (policy:no-writeback tag)", bookID), nil)
				return
			}
		}

		releaseFileWrite, gateErr := writeBackFileGate.acquire(ctx)
		if gateErr != nil {
			failed.Add(1)
			_ = progress.Log("warn", fmt.Sprintf("book %s: write-back canceled while waiting for file I/O", bookID), nil)
			return
		}
		defer releaseFileWrite()

		// Rename FIRST, then re-read, then lock. Order matters: the rename moves
		// the file, so a lock taken on the pre-rename path would guard a path
		// nothing is about to be written to. The rename itself is guarded on the
		// OLD path so two workers cannot move the same file at once.
		if doRename {
			releaseOld := writeBackPathLocks.lock(book.FilePath)
			renameErr := mfs.RunApplyPipelineRenameOnly(bookID, book)
			releaseOld()
			if renameErr != nil {
				_ = progress.Log("warn", fmt.Sprintf("book %s: rename failed: %v", bookID, renameErr), nil)
			}
			if fresh, freshErr := store.GetBookByID(bookID); freshErr == nil && fresh != nil {
				book = fresh
			}
		}

		// Serialize on the destination path. See path_locks.go for the three
		// hazards this closes (version-group siblings, protected-path redirect,
		// and the one-second-granularity .bak- backup name).
		release := writeBackPathLocks.lock(book.FilePath)
		count, writeErr := mfs.WriteBackMetadataForBook(bookID)
		release()

		if writeErr != nil {
			failed.Add(1)
			_ = progress.Log("warn", fmt.Sprintf("book %s: write-back failed: %v", bookID, writeErr), nil)
			return
		}
		written.Add(1)
		if count > 0 && s.activityWriter != nil {
			activity.LogBatch(s.activityWriter, opID, "metadata-apply", "write-back",
				activity.BatchItem{Name: book.Title, Count: count})
		}
	}

	for range workers {
		wg.Go(func() {
			for bookID := range jobCh {
				// Cancellation is checked per item inside the worker, not only in
				// the feeder: with a deep buffer the feeder can be finished long
				// before the workers are, so a feeder-only check would let a
				// canceled op keep writing files for the length of the backlog.
				if ctx.Err() != nil || progress.IsCanceled() {
					canceled.Store(true)
					continue // drain, don't return — returning would deadlock the feeder
				}
				processOne(bookID)

				done := written.Load() + failed.Load() + skipped.Load()
				// UpdateProgress on EVERY item, unconditionally: this is what resets
				// the registry stuck-op watchdog (5-minute default ProgressTimeout).
				// The message is deliberately coarse-grained in shape but carries the
				// running tallies the operator needs.
				_ = progress.UpdateProgress(int(done), total,
					fmt.Sprintf("processing %d/%d (%d written, %d failed, %d skipped)", done, total, written.Load(), failed.Load(), skipped.Load()))
				if done%10 == 0 {
					ckptMu.Lock()
					_ = operations.SaveCheckpoint(store, opID, "bulk_write_back", "writing", int(done), total)
					ckptMu.Unlock()
				}
			}
		})
	}

	for i := startIdx; i < total; i++ {
		if ctx.Err() != nil || progress.IsCanceled() {
			canceled.Store(true)
			_ = progress.Log("info", fmt.Sprintf("canceled after feeding %d/%d books", i-startIdx, total-startIdx), nil)
			break
		}
		select {
		case jobCh <- bookIDs[i]:
		case <-ctx.Done():
		}
	}
	close(jobCh)
	wg.Wait()

	if canceled.Load() {
		_ = progress.Log("info", "bulk write-back canceled before completing all books", nil)
	}

	_ = operations.ClearState(store, opID)
	summary := fmt.Sprintf("bulk write-back complete: %d written, %d failed, %d skipped out of %d", written.Load(), failed.Load(), skipped.Load(), total)
	_ = progress.Log("info", summary, nil)
	if s.activityWriter != nil {
		activity.FlushOperation(s.activityWriter, opID)
	}
	return nil
}

// runIsbnEnrichment enriches missing ISBN identifiers from external sources.
// Idempotent — books that already have an ISBN are skipped, so a restart
// safely re-runs from scratch (no checkpoint needed).
func (s *Server) runIsbnEnrichment(ctx context.Context, progress operations.ProgressReporter, opID string) error {
	if s.metadataFetchService == nil || s.metadataFetchService.ISBNEnrichment() == nil {
		_ = progress.Log("info", "ISBN enrichment service is not configured, skipping", nil)
		return nil
	}
	startMsg := "Scanning for books missing ISBN identifiers"
	_ = progress.Log("info", startMsg, nil)
	if operations.IsManual(ctx) {
		activity.EmitInfo(s.activityWriter, opID, "isbn-enrich", "isbn-enrichment", startMsg, activity.AlwaysShow)
	}
	checked, updated, err := s.metadataFetchService.ISBNEnrichment().EnrichMissingISBNs(ctx, 100, s.activityWriter, opID)
	if err != nil {
		return err
	}
	activity.FlushOperation(s.activityWriter, opID)
	msg := fmt.Sprintf("ISBN enrichment complete: checked %d, updated %d", checked, updated)
	_ = progress.Log("info", msg, nil)
	// Use real (checked, checked) so the bar is honest. Fall back to (1,1)
	// when nothing was checked to avoid 0/0.
	total := checked
	if total <= 0 {
		total = 1
	}
	_ = progress.UpdateProgress(total, total, fmt.Sprintf("%s (%d/%d 100.00%%)", msg, total, total))
	tags := activity.TagsIf(updated == 0, activity.NoOpTag)
	if operations.IsManual(ctx) {
		tags = append(tags, activity.AlwaysShow)
	}
	activity.EmitInfo(s.activityWriter, opID, "isbn-enrich", "isbn-enrichment", msg, tags...)
	return nil
}

// runMetadataRefreshScan reports books with incomplete metadata. Read-only,
// safe to re-run on restart with no state.
func (s *Server) runMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error {
	store := s.Ops()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = progress.Log("info", "Starting metadata refresh scan", nil)
	// Pre-load total is unknown; placeholder (0/1) avoids 0/0.
	_ = progress.UpdateProgress(0, 1, "Scanning books for incomplete metadata... (0/1 0.00%)")
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("failed to get books: %w", err)
	}
	_ = progress.Log("info", fmt.Sprintf("Checking %d books for incomplete metadata", len(books)), nil)
	incomplete := 0
	for i, book := range books {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if book.AuthorID == nil || book.Title == "" {
			incomplete++
			_ = progress.Log("debug", fmt.Sprintf("Incomplete: %q (id=%s)", book.Title, book.ID), nil)
		}
		if (i+1)%200 == 0 {
			_ = progress.UpdateProgress(i+1, len(books), fmt.Sprintf("Checked %d/%d books", i+1, len(books)))
		}
	}
	resultMsg := fmt.Sprintf("Found %d books with incomplete metadata out of %d total", incomplete, len(books))
	_ = progress.Log("info", resultMsg, nil)
	_ = progress.UpdateProgress(len(books), len(books), resultMsg)
	return nil
}
