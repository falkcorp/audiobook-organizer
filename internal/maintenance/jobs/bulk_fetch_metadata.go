// file: internal/maintenance/jobs/bulk_fetch_metadata.go
// version: 1.12.0
// guid: b3c9d7e8-0f1a-2b3c-4d5e-6f7a8b9c0d1e
// last-edited: 2026-09-05

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func init() { maintenance.Register(&bulkFetchMetadataJob{}) }

type bulkFetchMetadataJob struct{}

type bmf_params struct {
	PreferAudible bool `json:"prefer_audible"`
	SkipCached    bool `json:"skip_cached"`
}

func (j *bulkFetchMetadataJob) ID() string       { return "bulk-fetch-metadata" }
func (j *bulkFetchMetadataJob) Name() string     { return "Bulk Fetch Metadata" }
func (j *bulkFetchMetadataJob) Category() string { return "Metadata" }
func (j *bulkFetchMetadataJob) Description() string {
	return "Fetches and caches metadata from all configured sources for every book in the library"
}
func (j *bulkFetchMetadataJob) DefaultParams() any { return &bmf_params{} }
func (j *bulkFetchMetadataJob) CanResume() bool    { return true }
func (j *bulkFetchMetadataJob) Permission() string { return string(auth.PermLibraryEditMetadata) }

func (j *bulkFetchMetadataJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	// prefer_audible / skip_cached arrive on the run's own params blob, via the
	// context. This used to read store.GetOperationParams(opID), whose only
	// writer (operations.SaveParams) has no caller on the maintenance path since
	// the v1 op minter was retired (#2784) — so both flags were silently pinned
	// to false no matter what the operator sent. See maintenance.WithRawParams.
	//
	// Both zero values are the conservative choice (source chain unchanged, no
	// cache skipping), which is what makes an absent blob safe to fall through on:
	// a requeue re-enqueues with nil params.
	preferAudible := false
	skipCached := false
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		var p bmf_params
		if jerr := json.Unmarshal(raw, &p); jerr == nil {
			preferAudible = p.PreferAudible
			skipCached = p.SkipCached
		}
	}

	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}

	ttlDays := config.AppConfig.MetadataFetchCacheTTLDays

	var existingResults []database.OperationResult
	if opID != "" {
		existingResults, _ = store.GetOperationResults(opID)
	}
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

	sourceChain := bmf_buildSourceChain()
	if len(sourceChain) == 0 {
		sourceChain = []metadata.MetadataSource{metadata.NewChainSource(metadata.NewAudibleClient())}
	}

	// Same policy as the v2 op, from the same function -- this job builds its own
	// chain, so a guard placed only on the other path would never fire here.
	live, skipped, perr := metafetch.PrepareFetchChain(sourceChain, preferAudible)
	if len(skipped) > 0 {
		slog.Warn("bulk-fetch-metadata: skipping throttled providers",
			"skipped", strings.Join(skipped, ","), "detail", metadata.ThrottleSummary(skipped))
	}
	if perr != nil {
		return perr
	}
	sourceChain = live

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
		if skipCached {
			maxAge := time.Duration(ttlDays) * 24 * time.Hour
			hasFreshCache := false
			for _, src := range sourceChain {
				if cached, _, cerr := database.CachedMetadataForProvider(store, b.ID, metadata.ProviderIDOf(src), src.Name(), maxAge); cerr == nil && cached != nil {
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
	slog.Info("bulk-fetch-metadata books total, already cached, to fetch", "opID", opID, "totalBooks", totalBooks, "alreadyDone", alreadyDone, "work_count", len(work))

	reporter.SetTotal(totalBooks)
	for range alreadyDone {
		reporter.Increment()
	}

	if len(work) == 0 {
		slog.Info("all books already cached")
		return nil
	}

	completed := int64(alreadyDone)
	found := 0
	notFound := 0
	errored := 0

	maxAge := time.Duration(ttlDays) * 24 * time.Hour
	// Bound concurrent calls per provider exactly as the v2 operation does, so
	// this path cannot stampede a provider the other path is being polite to.
	sem := metafetch.NewProviderSemaphore(sourceChain, metafetch.DefaultPerProviderFetchCap)

	for i, w := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		bookID := w.book.ID
		currentAuthor := w.authorName
		out, werr := metafetch.WalkSourceChain(ctx, store, sourceChain, sem,
			bookID, w.book.Title, currentAuthor, maxAge)
		if werr != nil {
			return werr
		}
		if out.AllThrottled {
			return metafetch.AllThrottledMidRunError(sourceChain)
		}
		sourceName := out.SourceName
		cacheHit := out.CacheHit

		resultStatus := out.Status()
		switch resultStatus {
		case metafetch.FetchStatusCached:
			if !cacheHit {
				if blob, merr := json.Marshal(out.Results); merr == nil {
					_ = database.PutCachedMetadataFetch(store, bookID, out.ProviderKey, blob, 0)
				}
			}
			found++
		case metafetch.FetchStatusFetchError:
			// A provider failure is not a missing book. Recording it as not_found
			// here would put false misses in the ledger from THIS path even though
			// the v2 operation stopped doing so.
			errored++
		default:
			notFound++
		}

		if opID != "" {
			_ = store.CreateOperationResult(&database.OperationResult{
				OperationID: opID,
				BookID:      bookID,
				ResultJSON:  metafetch.LedgerResultJSON(resultStatus, sourceName, out.Variant),
				Status:      resultStatus,
			})
		}

		atomic.AddInt64(&completed, 1)
		reporter.Increment()

		// Rate-limit live API calls.
		if !cacheHit && (sourceName != "" || out.Err != nil) && i < len(work)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	finalCount := atomic.LoadInt64(&completed)
	slog.Info("bulk-fetch-metadata done", "opID", opID, "finalCount", finalCount, "found", found, "notFound", notFound, "errors", errored)
	slog.Info("complete", "found", found, "notFound", notFound, "errors", errored)
	return nil
}

// bmf_buildSourceChain reads config.AppConfig.MetadataSources and returns
// ordered, circuit-breaker-wrapped metadata sources.
func bmf_buildSourceChain() []metadata.MetadataSource {
	sources := make([]config.MetadataSource, len(config.AppConfig.MetadataSources))
	copy(sources, config.AppConfig.MetadataSources)
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Priority < sources[j].Priority
	})

	var chain []metadata.MetadataSource
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		var rawSource metadata.MetadataSource
		switch src.ID {
		case "openlibrary":
			rawSource = metadata.NewOpenLibraryClient()
		case "google-books":
			apiKey := config.AppConfig.GoogleBooksAPIKey
			if apiKey == "" {
				if k, ok := src.Credentials["apiKey"]; ok && k != "" {
					apiKey = k
				}
			}
			rawSource = metadata.NewGoogleBooksClient(apiKey)
		case "audible":
			rawSource = metadata.NewAudibleClient()
		case "audnexus":
			rawSource = metadata.NewAudnexusClient()
		case "hardcover":
			token := config.AppConfig.HardcoverAPIToken
			if token == "" {
				if apiToken, ok := src.Credentials["api_token"]; ok && apiToken != "" {
					token = apiToken
				} else if apiKey, ok := src.Credentials["apiKey"]; ok && apiKey != "" {
					token = apiKey
				}
			}
			if token != "" {
				rawSource = metadata.NewHardcoverClient(token)
			} else {
				slog.Warn("Hardcover source enabled but no API token configured")
			}
		case "wikipedia":
			rawSource = metadata.NewWikipediaClient()
		default:
			slog.Warn("Unknown metadata source", "src", src.ID)
		}
		if rawSource != nil {
			chain = append(chain, metadata.NewChainSource(rawSource))
		}
	}
	return chain
}

