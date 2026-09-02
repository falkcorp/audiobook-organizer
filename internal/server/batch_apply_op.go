// file: internal/server/batch_apply_op.go
// version: 1.4.1
// guid: 8a3f21d7-6c04-4b91-a2e5-7d0f3b8c5194
// last-edited: 2026-09-02
//
// batch_apply_op registers the "metadata.batch-apply-cached" v2 OperationDef.
// The HTTP handler BatchApplyFromCache enqueues this and returns the op id
// immediately instead of holding the request open for the whole batch.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// errText renders an error for a log attribute, tolerating nil. The callers
// below log a reason on a branch that can be reached with err == nil (an empty
// candidate list is not an error), so a bare err.Error() would panic exactly on
// the "nothing was cached" case.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// batchApplyOpParams is the JSON params for the metadata.batch-apply-cached op.
type batchApplyOpParams struct {
	BookIDs []string `json:"book_ids"`
	// WriteBack mirrors the handler's body.WriteBack: when true (the default at
	// the handler) the applied metadata is also written into the audio files and
	// enqueued for iTunes sync. When false only the database rows change.
	WriteBack bool `json:"write_back"`
}

func mergeBatchApplyQueuedParams(existing, incoming json.RawMessage) (json.RawMessage, bool, error) {
	var current, next batchApplyOpParams
	if err := json.Unmarshal(existing, &current); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(incoming, &next); err != nil {
		return nil, false, err
	}
	if current.WriteBack != next.WriteBack {
		return nil, false, nil
	}
	seen := make(map[string]struct{}, len(current.BookIDs)+len(next.BookIDs))
	merged := batchApplyOpParams{WriteBack: current.WriteBack}
	for _, id := range append(current.BookIDs, next.BookIDs...) {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		merged.BookIDs = append(merged.BookIDs, id)
	}
	// Fail-safe cap (internal/applycap): two under-cap requests must not union
	// into one over-cap run. Declining the merge is safe — the registry falls
	// through to the ConcurrencyKey dedupe, the params byte-differ, and the
	// second request is queued to run on its own, where Run's gate applies.
	if !applycap.Fits(len(merged.BookIDs), config.AppConfig.BulkApplyMaxItems) {
		return nil, false, nil
	}
	raw, err := json.Marshal(merged)
	return raw, err == nil, err
}

