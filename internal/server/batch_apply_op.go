// file: internal/server/batch_apply_op.go
// version: 1.8.0
// guid: 8a3f21d7-6c04-4b91-a2e5-7d0f3b8c5194
// last-edited: 2026-09-09
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
	"slices"
	"sync"
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
	// BookIDs is the work REMAINING. A checkpoint rewrites it to the unfinished
	// tail, so a resumed run is shaped exactly like a smaller fresh run and
	// needs no special-case decoding anywhere.
	BookIDs []string `json:"book_ids"`
	// WriteBack mirrors the handler's body.WriteBack: when true (the default at
	// the handler) the applied metadata is also written into the audio files and
	// enqueued for iTunes sync. When false only the database rows change.
	WriteBack bool `json:"write_back"`
	// OriginalTotal is the size of the batch as the USER requested it, carried
	// across restarts for display only. Nothing branches on it. Without it a
	// resumed run reports "applied 120 of 352" for a job the user started as 699
	// books, which reads as the op having silently lost work.
	OriginalTotal int `json:"original_total,omitempty"`
}

// completed is how many books earlier attempts of this run already finished,
// derived rather than stored so it cannot drift out of step with BookIDs.
//
// Clamped at zero on purpose. OriginalTotal is absent on a fresh run and on
// params hand-written against /operations/v2, and a merge can legitimately grow
// BookIDs past a stale OriginalTotal; in every one of those cases the truthful
// answer is "nothing is known to be done yet", not a negative offset that would
// drive the progress bar backwards.
func (p batchApplyOpParams) completed() int {
	if p.OriginalTotal <= len(p.BookIDs) {
		return 0
	}
	return p.OriginalTotal - len(p.BookIDs)
}

// summarizeBatchApplyQueued reports the size of a queued batch apply, so a run
// waiting behind its own ConcurrencyKey says how many books it covers instead
// of only "Waiting to start…".
//
// It matters most for exactly this op. mergeBatchApplyQueuedParams keeps
// unioning newly approved books into the row while it waits, so the number both
// starts invisible and then grows; and the op's own Run reports
// UpdateProgress(priorDone, originalTotal) only once it starts, which for a
// four-hour serialized job can be a long time after the user asked.
//
// The counts deliberately match what Run reports on its first tick, so the
// numbers do not jump when the op finally starts.
func summarizeBatchApplyQueued(params json.RawMessage) (int, int, string) {
	p, ok := decodeQueuedParams[batchApplyOpParams](params)
	if !ok {
		return 0, 0, ""
	}
	return formatQueuedBookSummary(p.completed(), len(p.BookIDs), "apply")
}

// batchApplyCheckpointState builds the checkpoint payload for a contiguous
// completion watermark. It is a named function rather than an inline expression
// so a test can drive the REAL payload builder through RunItems instead of
// re-deriving the slice arithmetic and proving only that the copy agrees with
// itself.
//
// bookIDs is this attempt's work; watermark indexes into it.
//
// deferred is the books the write-back gate never let through. They sit BELOW
// the watermark — runOne returned nil for them so the loop could keep going —
// so the suffix alone would drop them, and nothing was applied for them, not
// even the database half. Re-appending them keeps the work owed across a
// restart. Order does not matter to a resumed run, so they go on the end.
func batchApplyCheckpointState(
	bookIDs []string,
	writeBack bool,
	originalTotal, watermark int,
	deferred []string,
) batchApplyOpParams {
	watermark = min(max(watermark, 0), len(bookIDs))
	remaining := bookIDs[watermark:]
	if len(deferred) > 0 {
		// Clone first: appending to bookIDs[watermark:] in place would write
		// past the subslice into the caller's own backing array.
		seen := make(map[string]struct{}, len(remaining)+len(deferred))
		for _, id := range remaining {
			seen[id] = struct{}{}
		}
		owed := slices.Clone(remaining)
		for _, id := range deferred {
			// A book can be deferred AND sit above the watermark when a gap
			// held the prefix back. Carrying it twice would apply it twice.
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			owed = append(owed, id)
		}
		remaining = owed
	}
	return batchApplyOpParams{
		BookIDs:       remaining,
		WriteBack:     writeBack,
		OriginalTotal: originalTotal,
	}
}

