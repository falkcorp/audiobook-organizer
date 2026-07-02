// file: internal/server/metadata_ops.go
// version: 1.3.0
// guid: fba55738-5898-4950-8e79-3ee008ad0c70
// last-edited: 2026-07-01
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
)

// runBulkMetadataFetchAll is the resumable core of the full-library metadata
// fetch. It ONLY fetches and caches — it never writes to book records.
// Results land in PutCachedMetadataFetch so the per-book review UI can show
// them immediately when the user clicks "apply". Idempotent: books with an
// existing OperationResult row are skipped on resume.
func (s *Server) runBulkMetadataFetchAll(
	ctx context.Context,
	opID string,
	params operations.BulkMetadataFetchParams,
	store database.Store,
	progress operations.ProgressReporter,
) error {
	// Create operation context for structured logging
	op := &logging.OpContext{
		ID:     opID,
		Type:   "metadata-fetch",
		Status: "pending",
	}
	ctx = logging.WithOp(ctx, op)

	// Total unknown until books load; use placeholder (0/1) to avoid 0/0.
	_ = progress.UpdateProgress(0, 1, "loading books (0/1 0.00%)")

	allBooks, err := store.GetAllBooks(0, 0)
	if err != nil {
		op.SetStatus("failed")
		logging.Error(ctx, "failed to load all books", "err", err)
		return fmt.Errorf("GetAllBooks: %w", err)
	}

	maxAge := time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour

	existingResults, _ := store.GetOperationResults(opID)
	done := make(map[string]bool, len(existingResults))
	for _, r := range existingResults {
		done[r.BookID] = true
	}

	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("GetAllAuthors: %w", err)
	}
	authorByID := make(map[int]string, len(allAuthors))
	for _, a := range allAuthors {
		authorByID[a.ID] = a.Name
	}

	type bookWork struct {
		book       database.Book
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
		return nil
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

	completed := int64(alreadyDone)
	found := 0
	notFound := 0

	for i, w := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}

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
				Status:      "skipped_fragment",
			})
			notFound++
			n := atomic.AddInt64(&completed, 1)
			if i%50 == 0 || int(n) == totalBooks {
				_ = progress.UpdateProgress(int(n), totalBooks,
					fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d", n, totalBooks, found, notFound))
			}
			continue
		}

		searchTitle := stripChapterFromTitle(w.book.Title)

		var metaResults []metadata.BookMetadata
		var sourceName string
		cacheHit := false

		for _, src := range sourceChain {
			if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, bookID, src.Name(), maxAge); cerr == nil && cached != nil {
				var cachedResults []metadata.BookMetadata
				if jerr := json.Unmarshal(cached.Results, &cachedResults); jerr == nil && len(cachedResults) > 0 {
					metaResults = cachedResults
					sourceName = src.Name()
					cacheHit = true
					break
				}
			}
			var fetchErr error
			if currentAuthor != "" {
				metaResults, fetchErr = src.SearchByTitleAndAuthor(ctx, searchTitle, currentAuthor)
				if fetchErr == nil && len(metaResults) > 0 {
					sourceName = src.Name()
					break
				}
			}
			metaResults, fetchErr = src.SearchByTitle(ctx, searchTitle)
			if fetchErr == nil && len(metaResults) > 0 {
				sourceName = src.Name()
				break
			}
			if searchTitle != w.book.Title {
				if currentAuthor != "" {
					metaResults, fetchErr = src.SearchByTitleAndAuthor(ctx, w.book.Title, currentAuthor)
					if fetchErr == nil && len(metaResults) > 0 {
						sourceName = src.Name()
						break
					}
				}
				metaResults, fetchErr = src.SearchByTitle(ctx, w.book.Title)
				if fetchErr == nil && len(metaResults) > 0 {
					sourceName = src.Name()
					break
				}
			}
		}

		resultStatus := "not_found"
		if len(metaResults) > 0 && sourceName != "" {
			if !cacheHit {
				if blob, merr := json.Marshal(metaResults); merr == nil {
					_ = database.PutCachedMetadataFetch(store, bookID, sourceName, blob, 0)
				}
			}
			found++
			resultStatus = "cached"
		} else {
			notFound++
		}

		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: opID,
			BookID:      bookID,
			ResultJSON:  fmt.Sprintf(`{"status":%q,"source":%q}`, resultStatus, sourceName),
			Status:      resultStatus,
		})

		n := atomic.AddInt64(&completed, 1)
		if i%50 == 0 || int(n) == totalBooks {
			_ = progress.UpdateProgress(int(n), totalBooks,
				fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d", n, totalBooks, found, notFound))
		}

		// Rate-limit live API calls; cache hits are instant so skip the delay.
		if !cacheHit && sourceName != "" && i < len(work)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	finalCount := atomic.LoadInt64(&completed)
	_ = progress.UpdateProgress(int(finalCount), totalBooks,
		fmt.Sprintf("complete — cached:%d not_found:%d", found, notFound))
	op.SetStatus("success")
	logging.Info(ctx, "bulk-metadata-fetch complete", "finalCount", finalCount, "found", found, "notFound", notFound)
	return nil
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
		matched := metabatch.LatestMatchedBookIDs(s.Store())
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
			store := s.Store()
			if store == nil {
				return fmt.Errorf("bulk_metadata_fetch: database not initialized")
			}

			// Generate a stable opID for OperationResult rows (resume key).
			// The registry assigns its own run ID; we derive a deterministic
			// sub-ID so OperationResult rows survive restarts.
			opID := ulid.Make().String()

			fetchParams := operations.BulkMetadataFetchParams{
				PreferAudible: p.PreferAudible,
				SkipCached:    p.SkipCached,
			}

			progress := registryProgressAdapter{r: reporter}

			bookIDs, err := operations.ResolveBookIDs(p.Selection, func(f operations.FilterSpec) ([]string, error) {
				return s.resolveFilterToBookIDs(ctx, f)
			})
			if err != nil {
				return fmt.Errorf("bulk_metadata_fetch: resolve selection: %w", err)
			}

			if len(bookIDs) > 0 {
				return s.runBulkMetadataFetchForBookIDs(ctx, opID, bookIDs, fetchParams, store, progress)
			}
			return s.runBulkMetadataFetchAll(ctx, opID, fetchParams, store, progress)
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
	store database.Store,
	progress operations.ProgressReporter,
) error {
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

	completed := int64(alreadyDone)
	found, notFound := 0, 0
	for i, w := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		bookID := w.book.ID

		// Skip obvious chapter fragments of shattered audiobooks (see
		// runBulkMetadataFetchAll for the rationale): never search or cache a
		// bogus catalog match for a title like "06 Chapter 6".
		if metadata.IsLikelyChapterFragment(w.book.Title) {
			_ = store.CreateOperationResult(&database.OperationResult{
				OperationID: opID,
				BookID:      bookID,
				ResultJSON:  `{"status":"skipped_fragment","source":""}`,
				Status:      "skipped_fragment",
			})
			notFound++
			n := atomic.AddInt64(&completed, 1)
			if i%50 == 0 || int(n) == totalBooks {
				_ = progress.UpdateProgress(int(n), totalBooks,
					fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d", n, totalBooks, found, notFound))
			}
			continue
		}

		searchTitle := stripChapterFromTitle(w.book.Title)

		var metaResults []metadata.BookMetadata
		var sourceName string
		cacheHit := false
		for _, src := range sourceChain {
			if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, bookID, src.Name(), maxAge); cerr == nil && cached != nil {
				var cr []metadata.BookMetadata
				if jerr := json.Unmarshal(cached.Results, &cr); jerr == nil && len(cr) > 0 {
					metaResults, sourceName, cacheHit = cr, src.Name(), true
					break
				}
			}
			var ferr error
			if w.authorName != "" {
				metaResults, ferr = src.SearchByTitleAndAuthor(ctx, searchTitle, w.authorName)
				if ferr == nil && len(metaResults) > 0 {
					sourceName = src.Name()
					break
				}
			}
			metaResults, ferr = src.SearchByTitle(ctx, searchTitle)
			if ferr == nil && len(metaResults) > 0 {
				sourceName = src.Name()
				break
			}
		}

		resultStatus := "not_found"
		if len(metaResults) > 0 && sourceName != "" {
			if !cacheHit {
				if blob, merr := json.Marshal(metaResults); merr == nil {
					_ = database.PutCachedMetadataFetch(store, bookID, sourceName, blob, 0)
				}
			}
			found++
			resultStatus = "cached"
		} else {
			notFound++
		}
		_ = store.CreateOperationResult(&database.OperationResult{
			OperationID: opID,
			BookID:      bookID,
			ResultJSON:  fmt.Sprintf(`{"status":%q,"source":%q}`, resultStatus, sourceName),
			Status:      resultStatus,
		})

		n := atomic.AddInt64(&completed, 1)
		if i%50 == 0 || int(n) == totalBooks {
			_ = progress.UpdateProgress(int(n), totalBooks,
				fmt.Sprintf("fetched %d/%d — cached:%d not_found:%d", n, totalBooks, found, notFound))
		}
		if !cacheHit && sourceName != "" && i < len(work)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	finalCount := atomic.LoadInt64(&completed)
	_ = progress.UpdateProgress(int(finalCount), totalBooks,
		fmt.Sprintf("complete — cached:%d not_found:%d", found, notFound))
	op.SetStatus("success")
	logging.Info(ctx, "bulk-metadata-fetch-ids complete", "finalCount", finalCount, "found", found, "notFound", notFound)
	return nil
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
	const workers = 2

	store := s.Store()
	mfs := s.metadataFetchService
	total := len(bookIDs)

	if startIdx > 0 {
		_ = progress.Log("info", fmt.Sprintf("resuming bulk write-back from index %d/%d", startIdx, total), nil)
	}

	type job struct {
		id   string
		book *database.Book
	}

	jobCh := make(chan job, workers*2)
	var wg sync.WaitGroup
	var written, failed atomic.Int64
	var mu sync.Mutex

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				if ctx.Err() != nil {
					return
				}
				count, writeErr := mfs.WriteBackMetadataForBook(j.id)
				if writeErr != nil {
					failed.Add(1)
					mu.Lock()
					_ = progress.Log("warn", fmt.Sprintf("book %s: write-back failed: %v", j.id, writeErr), nil)
					mu.Unlock()
				} else {
					written.Add(1)
					if count > 0 && s.activityWriter != nil {
						activity.LogBatch(s.activityWriter, opID, "metadata-apply", "write-back",
							activity.BatchItem{Name: j.book.Title, Count: count})
					}
				}
				done := written.Load() + failed.Load()
				mu.Lock()
				_ = progress.UpdateProgress(int(done), total, fmt.Sprintf("processing %d/%d (%d written, %d failed)", done, total, written.Load(), failed.Load()))
				if done%10 == 0 {
					_ = operations.SaveCheckpoint(store, opID, "bulk_write_back", "writing", int(done), total)
				}
				mu.Unlock()
			}
		}()
	}

	for i := startIdx; i < total; i++ {
		if ctx.Err() != nil || progress.IsCanceled() {
			mu.Lock()
			_ = progress.Log("info", fmt.Sprintf("canceled after feeding %d/%d books", i-startIdx, total-startIdx), nil)
			mu.Unlock()
			break
		}

		bookID := bookIDs[i]
		book, err := store.GetBookByID(bookID)
		if err != nil || book == nil {
			failed.Add(1)
			mu.Lock()
			_ = progress.Log("warn", fmt.Sprintf("book %s: not found", bookID), nil)
			mu.Unlock()
			continue
		}
		if s.isProtectedPath(book.FilePath) {
			mu.Lock()
			_ = progress.Log("info", fmt.Sprintf("book %s: skipping protected path", bookID), nil)
			mu.Unlock()
			continue
		}
		if tags, tagErr := store.GetBookTags(bookID); tagErr == nil {
			if policy.EvaluatePolicy(tags).NoWriteback {
				mu.Lock()
				_ = progress.Log("info", fmt.Sprintf("book %s: skipping write-back (policy:no-writeback tag)", bookID), nil)
				mu.Unlock()
				continue
			}
		}
		if doRename {
			if renameErr := mfs.RunApplyPipelineRenameOnly(bookID, book); renameErr != nil {
				mu.Lock()
				_ = progress.Log("warn", fmt.Sprintf("book %s: rename failed: %v", bookID, renameErr), nil)
				mu.Unlock()
			}
		}

		select {
		case jobCh <- job{id: bookID, book: book}:
		case <-ctx.Done():
		}
	}
	close(jobCh)
	wg.Wait()

	_ = operations.ClearState(store, opID)
	summary := fmt.Sprintf("bulk write-back complete: %d written, %d failed out of %d", written.Load(), failed.Load(), total)
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
	store := s.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = progress.Log("info", "Starting metadata refresh scan", nil)
	// Pre-load total is unknown; placeholder (0/1) avoids 0/0.
	_ = progress.UpdateProgress(0, 1, "Scanning books for incomplete metadata... (0/1 0.00%)")
	books, err := store.GetAllBooks(10000, 0)
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
