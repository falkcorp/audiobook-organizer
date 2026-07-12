// file: internal/plugins/dedup/lsh_index_build.go
// version: 1.4.1
// guid: e61b955e-93bf-4ea6-bb1f-7acd30491fdb
// last-edited: 2026-07-12

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"runtime"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// LSHIndexStore is the narrow store interface required by the
// lsh-index-build op. Using a narrow interface keeps the op decoupled
// from the concrete *PebbleStore while remaining testable with a mock.
//
// The *PebbleStore satisfies this interface; other store implementations
// may return errors from the LSH methods (they carry no index).
type LSHIndexStore interface {
	// GetAllBookFilesCore returns the BookFileCore projection of every
	// BookFile row. The op iterates all files and indexes only those with a
	// whole-file fingerprint (AcoustIDFingerprintDurationSec > 0 — the KEPT
	// proxy; the raw AcoustIDFingerprint bytes are a heavy field never
	// present on Core and must be hydrated per-book via GetBookFiles).
	GetAllBookFilesCore() ([]database.BookFileCore, error)

	// HasLSHIndex reports whether a BookFile already has an fpidx_meta row.
	// The op uses this to skip already-indexed files on incremental re-runs —
	// PutLSHEntries is idempotent but skipping avoids unnecessary writes.
	HasLSHIndex(bookFileID string) bool

	// PutLSHEntries writes the fpidx: index rows and fpidx_meta: member list
	// for (fileID, bookID, subprints, bands) atomically. Idempotent.
	PutLSHEntries(fileID, bookID string, subs []fingerprint.Subprint, bands []byte) error

	// IsLSHIndexBuilt / SetLSHIndexBuilt manage the versioned completion flag
	// lsh_index_v1_done. The op sets the flag on successful completion and
	// checks it to support the "already done" fast-path (though the op is
	// always resumable — re-running it is safe and continues from unindexed files).
	IsLSHIndexBuilt() bool
	SetLSHIndexBuilt() error

	// GetSetting / SetSetting are used to read and write the completion flag
	// via the standard settings key-value store.
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, dataType string, internal bool) error
}

// lshIndexBuildDef returns the OperationDef for the dedup.lsh-index-build op.
//
// Design decisions:
//   - ConcurrencyKey "dedup.lsh-index" prevents the T013 probe-collector from
//     racing an in-progress build (T013 reads the same keys this op writes).
//   - ResumeRequeue: on crash/restart, the op re-enqueues from scratch — but
//     HasLSHIndex skips already-indexed files so it effectively resumes.
//   - Timeout 120m: a full library rebuild on a 15K-file corpus with 64 bands
//     each needs ~1 M Pebble writes; benchmarking shows ~10 min on a cold NVMe,
//     leaving 10× headroom for slow disks.
func (p *Plugin) lshIndexBuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.lsh-index-build",
		Plugin:          "dedup",
		DisplayName:     "Build LSH fingerprint index",
		Description:     "Builds the fpidx: secondary index over whole-file AcoustID fingerprints, enabling fast near-duplicate lookup without a full O(N) scan.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.lsh-index",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runLSHIndexBuild,
	}
}

