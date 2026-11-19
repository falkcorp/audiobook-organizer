// file: internal/plugins/dedup/build_candidate_index.go
// version: 1.2.0
// guid: 87487445-8923-477d-866c-b8153fd7755b
// last-edited: 2026-11-18

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
	"sync"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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
		ID:              "dedup.build-candidate-status-index",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "dedup",
		DisplayName:     "Build candidate status secondary index",
		Description:     buildCandidateStatusIndexHelp,
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

// buildCandidateStatusIndexKind is rule R3's native kind for
// dedup.build-candidate-status-index.
const buildCandidateStatusIndexKind = tasks.Kind("books.dedup.build-candidate-status-index")

// buildCandidateStatusIndexHelp is HEAD's Description and the native Help.
const buildCandidateStatusIndexHelp = "Backfills the dedup:s: status secondary index over dedup candidates, " +
	"enabling ListCandidates to serve status-filtered queries (e.g. the triage default " +
	"pending) from an O(k) indexed scan instead of the O(N) dedup:r: full-table scan. " +
	"Idempotent — safe to re-run."

// buildCandidateStatusIndexStore is the one read the core makes.
type buildCandidateStatusIndexStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
}

// buildCandidateStatusIndexWriter is the writes the core makes: one presence-only
// index row per candidate and the completion flag.
type buildCandidateStatusIndexWriter interface {
	WriteCandidateStatusIndexRow(id int64, status string) error
	SetCandidateStatusIndexBuilt() error
}

// buildCandidateStatusIndexEnv is what the core takes from its shell.
type buildCandidateStatusIndexEnv struct {
	kindEnv
	Store  buildCandidateStatusIndexStore
	Writer buildCandidateStatusIndexWriter
}

// buildCandidateStatusIndexResult is the native run's result. HEAD stored none.
type buildCandidateStatusIndexResult struct {
	Total   int `json:"total"`
	Written int `json:"written"`
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
}

// buildCandidateStatusIndexCore is the body of runBuildCandidateStatusIndex,
// shared by both shells. Its per-item Label reads running counters, which the
// native pool cannot carry (the kernel's label is pure), so it is passed as
// Label: HEAD's pool shows it, the native run drops it.
func buildCandidateStatusIndexCore(ctx context.Context, env buildCandidateStatusIndexEnv) (buildCandidateStatusIndexResult, error) {
	logger.InfoCtx(ctx, "build-candidate-status-index: starting")
	loadProg := newProgress(env.kindEnv, 0)
	loadProg.Start("Loading dedup candidates…")

	candidates, _, err := env.Store.ListCandidates(database.CandidateFilter{
		Limit: candidateStatusIndexScanLimit,
	})
	if err != nil {
		return buildCandidateStatusIndexResult{}, fmt.Errorf("build-candidate-status-index: list candidates: %w", err)
	}
	total := len(candidates)
	if total == 0 {
		loadProg.Done("No candidates found — nothing to index")
		logger.InfoCtx(ctx, "build-candidate-status-index: no candidates, exiting")
		return buildCandidateStatusIndexResult{}, nil
	}
	logger.InfoCtx(ctx, "build-candidate-status-index: loaded candidates", "total", total)

	prog := newProgress(env.kindEnv, total)
	prog.Start(fmt.Sprintf("Indexing candidate status: 0 / %d", total))

	var mu sync.Mutex
	var written, skipped, errs int
	const logInterval = 15 * time.Second
	lastLog := time.Now()

	runErr := env.Items(ctx, tasks.CPU(), total, kindItemsOptions{
		Label: func(i, t int) string {
			mu.Lock()
			curWritten, curSkipped, curErrs := written, skipped, errs
			mu.Unlock()
			return fmt.Sprintf("Indexing candidate status: %d/%d (written=%d skipped=%d errors=%d)",
				i+1, t, curWritten, curSkipped, curErrs)
		},
	}, func(ctx context.Context, i int) error {
		c := candidates[i]
		if env.Canceled() {
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
		if err := env.Writer.WriteCandidateStatusIndexRow(c.ID, c.Status); err != nil {
			env.Logger.Error("build-candidate-status-index: write error",
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
			logger.InfoCtx(ctx, "build-candidate-status-index: progress",
				"written", curWritten, "skipped", curSkipped, "errors", curErrs, "total", total)
		}
		return nil
	})
	if runErr != nil {
		logger.InfoCtx(ctx, "build-candidate-status-index: stopped", "written", written, "error", runErr)
		return buildCandidateStatusIndexResult{}, runErr
	}

	prog.Finalize("writing completion flag…")

	if flagErr := env.Writer.SetCandidateStatusIndexBuilt(); flagErr != nil {
		env.Logger.Error("build-candidate-status-index: failed to set completion flag", "error", flagErr)
		// Non-fatal: the index rows are written even if the flag write
		// fails. ListCandidates simply keeps using the full scan until a
		// subsequent run successfully sets the flag.
	}

	summary := fmt.Sprintf(
		"Candidate status index build complete — %d written, %d skipped (empty status), %d errors (of %d candidates)",
		written, skipped, errs, total)
	prog.Done(summary)
	logger.InfoCtx(ctx, "build-candidate-status-index: complete",
		"written", written, "skipped", skipped, "errors", errs, "total", total)
	return buildCandidateStatusIndexResult{Total: total, Written: written, Skipped: skipped, Errors: errs}, nil
}

// runBuildCandidateStatusIndex is the v2 shell: HEAD's Run over
// buildCandidateStatusIndexCore. The embedding store is both the read store and
// the writer, as at HEAD.
func (p *Plugin) runBuildCandidateStatusIndex(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	_, err := buildCandidateStatusIndexCore(ctx, buildCandidateStatusIndexEnv{
		kindEnv: headEnv(reporter, true),
		Store:   p.embeddingStore,
		Writer:  p.embeddingStore,
	})
	return err
}
