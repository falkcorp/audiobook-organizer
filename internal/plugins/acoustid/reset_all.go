// file: internal/plugins/acoustid/reset_all.go
// version: 1.13.0
// guid: f3b1e8c4-2d7a-4d62-aabb-1f1d6e2c4a01
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// resetAllKind is rule R3's native kind for acoustid.reset-all.
const resetAllKind = tasks.Kind("books.acoustid.reset-all")

// resetAllHelp is the definition's description, HEAD's and the native kind's
// Help (rule R5: verbatim).
const resetAllHelp = "Clears every stored AcoustID fingerprint segment, drops acoustid dedup candidates, wipes the LSH index, and re-enqueues a forced rescan."

// resetAllEnv is what the reset-all core takes from its shell (rules R2 and
// R10): reads through Store, every write (the batched wipe, the per-row clear,
// the candidate deletes) through Writer. Candidates is the candidate list, nil
// when the plugin has neither a reset candidate store nor an embedding store;
// the pass is then skipped as HEAD skipped it.
type resetAllEnv struct {
	kindEnv
	Store      kindStore
	Writer     kindWriter
	Candidates candidateReads
}

// resetAllResult is the native run's result: what the summary line reports.
// HEAD stored no result, so its shell drops it.
type resetAllResult struct {
	Cleared           int `json:"cleared"`
	CandidatesDeleted int `json:"candidates_deleted"`
	BusyRows          int `json:"busy_rows"`
	FailedBatches     int `json:"failed_batches"`
	LeftBehind        int `json:"left_behind"`
}

// resetAllSlowParallelism is the per-row fallback pool (rule R4): HEAD's
// Concurrency is runtime.NumCPU(). Each item hydrates and rewrites its own row.
func resetAllSlowParallelism() tasks.Parallelism { return tasks.CPU() }

// resetAllChunkParallelism is the candidate-delete pass (rule R4): HEAD omits
// Concurrency (one batch at a time under the store-wide candidate lock).
func resetAllChunkParallelism() tasks.Parallelism {
	return tasks.Sequential("each chunk is one batch under the store-wide candidate lock; a pool would only queue on it")
}

// resetAllDef registers acoustid.reset-all — the "nuke everything and start
// over" admin op for AcoustID fingerprint state. Use when stored fingerprints
// are suspected bad (e.g. sentinel "AQAAAA" pollution that pairs every book
// with a single anchor at similarity 1.0).
//
// The op:
//  1. Clears AcoustIDSeg0..6 on every BookFile (also drops the
//     book_file_acoustid: secondary index entries via UpdateBookFile).
//  2. Deletes all pending dedup candidates on the "acoustid" layer.
//  3. Enqueues a forced acoustid.fingerprint-rescan so the library
//     recomputes from scratch with the new write-time guards in place.
func (p *Plugin) resetAllDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.reset-all",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "acoustid",
		DisplayName:     "Reset all AcoustID fingerprints",
		Description:     resetAllHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh,
		ConcurrencyKey:  "acoustid.fingerprint",
		Cancellable:     true,
		Timeout:         2 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runResetAll,
	}
}

func (p *Plugin) runResetAll(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_, err := resetAllCore(ctx, p.headResetAllEnv(reporter))
	return err
}

// headResetAllEnv is HEAD's env: the plugin's store for reads, and a writer over
// the same store whose candidate deletes go to the reset candidate store (or,
// failing that, the embedding store), as HEAD chose. A plugin with no store
// hands the core a nil Store, which it refuses with HEAD's message.
func (p *Plugin) headResetAllEnv(reporter sdk.Reporter) resetAllEnv {
	env := resetAllEnv{kindEnv: headEnv(reporter, true)}
	if p.store == nil {
		return env
	}
	var candidates resetCandidateStore
	switch {
	case p.resetCandidates != nil:
		candidates = p.resetCandidates
	case p.embeddingStore != nil:
		candidates = p.embeddingStore
	}
	env.Store = p.store
	env.Writer = headKindWriter{store: p.store, cands: candidates}
	if candidates != nil {
		env.Candidates = candidates
	}
	return env
}