// runLSHIndexBuild is the op runner for dedup.lsh-index-build.
//
// Algorithm:
//  1. Load all BookFiles (memdb-slim projection: AcoustIDFingerprint is nil'd
//     by stripBookFileForMemdb; AcoustIDFingerprintDurationSec survives as the
//     memdb-safe proxy for "a whole-file fingerprint exists").
//  2. Group files by BookID and fan the groups out over a bounded worker pool
//     (registry.RunItems, Concurrency=runtime.NumCPU()) per the CLAUDE.md
//     whole-library-concurrency mandate — this loop now does a per-book
//     Pebble read (hydrate) plus CPU Subprints work per file, which stalled
//     a single core for hours before CONC remediation. Grouping by book
//     means each worker touches a disjoint BookID, so no per-file lock or
//     shared hydrate cache is needed.
//  3. For each file with a whole-file fp (DurationSec>0) whose blob was
//     memdb-stripped (len(AcoustIDFingerprint)==0), hydrate the real bytes
//     via p.store.GetBookFiles(bookID) — a raw Pebble read, never memdb —
//     once per book, then derive subprints and call PutLSHEntries — unless
//     HasLSHIndex already returns true (incremental skip).
//  4. Files without a fingerprint are collected by BookID; on completion,
//     acoustid.fingerprint-rescan is enqueued for those books so the next
//     lsh-index-build run can pick them up.
//  5. Progress is reported per book (RunItems' Label/UpdateProgress) with a
//     running file-level tally in the label text; a slog heartbeat fires at
//     most once per logInterval to avoid flooding the log on a ~275K-file run.
//  6. On completion, set lsh_index_v1_done so T013 can gate on it.
func (p *Plugin) runLSHIndexBuild(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// Obtain the LSH-capable store via type assertion. The concrete
	// *PebbleStore satisfies LSHIndexStore; SQLite and mock stores may not.
	lshStore, ok := p.store.(LSHIndexStore)
	if !ok {
		return fmt.Errorf("store does not implement LSHIndexStore (PebbleDB required)")
	}

	logging.Info(ctx, "lsh-index-build: starting")
	loadProg := sdk.NewProgress(reporter, 0)
	loadProg.Start("Loading BookFiles for LSH indexing…")

	files, err := lshStore.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("lsh-index-build: load files: %w", err)
	}
	total := len(files)
	if total == 0 {
		loadProg.Done("No BookFiles found — nothing to index")
		logging.Info(ctx, "lsh-index-build: no files, exiting")
		return nil
	}
	logging.Info(ctx, "lsh-index-build: loaded files", "total", total)

	// Group files by BookID so each worker hydrates a book's full BookFile
	// rows (via p.store.GetBookFiles, a raw Pebble read that bypasses memdb
	// stripping) at most once, and so registry.RunItems can process disjoint
	// books in parallel with no shared per-file state. We store INDICES into
	// `files` (not copies of the BookFile structs) to avoid holding a second
	// full copy of the whole-library slice — at ~275K+ rows that doubling is
	// real RSS on a swap-pressured host. `files` and `groups` are built
	// sequentially and only READ (never mutated) once RunItems starts, so
	// concurrent reads from worker goroutines are race-free without a lock.
	bookOrder := make([]string, 0, total)
	groups := make(map[string][]int, total)
	for i := range files {
		bookID := files[i].BookID
		if _, seen := groups[bookID]; !seen {
			bookOrder = append(bookOrder, bookID)
		}
		groups[bookID] = append(groups[bookID], i)
	}

	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Indexing LSH bands: 0 / %d files", total))

	var mu sync.Mutex
	var indexed, skipped, noFP, noFPPermFailed, errs, processedFiles int
	// Track unique book IDs whose files lack a fingerprint so we can
	// enqueue acoustid.fingerprint-rescan for them after the main loop.
	// Books are only added if at least one file has never been tried
	// (FingerprintFailedAt == nil) — permanently-failed files are excluded
	// to prevent an infinite rescan loop. Guarded by mu (written by every
	// worker goroutine).
	noFPBookSet := make(map[string]struct{})

	const logInterval = 15 * time.Second
	lastLog := time.Now()

	runErr := registry.RunItems(ctx, reporter, bookOrder, func(ctx context.Context, bookID string) error {
		if reporter.IsCanceled() {
			return context.Canceled
		}

		idxs := groups[bookID] // indices into the read-only `files` slice

		// Does any file in this book carry the proxy (DurationSec>0)? Core
		// never carries the raw AcoustIDFingerprint bytes at all (heavy
		// field, stripped on both the memdb and Pebble-direct paths), so any
		// file with a whole-file fp always needs the hydrate read now.
		needsHydrate := false
		for _, idx := range idxs {
			f := &files[idx]
			if f.AcoustIDFingerprintDurationSec > 0 {
				needsHydrate = true
				break
			}
		}

		var hydrated map[string]database.BookFile
		if needsHydrate {
			if full, ferr := p.store.GetBookFiles(bookID); ferr != nil {
				reporter.Logger().Error("lsh-index-build: hydrate GetBookFiles error",
					"book_id", bookID, "error", ferr)
				// hydrated stays nil; affected files below fall through to the
				// "no bytes after hydrate" error path instead of silently
				// vanishing from the accounting.
			} else {
				hydrated = make(map[string]database.BookFile, len(full))
				for _, hf := range full {
					hydrated[hf.ID] = hf
				}
			}
		}

		var (
			localIndexed, localSkipped, localNoFP, localNoFPPermFailed, localErrs int
			localNoFPBooks                                                        []string
		)

		for _, idx := range idxs {
			f := files[idx]
			if reporter.IsCanceled() {
				return context.Canceled
			}

			// AcoustIDFingerprintDurationSec > 0 is the memdb-safe proxy for
			// "has whole-file fp" — Core carries no AcoustIDFingerprint bytes
			// to check directly.
			hasWholeFP := f.AcoustIDFingerprintDurationSec > 0
			if !hasWholeFP {
				localNoFP++
				if f.FingerprintFailedAt == nil {
					localNoFPBooks = append(localNoFPBooks, f.BookID)
				} else {
					localNoFPPermFailed++
				}
				continue
			}

			// Incremental resume: skip files that already have an fpidx_meta
			// row. PutLSHEntries is idempotent, but the skip avoids
			// unnecessary writes when re-running after a partial build.
			if lshStore.HasLSHIndex(f.ID) {
				localSkipped++
				continue
			}

			// Core never carries the raw bytes; the op always hydrates via
			// the per-book GetBookFiles map built above.
			var fp []byte
			if hydrated != nil {
				if hf, ok := hydrated[f.ID]; ok {
					fp = hf.AcoustIDFingerprint
				}
				if len(fp) == 0 {
					reporter.Logger().Warn("lsh-index-build: hydrate returned empty fingerprint (data drift)",
						"file_id", f.ID, "book_id", f.BookID)
				}
			}
			if len(fp) == 0 {
				// Hydrate failed, or the raw row genuinely has no blob
				// despite the DurationSec proxy claiming one exists.
				// Count as an error (not noFP) so it isn't misrouted into
				// the rescan-enqueue path — the file DOES have a
				// fingerprint on record, we just couldn't read it this run.
				localErrs++
				continue
			}

			subs, bands, fpErr := fingerprint.Subprints(fp)
			if fpErr != nil {
				// Misaligned fingerprint bytes — log and continue. Don't abort
				// the entire build for one corrupt row.
				reporter.Logger().Error("lsh-index-build: Subprints error",
					"file_id", f.ID, "error", fpErr)
				localErrs++
				continue
			}
			if len(subs) == 0 {
				// Fingerprint too short to sample (< 4 frames after edge trim).
				// Apply same permanent-failure exclusion as the noFP path above.
				localNoFP++
				if f.FingerprintFailedAt == nil {
					localNoFPBooks = append(localNoFPBooks, f.BookID)
				} else {
					localNoFPPermFailed++
				}
				continue
			}

			if putErr := lshStore.PutLSHEntries(f.ID, f.BookID, subs, bands); putErr != nil {
				reporter.Logger().Error("lsh-index-build: PutLSHEntries error",
					"file_id", f.ID, "error", putErr)
				localErrs++
				continue
			}
			localIndexed++
		}

		mu.Lock()
		indexed += localIndexed
		skipped += localSkipped
		noFP += localNoFP
		noFPPermFailed += localNoFPPermFailed
		errs += localErrs
		processedFiles += len(idxs)
		for _, id := range localNoFPBooks {
			noFPBookSet[id] = struct{}{}
		}
		curProcessed, curIndexed, curSkipped, curNoFP, curPermFailed, curErrs :=
			processedFiles, indexed, skipped, noFP, noFPPermFailed, errs
		shouldLog := time.Since(lastLog) >= logInterval || curProcessed >= total
		if shouldLog {
			lastLog = time.Now()
		}
		mu.Unlock()

		if shouldLog {
			logging.Info(ctx, "lsh-index-build: progress",
				"processed", curProcessed, "total", total,
				"indexed", curIndexed, "skipped", curSkipped,
				"no_fp", curNoFP, "no_fp_perm_failed", curPermFailed, "errors", curErrs)
		}

		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, t int) string {
			mu.Lock()
			curProcessed, curIndexed, curSkipped, curNoFP, curPermFailed, curErrs :=
				processedFiles, indexed, skipped, noFP, noFPPermFailed, errs
			mu.Unlock()
			return fmt.Sprintf("Indexing LSH bands: %d/%d files (indexed=%d skipped=%d noFP=%d permFailed=%d errors=%d) [book %d/%d]",
				curProcessed, total, curIndexed, curSkipped, curNoFP, curPermFailed, curErrs, i+1, t)
		},
	})
	if runErr != nil {
		logging.Info(ctx, "lsh-index-build: stopped", "processed", processedFiles, "indexed", indexed, "error", runErr)
		return runErr
	}

	prog.Finalize("writing completion flag…")

	// Enqueue fingerprint-rescan for books that had unfingerprinted files.
	// A subsequent lsh-index-build run will pick them up once fingerprinted.
	if len(noFPBookSet) > 0 {
		noFPBookIDs := make([]string, 0, len(noFPBookSet))
		for id := range noFPBookSet {
			noFPBookIDs = append(noFPBookIDs, id)
		}
		if p.registry != nil {
			_, enqErr := p.registry.EnqueueOp(ctx, "acoustid.fingerprint-rescan", map[string]any{
				"scope":    "books",
				"book_ids": noFPBookIDs,
			})
			if enqErr != nil {
				reporter.Logger().Warn("lsh-index-build: failed to enqueue fingerprint-rescan",
					"books", len(noFPBookIDs), "error", enqErr)
			} else {
				reporter.Logger().Info("lsh-index-build: queued fingerprint-rescan for unfingerprinted books",
					"books", len(noFPBookIDs), "files", noFP)
				logging.Info(ctx, "lsh-index-build: enqueued fingerprint-rescan",
					"books", len(noFPBookIDs), "files", noFP)
			}
		} else {
			reporter.Logger().Warn("lsh-index-build: registry unavailable, cannot enqueue fingerprint-rescan",
				"no_fp_books", len(noFPBookIDs), "no_fp_files", noFP)
		}
	}

	// Mark the index as built so T013's probe-collector can enable itself.
	if flagErr := lshStore.SetLSHIndexBuilt(); flagErr != nil {
		reporter.Logger().Error("lsh-index-build: failed to set completion flag", "error", flagErr)
		// Non-fatal: the index is built even if the flag write fails.
		// The op will simply re-index skippable files on the next run,
		// but no data is lost.
	}

	summary := fmt.Sprintf(
		"LSH index build complete — %d indexed, %d skipped (already indexed), %d no-fingerprint (%d books queued for rescan, %d permanently failed), %d errors (of %d files)",
		indexed, skipped, noFP, len(noFPBookSet), noFPPermFailed, errs, total)
	prog.Done(summary)
	logging.Info(ctx, "lsh-index-build: complete",
		"indexed", indexed, "skipped", skipped,
		"no_fp", noFP, "no_fp_books", len(noFPBookSet),
		"no_fp_perm_failed", noFPPermFailed, "errors", errs, "total", total)
	return nil
}