// batchApplyCheckpointEvery is how many completed books pass between checkpoint
// writes. The checkpoint is the contiguous-completion watermark, so a restart
// re-applies at most this many books. Re-applying is safe (the same cached
// candidate lands on the same book) but not free — it rewrites tags into the
// audio files — so this is deliberately small rather than the 200 a cheap
// probe-style backfill can afford.
const batchApplyCheckpointEvery = 10

// batchApplyMinCheckpointInterval arms the registry watchdog's uncheckpointed
// strike path, which is gated on ResumePolicy == ResumeRestart AND a nonzero
// MinCheckpointInterval (registry/watchdog.go). Leaving it zero would mean a
// hung run is never struck — the silent half of declaring a resume policy.
//
// Sized from the WORST case, not the average: PerItemTimeout is 3 minutes and
// WriteBackWorkers can be configured down to 1, so batchApplyCheckpointEvery
// books can legitimately take 30 minutes with no checkpoint in between. A
// tighter value would strike healthy long-running batches, which is how the
// strike path came to fire spuriously for every ResumeRestart job before it was
// sharpened.
const batchApplyMinCheckpointInterval = 45 * time.Minute

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
	// Books an earlier attempt already finished, inferred before BookIDs is
	// rewritten. Preserving it across the merge is what keeps a resumed-then-
	// extended run reporting absolute progress instead of appearing to restart.
	priorDone := current.completed()
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
	// Re-derive rather than carry OriginalTotal through: the merged run's total
	// is what an earlier attempt finished plus what is left to do. Setting it to
	// current.OriginalTotal would under-count the newly enqueued books and make
	// the run report completion before it had touched them.
	merged.OriginalTotal = priorDone + len(merged.BookIDs)
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
		ID:              "metadata.batch-apply-cached",
		Liveness:        opsregistry.LivenessRunItems,
		Plugin:          "metadata",
		DisplayName:     "Apply Cached Metadata",
		Description:     "Apply the highest-scored cached metadata candidate to each of a set of books, optionally writing tags back into the audio files.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeRestart,
		// Non-zero is REQUIRED alongside ResumeRestart, not optional decoration
		// — see the constant.
		MinCheckpointInterval: batchApplyMinCheckpointInterval,
		ConcurrencyKey:        "metadata.batch-apply-cached",
		MergeQueuedParams:     mergeBatchApplyQueuedParams,
		SummarizeQueued:       summarizeBatchApplyQueued,
		Permissions:           []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:          []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
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
			// bookIDs is what is LEFT. On a resumed run the registry has merged
			// the checkpoint back into params, so this is already the unfinished
			// tail and the run needs no resume branch of its own.
			bookIDs := p.BookIDs
			total := len(bookIDs)
			priorDone := p.completed()
			// Denominator the USER recognises, so a resumed run continues the
			// count instead of appearing to start a smaller job.
			originalTotal := priorDone + total
			if priorDone > 0 {
				reporter.Log(slog.LevelInfo, "resuming metadata apply",
					slog.Int("remaining", total),
					slog.Int("already_applied_by_earlier_attempts", priorDone),
					slog.Int("total", originalTotal))
			}
			_ = progress.UpdateProgress(priorDone, originalTotal, "starting metadata apply")

			var applied, noCandidates, decodeFailed, applyFailed, writeFailed atomic.Int64
			// gateDeferred holds the IDS — not a count — of books never
			// ATTEMPTED because the write-back gate stayed saturated past this
			// item's own timeout. Kept separate from writeFailed: nothing was
			// applied for these, not even the database half, so the summary must
			// not let them read as partial successes.
			//
			// It is a list rather than a counter because these books are still
			// OWED. A count could only be reported; the IDs can be retried at the
			// end of the run and carried in the checkpoint across a restart. Gate
			// saturation is a load condition affecting the whole batch, so
			// deferring is expected under load and must not quietly drop work.
			var gateDeferredMu sync.Mutex
			var gateDeferred []string
			noteGateDeferred := func(id string) {
				gateDeferredMu.Lock()
				gateDeferred = append(gateDeferred, id)
				gateDeferredMu.Unlock()
			}
			snapshotGateDeferred := func() []string {
				gateDeferredMu.Lock()
				defer gateDeferredMu.Unlock()
				return slices.Clone(gateDeferred)
			}
			takeGateDeferred := func() []string {
				gateDeferredMu.Lock()
				defer gateDeferredMu.Unlock()
				out := gateDeferred
				gateDeferred = nil
				return out
			}
			// skippedLocked counts BOOKS where at least one user-locked field was
			// left alone. It is not subtracted from applied: the unlocked fields
			// landed. It exists so the summary cannot read "applied 500 of 500"
			// while 200 of those kept a curated title the user would otherwise
			// go looking for in the op log.
			var skippedLocked atomic.Int64

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
						// ctx here is the PER-ITEM context (PerItemTimeout: 3m),
						// not the run context, so gateErr is DeadlineExceeded
						// whenever the write-back gate stays saturated for three
						// minutes. That is a load condition affecting the whole
						// batch, not a property of this book.
						//
						// Returning it would end the run: ErrModeCollect still
						// joins per-item errors and RunItems returns the join, so
						// ONE gate timeout out of 699 books marks the op failed
						// and terminal, and the summary naming every successful
						// apply never prints. Record it as a per-book outcome
						// instead — but as an ID, not a count, because unlike the
						// other non-apply reasons this book still needs applying.
						//
						// Genuine cancellation is unaffected: RunItems checks the
						// RUN context itself and stops the loop, so swallowing
						// this per-item deadline cannot make a cancelled batch
						// look like a completed one.
						noteGateDeferred(id)
						reporter.Log(slog.LevelWarn, "book not applied",
							slog.String("book_id", id),
							slog.String("reason", "write-back gate unavailable"),
							slog.String("error", gateErr.Error()))
						return nil
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
				if len(out.SkippedLocked) > 0 {
					skippedLocked.Add(1)
					reporter.Log(slog.LevelInfo, "applied; user-locked fields left unchanged",
						slog.String("book_id", id),
						slog.Any("skipped_locked", out.SkippedLocked))
				}
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
			//
			// CHECKPOINTING. runOne returns nil for every per-book outcome
			// (failures land in the counters), and RunItems only advances the
			// watermark on a nil return — so every book examined here leaves the
			// remaining set, whether it applied or was skipped. That is
			// deliberate and is the user's requirement verbatim: "don't let one
			// bad item constantly make it fail." A book with no cached candidate
			// would otherwise be retried on every restart, forever.
			//
			// The cost is accepted knowingly: a book whose write-back failed on a
			// transient fault (a NAS blip) also leaves the set and is not retried
			// by the resume. It is not lost — the database apply is durable and
			// the failure is counted and logged as writeFailed — but recovering
			// the file write needs a fresh apply, not this checkpoint. Note this
			// inverts RunItems' documented default rationale ("a FAILED item never
			// advances the watermark"), which is why it is spelled out here rather
			// than left to read as an oversight.
			runErr := opsregistry.RunItems(ctx, reporter, bookIDs, runOne, opsregistry.RunItemsOptions{
				Concurrency:    writeBackWorkers(),
				PerItemTimeout: 3 * time.Minute,
				ErrMode:        opsregistry.ErrModeCollect,
				Label:          func(int, int) string { return "applying cached metadata" },
				// Absolute position in the batch the user started, not in this
				// attempt's remainder.
				ProgressOffset:  priorDone,
				ProgressTotal:   originalTotal,
				CheckpointEvery: batchApplyCheckpointEvery,
				// The watermark is the contiguous completed PREFIX, so
				// bookIDs[watermark:] is exactly the work still owed — including
				// any book a faster worker finished out of order past a gap,
				// which is re-applied rather than skipped. Storing the remaining
				// IDs, rather than the watermark index the chapters-backfill
				// precedent stores, is what makes this safe against the set
				// changing between attempts: MergeQueuedParams can union new
				// books into a queued row, and an index into a list that has
				// since grown would silently skip whatever sorted below it. That
				// is the 2026-08-21 production incident shape (registry/types.go)
				// — new books discarded while the run reported success.
				//
				// Every field is carried, deliberately, even though today's
				// merge would not strictly require it. Checkpoint JSON is
				// OVERLAID on the resumed params by mergeJSONParams, which is a
				// maps.Copy: a key absent from the overlay keeps the base
				// params' value rather than reverting to a zero value. So
				// omitting WriteBack would currently be survivable — but only by
				// accident of the base row still holding it, and only while no
				// `omitempty` sits on the field. Writing the full state keeps
				// the checkpoint self-describing and independent of that.
				//
				// The direction that DOES bite is the opposite one: an
				// `omitempty` on BookIDs would drop an empty remaining-set from
				// the overlay and let the base params' original 699 IDs show
				// through, re-applying the entire batch. That is why BookIDs
				// carries no omitempty and OriginalTotal does.
				CheckpointStateFn: func(_ context.Context, watermark int) error {
					return reporter.Checkpoint(batchApplyCheckpointState(
						bookIDs, p.WriteBack, originalTotal, watermark,
						snapshotGateDeferred()))
				},
			})
			// Return BEFORE the "complete" row, so a canceled batch does not report
			// success on its way out.
			//
			// runOne returns nil on EVERY path — every per-book outcome, including
			// a write-back gate timeout, lands in a counter instead. That is load
			// bearing rather than incidental: ErrModeCollect does not suppress
			// errors, it joins them and RunItems returns the join, so any non-nil
			// return from runOne would fail the whole op. With that invariant,
			// runErr is exactly the run context's cancellation error and nothing
			// else. If a future edit adds an error return to runOne, it must
			// either be a genuine whole-batch abort or go in a counter.
			if runErr != nil {
				return runErr
			}

			// Second pass over the books the gate deferred. Without it the
			// checkpoint carry above only helps a run that gets RESTARTED: a run
			// that finishes normally reaches terminal success, its checkpoint
			// becomes moot, and the deferred books are never applied by anything.
			//
			// This is not a generic retry loop and must not become one — it
			// exists because gate saturation is a property of the MOMENT, not of
			// the book, and the biggest consumer of that gate is this op's own
			// workers, which have now finished. So the second look is the one
			// most likely to succeed, and one is enough: a book deferred twice is
			// contending with something outside this batch, which another lap
			// here would not resolve.
			//
			// Same concurrency as the main pass, deliberately: the worst case is
			// that the retry takes as long as the portion of the batch it covers
			// would have, which is the cost of the work rather than an extra one.
			// No CheckpointEvery — a restart during this pass resumes from the
			// checkpoint the main pass already wrote, which still owes these
			// books because CheckpointStateFn folded them back in.
			if deferred := takeGateDeferred(); len(deferred) > 0 {
				reporter.Log(slog.LevelInfo, "retrying books the write-back gate deferred",
					slog.Int("count", len(deferred)))
				retryErr := opsregistry.RunItems(ctx, reporter, deferred, runOne, opsregistry.RunItemsOptions{
					Concurrency:    writeBackWorkers(),
					PerItemTimeout: 3 * time.Minute,
					ErrMode:        opsregistry.ErrModeCollect,
					Label:          func(int, int) string { return "retrying deferred books" },
					// Progress stays pinned at the batch's own end: these books
					// were already counted as examined by the main pass, so
					// letting this pass advance the bar would drive it past 100%.
					ProgressOffset: originalTotal,
					ProgressTotal:  originalTotal,
				})
				if retryErr != nil {
					return retryErr
				}
			}

			// The counters cover THIS attempt only; they are not carried across a
			// restart. So the resumed wording reports them against the books this
			// attempt examined and names the earlier work separately, rather than
			// printing "applied 120 of 699" — which would read as 579 failures.
			// Read AFTER the retry pass: whatever is still here was deferred
			// twice, so this counts books that genuinely need a fresh apply,
			// not books that merely waited once.
			stillDeferred := len(snapshotGateDeferred())
			summary := fmt.Sprintf(
				"applied %d of %d (no candidates %d, decode failed %d, apply failed %d, write-back failed %d, gate unavailable %d, kept user-locked fields on %d)",
				applied.Load(), total, noCandidates.Load(), decodeFailed.Load(),
				applyFailed.Load(), writeFailed.Load(), stillDeferred,
				skippedLocked.Load())
			if priorDone > 0 {
				// State the known ambiguity rather than implying a clean count.
				// The watermark is the contiguous completed PREFIX, so a resumed
				// attempt re-examines any book a faster worker finished past a
				// gap. Applying invalidates that book's cached candidates, so on
				// the second look it is indistinguishable from a book that never
				// had any and it lands in "no candidates". The number cannot be
				// recovered here; saying so beats a count that reads as failures.
				summary = fmt.Sprintf(
					"%s; %d more were completed by earlier attempts before a restart, %d in the batch overall"+
						" (some of this attempt's \"no candidates\" may be books an earlier attempt already applied)",
					summary, priorDone, originalTotal)
			}
			_ = progress.UpdateProgress(originalTotal, originalTotal, "complete: "+summary)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBatchApplyFromCacheOp(reg) })
}