// resetAllCore is the body of runResetAll, shared by both shells.
func resetAllCore(ctx context.Context, env resetAllEnv) (resetAllResult, error) {
	if env.Store == nil {
		return resetAllResult{}, fmt.Errorf("database store not available")
	}
	log := env.Logger
	startedAt := time.Now()

	// Initial Progress with N=0 — we don't know the real N until we either
	// get the first callback from the Pebble bulk-clear or load the slow
	// path's file list. The helper degrades gracefully to a 2-frame bar.
	prog := newProgress(env.kindEnv, 0)
	prog.Start("Clearing fingerprints (batched)…")

	// cleared is an atomic: the slow-path RunItems pass below runs fn and the
	// Label closure on a worker pool.
	var cleared atomic.Int64
	// busyRows counts rows the bulk clear left alone because other writers kept
	// changing them (the slow path below has no such notion).
	busyRows := 0

	// Fast path: PebbleStore exposes a batched bulk-clear that fsyncs once
	// per ~2000 records instead of once per UpdateBookFile call — ~100×
	// faster than the per-row fallback below.
	// resolveFingerprintResetter, which walks the decorator chain: a store
	// can arrive wrapped in a decorator (the server installed the indexedStore
	// decorator in NewServer until 07-S3, and p.store then held the WRAPPER in
	// production). A bare p.store.(*database.PebbleStore)
	// fails against a wrapper and
	// silently takes the per-row fallback below -- ~100x slower, with no error
	// and nothing in the logs to say the fast path was skipped. See
	// database/store_capability.go, which documents two prod jobs degraded this
	// way for weeks.
	if pebble := env.Writer.FingerprintResetter(); pebble != nil {
		var totalN int
		c, t, clearErr := pebble.ClearAllAcoustIDFingerprints(ctx, 2000,
			func(processed, c, t int) {
				// Rebuild the Progress as soon as we know the real total.
				if totalN != t {
					totalN = t
					prog = newProgress(env.kindEnv, t)
					prog.Start("Clearing fingerprints (batched)…")
				}
				prog.StepN(processed,
					fmt.Sprintf("Clearing fingerprints %d/%d (cleared=%d)", processed, t, c))
			})
		if sweepErr, busy := errors.AsType[*database.BookFileSweepConflictError](clearErr); busy {
			// Every other row was cleared; these were left as stored because
			// other writers kept changing them, and the store kept the LSH index
			// so they stay findable. Dropping the poisoned candidates below is
			// safe either way (the rescan regenerates them), so carry on and say
			// so in the summary instead of failing a run that did almost all of
			// its work. Re-running clears the rest and wipes the index.
			busyRows = sweepErr.Rows
			log.Warn("acoustid reset-all: rows left uncleared because they kept changing; LSH index kept",
				"rows", busyRows, "cleared", c, "total", t)
			clearErr = nil
		}
		if clearErr != nil {
			return resetAllResult{}, fmt.Errorf("bulk clear fingerprints: %w", clearErr)
		}
		cleared.Store(int64(c))
		log.Info("acoustid reset-all: bulk clear done", "cleared", c, "total", t, "busy", busyRows, "elapsed", time.Since(startedAt).Round(time.Second))
	} else {
		// Per-row fallback (mock/sqlite tests). AcoustIDSeg0..6 are stripped
		// from BookFileCore entirely (heavy fingerprint-diagnostic fields),
		// so this path must hydrate each candidate's full row via
		// GetBookFileByID just to read them, then clear + write back THAT full
		// row — never a hand-built BookFile{} from Core fields. Only reached
		// in mock/sqlite tests (prod uses the bulk-clear fast path above),
		// so the extra per-row hydrate read is not a perf concern.
		files, err := env.Store.GetAllBookFilesCore()
		if err != nil {
			return resetAllResult{}, fmt.Errorf("load book files: %w", err)
		}
		total := len(files)
		log.Info("acoustid reset-all: clearing fingerprints (slow path)", "book_files", total)
		prog = newProgress(env.kindEnv, total)
		prog.Start("Clearing fingerprints (slow path)…")
		err = env.Items(ctx, resetAllSlowParallelism(), total, kindItemsOptions{
			// Each item hydrates and rewrites its own book_file row (per-row
			// lock in PebbleStore), so items are disjoint and the pool is
			// sized to the cores. Only the mock/sqlite fallback reaches here.
			Label: func(i, t int) string {
				return fmt.Sprintf("Clearing fingerprints %d/%d (cleared=%d)", i+1, t, cleared.Load())
			},
		}, func(_ context.Context, i int) error {
			f := files[i]
			target, herr := env.Store.GetBookFileByID(f.BookID, f.ID)
			if herr != nil {
				log.Warn("acoustid reset-all: hydrate failed", "id", f.ID, "err", herr)
				return nil
			}
			if target == nil {
				log.Warn("acoustid reset-all: hydrate: row not found", "id", f.ID)
				return nil
			}
			if target.AcoustIDSeg0 == "" && target.AcoustIDSeg1 == "" && target.AcoustIDSeg2 == "" &&
				target.AcoustIDSeg3 == "" && target.AcoustIDSeg4 == "" && target.AcoustIDSeg5 == "" &&
				target.AcoustIDSeg6 == "" {
				return nil
			}
			target.AcoustIDSeg0 = ""
			target.AcoustIDSeg1 = ""
			target.AcoustIDSeg2 = ""
			target.AcoustIDSeg3 = ""
			target.AcoustIDSeg4 = ""
			target.AcoustIDSeg5 = ""
			target.AcoustIDSeg6 = ""
			if err := env.Writer.UpdateBookFile(target.ID, target); err != nil {
				log.Warn("acoustid reset-all: update file failed", "id", f.ID, "err", err)
				return nil
			}
			cleared.Add(1)
			return nil
		})
		if err != nil {
			return resetAllResult{}, err
		}
	}

	// Drop acoustid-layer pending candidates so the dedup review doesn't
	// keep showing the 14K+ poisoned rows after the rescan starts.
	prog.Finalize("Dropping acoustid dedup candidates…")
	deleted := 0 // sequential: the batches below run one at a time
	failedBatches, leftBehind := 0, 0
	if env.Candidates != nil {
		var allCands []database.DedupCandidate
		offset := 0
		const pageSize = 500
		for {
			cands, _, lerr := env.Candidates.ListCandidates(database.CandidateFilter{
				Layer:  "acoustid",
				Limit:  pageSize,
				Offset: offset,
			})
			if lerr != nil {
				log.Warn("acoustid reset-all: list candidates failed", "err", lerr)
				break
			}
			if len(cands) == 0 {
				break
			}
			allCands = append(allCands, cands...)
			offset += len(cands)
		}

		// Drop in batches: EmbeddingStore.DeleteCandidate holds the store lock
		// for the whole call, so fanning single deletes across a pool only
		// queues them on that lock. DeleteCandidates takes the lock and
		// commits once per chunk, which is the actual saving.
		// A pinned manual candidate keeps its acoustid layer: resetting
		// fingerprints does not withdraw a human's review request. It is
		// filtered here and again, under the lock, inside DeleteCandidates.
		var ids []int64
		for _, c := range allCands {
			if !database.IsManualCandidate(c) {
				ids = append(ids, c.ID)
			}
		}
		const deleteChunk = 500
		var chunks [][]int64
		for start := 0; start < len(ids); start += deleteChunk {
			chunks = append(chunks, ids[start:min(start+deleteChunk, len(ids))])
		}
		if len(chunks) > 0 {
			// runitems:sequential each chunk is one batch under the store-wide candidate lock; a pool would only queue on it
			if derr := env.Items(ctx, resetAllChunkParallelism(), len(chunks), kindItemsOptions{
				Label: func(i, t int) string {
					return fmt.Sprintf("Dropping acoustid dedup candidates batch %d/%d (deleted=%d)", i+1, t, deleted)
				},
			}, func(_ context.Context, ci int) error {
				chunk := chunks[ci]
				n, err := env.Writer.DeleteCandidates(chunk)
				if err != nil {
					// A failed batch deletes nothing (one Pebble batch), so
					// all of its rows are still in the review queue: keep
					// going with the rest, and say so in the summary.
					failedBatches++
					leftBehind += len(chunk)
					log.Warn("acoustid reset-all: delete candidates failed", "chunk_size", len(chunk), "err", err)
					return nil
				}
				deleted += n
				return nil
			}); derr != nil {
				return resetAllResult{}, derr
			}
		}
	}

	log.Info("acoustid reset-all: complete",
		"cleared", cleared.Load(),
		"candidates_deleted", deleted,
		"candidate_batches_failed", failedBatches,
		"candidates_left_behind", leftBehind,
		"elapsed", time.Since(startedAt).Round(time.Second))

	summary := fmt.Sprintf("Reset complete: %d files cleared, %d candidates dropped, LSH index wiped (elapsed %s).",
		cleared.Load(), deleted, time.Since(startedAt).Round(time.Second))
	if busyRows > 0 {
		summary = fmt.Sprintf("Reset INCOMPLETE: %d files cleared, %d candidates dropped, but %d files kept changing while the reset ran and were left as stored, so the LSH index was NOT wiped (elapsed %s). Run the reset again to clear them and wipe the index. A rescan already queued behind this reset is unaffected: it replaces each file's own index entries as it rewrites that file.",
			cleared.Load(), deleted, busyRows, time.Since(startedAt).Round(time.Second))
	}
	if failedBatches > 0 {
		summary += fmt.Sprintf(" WARNING: %d candidate batch(es) failed and %d poisoned candidates were left in the review queue; re-run to drop them.",
			failedBatches, leftBehind)
	}
	result := resetAllResult{
		Cleared: int(cleared.Load()), CandidatesDeleted: deleted, BusyRows: busyRows,
		FailedBatches: failedBatches, LeftBehind: leftBehind,
	}
	if busyRows > 0 {
		prog.Done(summary)
		return result, nil
	}
	prog.Done(summary + " Now enqueue acoustid.fingerprint-rescan with scope=all force=true.")
	return result, nil
}

// resetCandidateStore is the part of EmbeddingStore the candidate pass uses.
type resetCandidateStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	DeleteCandidates(ids []int64) (int, error)
}

// fingerprintResetter is the batched whole-library AcoustID fingerprint wipe.
//
// Not on database.Store (compile-probed 2026-08-19), so a bare assertion fails
// through a store decorator (the Bleve indexedStore, until 07-S3) -- which is exactly how this op
// degraded to the one-row-at-a-time fallback in production. Named rather than
// resolved with database.AsPebbleStore so this package does not depend on the
// concrete type by name -- see
// docs/plans/2026-08-19-split-the-pebblestore-surface.md.
type fingerprintResetter interface {
	ClearAllAcoustIDFingerprints(ctx context.Context, batchSize int, progress func(processed, cleared, total int)) (int, int, error)
}

// resolveFingerprintResetter walks the decorator chain, returning nil on a
// backend without the batched wipe so the caller keeps its per-row fallback.
func resolveFingerprintResetter(s any) fingerprintResetter {
	if c, ok := database.AsCapability[fingerprintResetter](s); ok {
		return c
	}
	return nil
}