// RegisterBatchApplyFromCacheOp registers the "metadata.batch-apply-cached" op.
//
// Why this exists at all: BatchApplyFromCache used to do the whole batch inline
// in the gin request goroutine. It was already parallel (errgroup at
// batchApplyConcurrency) and already pushed the file work to the I/O pool, so
// the problem was never a missing worker pool — it was the REQUEST DURATION. A
// 250-book apply measured 2m0s on production. Go's HTTP server does not kill a
// handler when the client disconnects, and ApplyMetadataCandidate takes no
// context, so the browser timed out, the UI reported "session expired, nothing
// was applied", and the server kept applying for another minute. The user was
// told the opposite of what happened.
//
// As an op it gets the three things the inline version could not have: a real
// ctx that cancels, a progress stream the UI can poll, and the registry
// watchdog. The handler now returns an op id in milliseconds.
func (s *Server) RegisterBatchApplyFromCacheOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:                "metadata.batch-apply-cached",
		Liveness:          opsregistry.LivenessRunItems,
		Plugin:            "metadata",
		DisplayName:       "Apply Cached Metadata",
		Description:       "Apply the highest-scored cached metadata candidate to each of a set of books, optionally writing tags back into the audio files.",
		DefaultPriority:   opsregistry.PriorityNormal,
		Cancellable:       true,
		Isolate:           false,
		Timeout:           4 * time.Hour,
		ResumePolicy:      opsregistry.ResumeDrop,
		ConcurrencyKey:    "metadata.batch-apply-cached",
		MergeQueuedParams: mergeBatchApplyQueuedParams,
		Permissions:       []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:      []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p batchApplyOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("batch-apply-cached: decode params: %w", err)
				}
			}

			// Fail-safe cap, checked HERE and not only at the HTTP handler: this
			// op can be dispatched directly through /operations/v2 with any
			// book_ids list, and a resumed/merged run never passed through the
			// handler. Refusal, not truncation — zero applies happen. It sits
			// before the dependency checks because it is pure params
			// validation, which also lets batch_apply_cap_test.go exercise it
			// on a zero-value Server.
			if err := applycap.Check("metadata.batch-apply-cached", len(p.BookIDs), config.AppConfig.BulkApplyMaxItems); err != nil {
				return err
			}

			svc := s.metadataFetchService
			if svc == nil {
				return fmt.Errorf("batch-apply-cached: metadata fetch service not initialized")
			}

			progress := registryProgressAdapter{r: reporter}
			bookIDs := p.BookIDs
			total := len(bookIDs)
			_ = progress.UpdateProgress(0, total, "starting metadata apply")

			var applied, noCandidates, decodeFailed, applyFailed, writeFailed atomic.Int64

			// itunes may be a typed nil (*itunesservice.WriteBackBatcher)(nil), which
			// is NOT == nil once boxed in an interface. Normalize to an untyped nil
			// so the guard inside applyCachedCandidateForBook actually fires.
			var itunes itunesEnqueuer
			if s.writeBackBatcher != nil {
				itunes = s.writeBackBatcher
			}

			// The file work runs inline in the op's own worker, NOT via
			// fileIOPool.Submit as the old handler did. That is deliberate and is
			// the whole point of the change: Submit returns immediately, so an op
			// using it would report 100% complete while tags were still being
			// written to disk — which defeats the status the op exists to provide.
			// Running it here makes the progress bar mean what it says, and brings
			// the work under the same path lock the batch-save op uses.
			//
			// Concurrency is writeBackWorkers() (disk/TagLib-bound), NOT the old
			// batchApplyConcurrency=4, because this loop now does the file I/O
			// rather than delegating it.
			runOne := func(ctx context.Context, id string) error {
				if p.WriteBack {
					releaseFileWrite, gateErr := writeBackFileGate.acquire(ctx)
					if gateErr != nil {
						return gateErr
					}
					defer releaseFileWrite()
				}
				out := applyCachedCandidateForBook(
					svc, s.Ops(), itunes, id, p.WriteBack, writeBackPathLocks.lock)

				if !out.Applied {
					switch out.Reason {
					case applySkipNoCachedCandidates:
						noCandidates.Add(1)
					case applySkipDecodeFailed:
						decodeFailed.Add(1)
					case applySkipApplyFailed:
						applyFailed.Add(1)
					}
					reporter.Log(slog.LevelWarn, "book not applied",
						slog.String("book_id", id),
						slog.String("reason", out.Reason),
						slog.String("error", errText(out.Err)))
					return nil
				}

				applied.Add(1)
				if out.WriteBackFailed {
					// Counted separately and logged, but NOT subtracted from
					// applied: the database change is real and durable. Reporting
					// it as unapplied would send someone re-applying work that
					// succeeded.
					writeFailed.Add(1)
					reporter.Log(slog.LevelWarn, "applied to database but write-back to files failed",
						slog.String("book_id", id),
						slog.String("error", errText(out.Err)))
				}
				return nil
			}

			// RunItems reports progress after EVERY item, which is what resets the
			// registry stuck-op watchdog. This def sets Timeout: 4h but no explicit
			// ProgressTimeout, so it inherits the 5-minute default. PerItemTimeout
			// is 3 minutes — deliberately BELOW that 5-minute watchdog, so a single
			// wedged book fails its own item and lets the loop report progress
			// again, rather than starving the watchdog and killing the whole op.
			// That is precisely how the UA-purge census died: no progress for
			// 5m18s against a 5m0s timeout.
			//
			// ErrModeCollect, NOT the default ErrModeFail: the loop this replaces
			// recorded every per-book failure as a skip and carried on, and
			// ErrModeFail would cancel the whole batch on the first bad book.
			//
			// The Label is deliberately COARSE and constant: reporter_db.go writes
			// one op_logs_v2 row per DISTINCT progress message, so a label carrying
			// the book id would write one DB row per book.
			runErr := opsregistry.RunItems(ctx, reporter, bookIDs, runOne, opsregistry.RunItemsOptions{
				Concurrency:    writeBackWorkers(),
				PerItemTimeout: 3 * time.Minute,
				ErrMode:        opsregistry.ErrModeCollect,
				Label:          func(int, int) string { return "applying cached metadata" },
			})
			// Return BEFORE the "complete" row, so a canceled batch does not report
			// success on its way out. runOne never returns a non-nil error (per-book
			// failures land in the counters), so runErr is exactly ctx.Err() on
			// cancellation and nil otherwise.
			if runErr != nil {
				return runErr
			}

			_ = progress.UpdateProgress(total, total, fmt.Sprintf(
				"complete: applied %d of %d (no candidates %d, decode failed %d, apply failed %d, write-back failed %d)",
				applied.Load(), total, noCandidates.Load(), decodeFailed.Load(),
				applyFailed.Load(), writeFailed.Load()))
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBatchApplyFromCacheOp(reg) })
}
