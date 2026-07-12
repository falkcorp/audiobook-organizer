// file: internal/plugins/dedup/build_candidate_index.go
// version: 1.0.1
// guid: 87487445-8923-477d-866c-b8153fd7755b
// last-edited: 2026-07-12

// Package dedup — op dedup.build-candidate-status-index (INIT-2 T4).
//
// Backfills the dedup:s: status secondary index for every existing dedup
// candidate. New candidates (created/updated after the write-path
// maintenance landed in embedding_store.go's UpsertCandidateNew /
// UpdateCandidateStatus / DeleteCandidate) already get their index rows
// maintained inline in the same batch as the record write; this op catches
// rows written before that change shipped.
//
// Once this op completes, ListCandidates flips from the O(N) dedup:r: full
// scan to the O(k) dedup:s:<status>: indexed read whenever a status filter
// is set (see embedding_store.go ListCandidates's "Fallback semantics" doc
// comment). Until it completes, ListCandidates always fails open to the full
// scan — no candidate can ever be silently dropped by an incomplete index.
//
// Idempotent: re-running overwrites the same presence-only keys — safe to
// re-run any number of times, including after a partial/cancelled run.
//
// Usage:
//
//	POST /api/v1/operations  {"op":"dedup.build-candidate-status-index"}
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
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// candidateStatusIndexScanLimit bounds the single ListCandidates call this op
// uses to load every candidate for the full-table scan. Named (not a bare
// literal) per the same discipline as handler.go's bothUnmatchedScanLimit:
// this must stay >= the max candidate population (~387k pending pre-drain,
// plus every other status combined) or the scan silently truncates and the
// index would be built incomplete. 2_000_000 is a generous ceiling well
// above any plausible population — never lower it without checking the
// current candidate count first.
const candidateStatusIndexScanLimit = 2_000_000

// buildCandidateStatusIndexDef returns the OperationDef for the
// dedup.build-candidate-status-index op (INIT-2 T4).
func (p *Plugin) buildCandidateStatusIndexDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.build-candidate-status-index",
		Plugin:      "dedup",
		DisplayName: "Build candidate status secondary index",
		Description: "Backfills the dedup:s: status secondary index over dedup candidates, " +
			"enabling ListCandidates to serve status-filtered queries (e.g. the triage default " +
			"pending) from an O(k) indexed scan instead of the O(N) dedup:r: full-table scan. " +
			"Idempotent — safe to re-run.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.build-candidate-status-index",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runBuildCandidateStatusIndex,
	}
}

// runBuildCandidateStatusIndex implements dedup.build-candidate-status-index.
//
// Algorithm (mirrors lsh_index_build.go's shape — collect once, then fan the
// per-item writes out over a bounded worker pool per the CLAUDE.md
// whole-library-concurrency mandate; a plain for-range loop writing one
// index row per candidate at ~387k rows is exactly the shape that caused the
// 2026-07-05 single-core `dedup.full-scan` stall):
//
//  1. Load every candidate via ListCandidates with no status filter (always
//     the full dedup:r: scan path — see embedding_store.go's Fallback
//     semantics doc comment) up to candidateStatusIndexScanLimit.
//  2. Fan the candidates out over registry.RunItems (Concurrency=runtime.NumCPU()).
//     Each worker writes one dedup:s: row via WriteCandidateStatusIndexRow — a
//     single-key Pebble Set using candidateWriteOpts (NoSync), never a shared
//     batch or lock, so workers never serialize behind an fsync (PR #1855 /
//     issue #19 lesson — see candidateWriteOpts's doc comment).
//  3. On successful completion (no cancellation, no run error), set the
//     dedup_candidate_status_index_v1_done flag so ListCandidates switches to
//     the indexed read path. A partial/cancelled run leaves the flag unset,
//     so the fail-open full-scan path stays authoritative until a clean
//     re-run — re-running is always safe (idempotent presence-only writes).
func (p *Plugin) runBuildCandidateStatusIndex(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}

	logging.Info(ctx, "build-candidate-status-index: starting")
	loadProg := sdk.NewProgress(reporter, 0)
	loadProg.Start("Loading dedup candidates…")

	candidates, _, err := p.embeddingStore.ListCandidates(database.CandidateFilter{
		Limit: candidateStatusIndexScanLimit,
	})
	if err != nil {
		return fmt.Errorf("build-candidate-status-index: list candidates: %w", err)
	}
	total := len(candidates)
	if total == 0 {
		loadProg.Done("No candidates found — nothing to index")
		logging.Info(ctx, "build-candidate-status-index: no candidates, exiting")
		return nil
	}
	logging.Info(ctx, "build-candidate-status-index: loaded candidates", "total", total)

	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Indexing candidate status: 0 / %d", total))

	var mu sync.Mutex
	var written, skipped, errs int
	const logInterval = 15 * time.Second
	lastLog := time.Now()

	runErr := registry.RunItems(ctx, reporter, candidates, func(ctx context.Context, c database.DedupCandidate) error {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		if c.Status == "" {
			// A candidate can never legitimately have an empty status
			// (UpsertCandidateNew defaults it to "pending"); this only
			// guards against corrupt/legacy rows. Skip rather than error —
			// one bad row must never abort an otherwise-clean backfill.
			mu.Lock()
			skipped++
			mu.Unlock()
			return nil
		}
		if err := p.embeddingStore.WriteCandidateStatusIndexRow(c.ID, c.Status); err != nil {
			reporter.Logger().Error("build-candidate-status-index: write error",
				"candidate_id", c.ID, "error", err)
			mu.Lock()
			errs++
			mu.Unlock()
			return nil // don't abort the whole run for one bad row — idempotent, re-runnable.
		}

		mu.Lock()
		written++
		curWritten, curSkipped, curErrs := written, skipped, errs
		shouldLog := time.Since(lastLog) >= logInterval
		if shouldLog {
			lastLog = time.Now()
		}
		mu.Unlock()
		if shouldLog {
			logging.Info(ctx, "build-candidate-status-index: progress",
				"written", curWritten, "skipped", curSkipped, "errors", curErrs, "total", total)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, t int) string {
			mu.Lock()
			curWritten, curSkipped, curErrs := written, skipped, errs
			mu.Unlock()
			return fmt.Sprintf("Indexing candidate status: %d/%d (written=%d skipped=%d errors=%d)",
				i+1, t, curWritten, curSkipped, curErrs)
		},
	})
	if runErr != nil {
		logging.Info(ctx, "build-candidate-status-index: stopped", "written", written, "error", runErr)
		return runErr
	}

	prog.Finalize("writing completion flag…")

	if flagErr := p.embeddingStore.SetCandidateStatusIndexBuilt(); flagErr != nil {
		reporter.Logger().Error("build-candidate-status-index: failed to set completion flag", "error", flagErr)
		// Non-fatal: the index rows are written even if the flag write
		// fails. ListCandidates simply keeps using the full scan until a
		// subsequent run successfully sets the flag.
	}

	summary := fmt.Sprintf(
		"Candidate status index build complete — %d written, %d skipped (empty status), %d errors (of %d candidates)",
		written, skipped, errs, total)
	prog.Done(summary)
	logging.Info(ctx, "build-candidate-status-index: complete",
		"written", written, "skipped", skipped, "errors", errs, "total", total)
	return nil
}
