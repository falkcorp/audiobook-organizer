// file: internal/server/metadata_candidate_op.go
// version: 2.0.0
// guid: 3f7e2c91-b4a0-4d8e-9c5f-1a6b7d8e0f23
// last-edited: 2026-05-11
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
// v2 OperationDef. The HTTP handler creates a v1 op record for backward
// compatibility, then enqueues this def. The Run func writes OperationResult
// rows under the v1 opID so that all existing readers work without changes.
func (s *Server) RegisterMetadataCandidateFetchOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "metadata.candidate-fetch",
		Liveness: opsregistry.LivenessManual,
		Plugin:          "metadata",
		DisplayName:     "Fetch Metadata Candidates",
		Description:     "Fetch and cache metadata candidates for a set of audiobooks (rate-limited, parallel). Results are stored in v1 OperationResult rows for review.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         8 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "metadata.candidate-fetch",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkGeneric},
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

			store := s.Store()
			mfs := s.metadataFetchService
			progress := registryProgressAdapter{r: reporter}
			totalBooks := p.TotalBooks
			if totalBooks == 0 {
				totalBooks = len(p.BookIDs)
			}
			opID := p.LegacyOpID

			// Transition v1 op record to running so that handleGetLatestMetadataFetch
			// can surface it and the dedup scan in handleBatchFetchCandidates sees it
			// as an active fetch. This mirrors what the legacy v1 queue did automatically.
			_ = store.UpdateOperationStatus(opID, "running", p.AlreadyDone, totalBooks,
				fmt.Sprintf("starting: %d books to fetch", len(p.BookIDs)))
			_ = progress.UpdateProgress(p.AlreadyDone, totalBooks, fmt.Sprintf("starting: %d books to fetch", len(p.BookIDs)))

			// Rate limiter: 10 requests per second globally across all workers.
			limiter := rate.NewLimiter(rate.Limit(10), 1)

			workCh := make(chan string, len(p.BookIDs))
			for _, id := range p.BookIDs {
				workCh <- id
			}
			close(workCh)

			var completed int64 = int64(p.AlreadyDone)
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
			// Transition v1 op record to its terminal state.
			finalStatus := "completed"
			if ctx.Err() != nil {
				finalStatus = "canceled"
			}
			_ = store.UpdateOperationStatus(opID, finalStatus, int(finalCount), totalBooks, finalStatus)
			_ = progress.UpdateProgress(int(finalCount), totalBooks, "completed")
			slog.Info("metadata-candidate-fetch done — / books, status", "opID", opID, "finalCount", finalCount, "totalBooks", totalBooks, "finalStatus", finalStatus)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterMetadataCandidateFetchOp(reg)
	})
}