// Policy: ResumeRestart. This was ResumeRequeue until 2026-08-23, on the reasoning
// that the job "checkpoints nothing, so resume already means re-run from zero".
// That was true of OpState and false in practice: the results table is this job's
// de-facto checkpoint. Run() reads GetOperationResults(OperationIDFromCtx(ctx)) to
// build the `done` skip-set, so the operation id IS the resume anchor.
//
// ResumeRequeue mints a fresh ULID for the new row. That was survivable only while
// the anchor travelled in params as legacy_op_id and was copied across the requeue;
// once the ctx id became the v2 row id, requeueing moved the anchor on every resume
// and an interrupted run re-fetched the entire library over the network. ResumeRestart
// updates the row in place, so the id — and therefore the skip-set — is stable.
//
// SCOPE: this makes the declaration correct, and correct is only consulted on
// one path. resumeAfterStartup takes its candidates from ListActiveOperationsV2
// (the opv2:act: index = queued|running), and every clean shutdown writes a
// status that deletes that key -- so a job stopped by a deploy is invisible to
// the sweep whatever its policy says, and only a hard kill leaves a row it can
// act on. That gap is pre-existing, affects every v2 op, and is tracked in
// todo.d/20260823-v2-resume-sweep-is-blind-to-interrupted-rows.md. Declaring
// ResumeDrop here would additionally throw the run away on the one path that
// does work.
func (j *bulkFetchMetadataJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
