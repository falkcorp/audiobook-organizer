// file: internal/server/metadata_candidate_op.go
// version: 3.0.0
// guid: 3f7e2c91-b4a0-4d8e-9c5f-1a6b7d8e0f23
// last-edited: 2026-08-22
//
// Registers the metadata.candidate-fetch v2 OperationDef. Pure params
// type moved to internal/metabatch.FetchOpParams.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"golang.org/x/time/rate"
)

// metadataCandidateFetchOpParams is a server-local alias for the shared params
// type so callers in this package do not need to qualify the package name.
type metadataCandidateFetchOpParams = metabatch.FetchOpParams

// RegisterMetadataCandidateFetchOp registers the "metadata.candidate-fetch"
// v2 OperationDef. The HTTP handler enqueues this def and returns the id
// EnqueueOp minted; Run writes OperationResult rows under that same id.
//
// It used to mint a separate v1 operations row first and key results on that,
// which meant the id the client held resolved at one endpoint and not the
// other.
func (s *Server) RegisterMetadataCandidateFetchOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "metadata.candidate-fetch",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "metadata",
		DisplayName:     "Fetch Metadata Candidates",
		Description:     "Fetch and cache metadata candidates for a set of audiobooks (rate-limited, parallel). Results are stored as OperationResult rows for review.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         8 * time.Hour,
		// ResumeRestart, not ResumeDrop. Dropping was only ever survivable because
		// resumeInterruptedMetadataFetch re-enqueued the remainder by hand off the
		// v1 interrupted-ops list; with the v1 row gone that hand-rolled path goes
		// with it, and leaving ResumeDrop would mean an 8-hour fetch interrupted by
		// a restart silently never resumes. Run skips books that already have
		// result rows, so re-entry is idempotent and costs no repeat API calls.
		ResumePolicy:   opsregistry.ResumeRestart,
		ConcurrencyKey: "metadata.candidate-fetch",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkGeneric},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p metadataCandidateFetchOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("metadata-candidate-fetch: decode params: %w", err)
				}
			}
			if len(p.BookIDs) == 0 {
				return nil
			}

			store := s.storeForWiring()
			mfs := s.metadataFetchService
			progress := registryProgressAdapter{r: reporter}
			totalBooks := p.TotalBooks
			if totalBooks == 0 {
				totalBooks = len(p.BookIDs)
			}
			// This op's OWN v2 id. Results used to be keyed on a v1 row minted
			// separately by the handler; that row is gone, and the result keyspace
			// takes an arbitrary string key, so it keys on the id every reader
			// already has.
			opID := opsregistry.ReporterOpID(reporter)

			// SKIP WHAT IS ALREADY FETCHED. This is what makes the op idempotent,
			// and it is load-bearing for ResumePolicy=ResumeRestart: resumeRestart
			// re-enters Run from the top with the ORIGINAL BookIDs (checkpoint state
			// is merged into params, but nothing prunes the work list). Without this
			// filter a restart re-fetches every book — up to ~10K external API calls
			// for a full-library run.
			//
			// It lives here rather than in a restart-time helper because Run is the
			// one place every trigger passes through. The previous arrangement put it
			// in resumeInterruptedMetadataFetch, which only the startup path called,
			// so any other resume trigger silently refetched.
			alreadyDone := 0
			if existing, rerr := store.GetOperationResults(opID); rerr == nil && len(existing) > 0 {
				remaining := metabatch.RemainingBooksToFetch(existing, p.BookIDs)
				alreadyDone = len(p.BookIDs) - len(remaining)
				p.BookIDs = remaining
				if alreadyDone > 0 {
					slog.Info("metadata-candidate-fetch resuming, skipping already-fetched books",
						"opID", opID, "skipped", alreadyDone, "remaining", len(p.BookIDs))
				}
				if len(p.BookIDs) == 0 {
					_ = progress.UpdateProgress(totalBooks, totalBooks, "completed")
					return nil
				}
			}

			_ = progress.UpdateProgress(alreadyDone, totalBooks, fmt.Sprintf("starting: %d books to fetch", len(p.BookIDs)))

			// Rate limiter: 10 requests per second globally across all workers.
			limiter := rate.NewLimiter(rate.Limit(10), 1)

			workCh := make(chan string, len(p.BookIDs))
			for _, id := range p.BookIDs {
				workCh <- id
			}
			close(workCh)

			var completed int64 = int64(alreadyDone)
			var wg sync.WaitGroup
			numWorkers := 8
			if numWorkers > len(p.BookIDs) {
				numWorkers = len(p.BookIDs)
			}

			for i := 0; i < numWorkers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for bookID := range workCh {
						if ctx.Err() != nil {
							return
						}
						result := s.fetchCandidateForBook(ctx, mfs, store, limiter, opID, bookID)
						resultJSON, err := json.Marshal(result)
						if err != nil {
							slog.Warn("metadata-candidate-fetch marshal result for book", "bookID", bookID, "err", err)
							continue
						}
						if err := store.CreateOperationResult(&database.OperationResult{
							OperationID: opID,
							BookID:      bookID,
							ResultJSON:  string(resultJSON),
							Status:      result.Status,
						}); err != nil {
							slog.Warn("metadata-candidate-fetch store result for book", "bookID", bookID, "err", err)
						}
						done := atomic.AddInt64(&completed, 1)
						_ = progress.UpdateProgress(int(done), totalBooks, fmt.Sprintf("fetched %d/%d", done, totalBooks))
					}
				}()
			}
			wg.Wait()

			finalCount := atomic.LoadInt64(&completed)

			// Cancellation is REPORTED, not swallowed. This used to return nil after
			// writing "canceled" to the v1 row; with that row gone, returning nil
			// would have the v2 worker derive "completed" from a run that stopped
			// early — a partial fetch indistinguishable from a full one. The worker
			// derives terminal status from this return value, so a canceled run has
			// to say so here.
			if ctx.Err() != nil {
				slog.Info("metadata-candidate-fetch canceled",
					"opID", opID, "finalCount", finalCount, "totalBooks", totalBooks)
				return ctx.Err()
			}
			_ = progress.UpdateProgress(int(finalCount), totalBooks, "completed")
			slog.Info("metadata-candidate-fetch done",
				"opID", opID, "finalCount", finalCount, "totalBooks", totalBooks)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterMetadataCandidateFetchOp(reg)
	})
}
