// file: internal/plugins/dedup/breakdown_backfill.go
// version: 1.0.0
// guid: ec0f5e9d-2f6d-485d-9f24-ad3d917d1834
// last-edited: 2026-07-17

// Package dedup — op dedup.breakdown-backfill.
//
// # Why this op exists
//
// 9,950 of ~10,362 exact-layer pending candidates on prod are pre-T015 rows
// with NO ScoreBreakdown. One data gap disables two tools at once:
//   - Engine.Rescore ("stored signal sets only") explicitly skips
//     nil-breakdown rows, so they can never be re-banded, and
//   - maintenance.dedup-exact-triage classifies every nil-breakdown row as
//     TriageClassUnknown, so the whole backlog lands in the manual-review
//     bucket.
//
// This op recomputes each nil-breakdown PENDING candidate's unified signal set
// and composed score with the engine's EXISTING scorer
// (Engine.ScorePairsForBook → the same collectors + unified.ComposeScore the
// operational scan uses via the shared collectPairSignals helper — no scorer
// fork) and persists it onto the candidate via UpdateCandidateScore.
//
// # Deliberate divergences from the operational scan
//
//  1. The work list is the existing pending candidates themselves — no new
//     pairs are ever created, and no lifecycle work (eligibility delete,
//     status change) is performed. Only ScoreBreakdown/Band/FormulaVersion
//     are written; Layer/Similarity/Status are untouched.
//  2. The `Band == ""` below-band skip is BYPASSED: a below-band breakdown is
//     still a breakdown — ANY breakdown beats none for triage/calibration.
//  3. The embedding cosine is NOT recomputable offline (it comes from the
//     stored candidate Similarity, mirroring the scan's embeddingMap). An
//     embedding-layer candidate with a nil Similarity is scored with the
//     remaining signals and the unavailable cosine is recorded in
//     missing_signal_counts.
//
// Pairs that produce zero signals stay reported-but-unscorable — never
// persisted, since an empty breakdown would be indistinguishable from
// "scored, no evidence".
//
// Dry-run (report only, 10 sample pairs logged) is the default.
// {"apply":true} writes.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/models"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// breakdownBackfillCandidateLimit mirrors internal/dedup's (unexported)
// wholeBacklogCandidateLimit: every whole-backlog op lists with the same 1M
// cap and WARNS when the listing hits it exactly (no silent caps).
const breakdownBackfillCandidateLimit = 1_000_000

// breakdownBackfillProgressEvery controls the "progress every N processed
// pairs" Info log.
const breakdownBackfillProgressEvery = 1000

// breakdownBackfillSampleLimit caps the sample pairs logged for a dry-run.
const breakdownBackfillSampleLimit = 10

// breakdownBackfillParams are the JSON parameters accepted by the op.
type breakdownBackfillParams struct {
	// Apply, if true, persists each recomputed breakdown via
	// UpdateCandidateScore. Default false (dry-run) — reports counts and 10
	// sample pairs, writes nothing.
	Apply bool `json:"apply"`
}

// candidateBackfillStore is the narrow store surface the op needs
// (database.EmbeddingStore satisfies it).
type candidateBackfillStore interface {
	ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error)
	UpdateCandidateScore(id int64, score *models.UnifiedDedupScore, band, formulaVersion string) error
}

// breakdownBackfillReport is the op's result payload, logged as JSON at the
// end of the run so a sandbox run can be measured mechanically.
type breakdownBackfillReport struct {
	Apply               bool           `json:"apply"`
	TotalPending        int            `json:"total_pending"`
	SkippedHasBreakdown int            `json:"skipped_has_breakdown"`
	Targets             int            `json:"targets"`
	Processed           int            `json:"processed"`
	Backfilled          int            `json:"backfilled"`
	WouldBackfill       int            `json:"would_backfill"`
	ZeroSignal          int            `json:"zero_signal"`
	SkippedNoBook       int            `json:"skipped_no_book"`
	ScoreErrs           int            `json:"score_errs"`
	UpdateErrs          int            `json:"update_errs"`
	MissingSignalCounts map[string]int `json:"missing_signal_counts"`
	SignalKindCounts    map[string]int `json:"signal_kind_counts"`
}

// breakdownBackfillDef returns the OperationDef for dedup.breakdown-backfill.
func (p *Plugin) breakdownBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.breakdown-backfill",
		Plugin:      "dedup",
		DisplayName: "Backfill ScoreBreakdowns onto pre-T015 pending candidates",
		Description: "Recomputes the unified signal set + composed score for every pending dedup " +
			"candidate with no ScoreBreakdown (pre-T015 rows) using the engine's existing scorer, " +
			"and persists it via UpdateCandidateScore so Engine.Rescore and " +
			"maintenance.dedup-exact-triage can act on the whole backlog. Writes ONLY " +
			"ScoreBreakdown/Band/FormulaVersion — never status, layer, or similarity. " +
			"Dry-run by default; apply=true writes.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.breakdown-backfill",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runBreakdownBackfill,
	}
}

// runBreakdownBackfill wires the plugin's engine + store into the testable
// runner.
func (p *Plugin) runBreakdownBackfill(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	return runBreakdownBackfillWith(ctx, p.engine, p.embeddingStore, rawParams, reporter)
}

// backfillRef is one nil-breakdown candidate to score, grouped under its
// EntityAID.
type backfillRef struct {
	candID       int64
	otherID      string // EntityBID (the pair's B side)
	embeddingCos *float64
	missingCos   bool // embedding-layer row whose stored cosine is gone
}

// backfillGroup bundles all target candidates sharing an A book so book A's
// per-book precompute (inside ScorePairsForBook) runs once per group. Groups
// are disjoint by A; each candidate row is keyed by a distinct candID, so
// parallel UpdateCandidateScore writes never collide.
type backfillGroup struct {
	aID  string
	refs []backfillRef
}

// runBreakdownBackfillWith is the testable core: it lists pending candidates,
// keeps the nil/empty-breakdown ones, groups them by EntityAID, scores each
// group through the engine's real scorer in a bounded worker pool
// (registry.RunItems, NumCPU workers — DB-read + CPU scoring over ~10K+
// pairs), and (apply=true) persists each recomputed breakdown via
// UpdateCandidateScore.
func runBreakdownBackfillWith(
	ctx context.Context,
	scorer pairScorer,
	store candidateBackfillStore,
	rawParams json.RawMessage,
	reporter sdk.Reporter,
) error {
	var params breakdownBackfillParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	log := reporter.Logger()

	// --- Load the whole pending backlog (book pairs only — author-pair
	// candidates have no Book rows for the scorer to load) ---
	_ = reporter.UpdateProgress(0, 3, "Loading pending candidates…")
	cands, _, err := store.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      breakdownBackfillCandidateLimit,
	})
	if err != nil {
		return fmt.Errorf("list pending candidates: %w", err)
	}
	// No silent caps: hitting the limit exactly means an unknown tail of
	// pending candidates was NOT listed — say so.
	if len(cands) == breakdownBackfillCandidateLimit {
		log.Warn("breakdown-backfill: candidate list truncated at limit — some pending candidates were not inspected",
			"limit", breakdownBackfillCandidateLimit)
	}
	if reporter.IsCanceled() {
		return context.Canceled
	}

	// --- Partition: already-scored rows are skipped, the rest are targets ---
	skippedHasBreakdown := 0
	groupByA := make(map[string]*backfillGroup)
	targets := 0
	missingSignalCounts := map[string]int{}
	for i := range cands {
		c := &cands[i]
		if c.ScoreBreakdown != nil && len(c.ScoreBreakdown.Signals) > 0 {
			skippedHasBreakdown++
			continue
		}
		ref := backfillRef{candID: c.ID, otherID: c.EntityBID}
		// Mirror the operational scan's embeddingMap: only an embedding-layer
		// row contributes a stored cosine. The cosine is the ONE signal input
		// that cannot be recomputed offline; when an embedding-layer row has
		// lost it, score with the remaining signals and record the gap.
		if c.Layer == "embedding" {
			if c.Similarity != nil {
				cos := *c.Similarity
				ref.embeddingCos = &cos
			} else {
				ref.missingCos = true
				missingSignalCounts["embedding_cosine"]++
			}
		}
		g, ok := groupByA[c.EntityAID]
		if !ok {
			g = &backfillGroup{aID: c.EntityAID}
			groupByA[c.EntityAID] = g
		}
		g.refs = append(g.refs, ref)
		targets++
	}
	groups := make([]backfillGroup, 0, len(groupByA))
	for _, g := range groupByA {
		groups = append(groups, *g)
	}

	log.Info("breakdown-backfill start",
		"apply", params.Apply,
		"total_pending", len(cands),
		"skipped_has_breakdown", skippedHasBreakdown,
		"targets", targets,
		"a_groups", len(groups),
		"workers", runtime.NumCPU(),
	)

	// --- Score + (apply) persist, sharded across disjoint A-groups ---
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Scoring %d nil-breakdown candidates across %d A-groups…", targets, len(groups)))

	var (
		mu               sync.Mutex
		processed        int
		backfilled       int
		wouldBackfill    int
		zeroSignal       int
		skippedNoBook    int
		scoreErrs        int
		updateErrs       int
		signalKindCounts = map[string]int{}
		samples          []string
	)

	err = registry.RunItems(ctx, reporter, groups, func(ctx context.Context, g backfillGroup) error {
		if reporter.IsCanceled() {
			return context.Canceled
		}

		inputs := make([]dedupengine.RescorePairInput, len(g.refs))
		for i, r := range g.refs {
			inputs[i] = dedupengine.RescorePairInput{OtherID: r.otherID, EmbeddingCos: r.embeddingCos}
		}

		results, sErr := scorer.ScorePairsForBook(ctx, g.aID, inputs)
		if sErr != nil {
			mu.Lock()
			scoreErrs += len(g.refs)
			processed += len(g.refs)
			mu.Unlock()
			log.Warn("breakdown-backfill: score error", "a_id", g.aID, "error", sErr)
			return nil // partial progress beats aborting the whole op
		}

		// Zip results back to refs by INDEX (ScorePairsForBook returns one
		// result per input, in order). A short return (nil A-book, mid-flight
		// cancellation) leaves the trailing refs unscored → skipped_no_book,
		// so the counts still reconcile against targets.
		if len(results) < len(g.refs) {
			mu.Lock()
			skippedNoBook += len(g.refs) - len(results)
			processed += len(g.refs) - len(results)
			mu.Unlock()
		}

		for i := 0; i < len(results); i++ {
			res := results[i]
			ref := g.refs[i]

			mu.Lock()
			processed++
			if processed%breakdownBackfillProgressEvery == 0 {
				log.Info("breakdown-backfill progress",
					"processed", processed,
					"targets", targets,
					"backfilled", backfilled,
					"would_backfill", wouldBackfill,
					"zero_signal", zeroSignal,
					"errors", scoreErrs+updateErrs,
				)
			}
			mu.Unlock()

			// Zero-signal pairs are unscorable — counted, never persisted.
			if res.NumSignals == 0 || res.Score == nil {
				mu.Lock()
				zeroSignal++
				mu.Unlock()
				continue
			}

			mu.Lock()
			for _, s := range res.Score.Signals {
				signalKindCounts[string(s.Kind)]++
			}
			if len(samples) < breakdownBackfillSampleLimit {
				samples = append(samples, fmt.Sprintf(
					"cand=%d pair=%s↔%s score=%.2f band=%q signals=%d",
					ref.candID, g.aID, ref.otherID, res.Score.Score, res.Score.Band, res.NumSignals))
			}
			mu.Unlock()

			if !params.Apply {
				mu.Lock()
				wouldBackfill++
				mu.Unlock()
				continue
			}

			// Persist ONLY ScoreBreakdown/Band/FormulaVersion (below-band
			// breakdowns included — the band gate is deliberately bypassed).
			if uErr := store.UpdateCandidateScore(ref.candID, res.Score, res.Score.Band, res.Score.Formula); uErr != nil {
				mu.Lock()
				updateErrs++
				mu.Unlock()
				log.Error("breakdown-backfill: update candidate score error",
					"candidate_id", ref.candID, "error", uErr)
				continue
			}
			mu.Lock()
			backfilled++
			mu.Unlock()
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, total int) string {
			return fmt.Sprintf("Scored %d/%d A-groups…", i+1, total)
		},
	})
	if err != nil {
		return err
	}

	// --- Report ---
	_ = reporter.UpdateProgress(2, 3, "Summarizing…")
	for _, s := range samples {
		log.Info("breakdown-backfill sample", "pair", s)
	}

	report := breakdownBackfillReport{
		Apply:               params.Apply,
		TotalPending:        len(cands),
		SkippedHasBreakdown: skippedHasBreakdown,
		Targets:             targets,
		Processed:           processed,
		Backfilled:          backfilled,
		WouldBackfill:       wouldBackfill,
		ZeroSignal:          zeroSignal,
		SkippedNoBook:       skippedNoBook,
		ScoreErrs:           scoreErrs,
		UpdateErrs:          updateErrs,
		MissingSignalCounts: missingSignalCounts,
		SignalKindCounts:    signalKindCounts,
	}
	reportJSON, _ := json.Marshal(report)
	_ = reporter.Log(slog.LevelInfo, "Breakdown-backfill report (JSON)", slog.String("report", string(reportJSON)))
	log.Info("breakdown-backfill complete",
		"apply", params.Apply,
		"total_pending", len(cands),
		"skipped_has_breakdown", skippedHasBreakdown,
		"targets", targets,
		"processed", processed,
		"backfilled", backfilled,
		"would_backfill", wouldBackfill,
		"zero_signal", zeroSignal,
		"skipped_no_book", skippedNoBook,
		"score_errs", scoreErrs,
		"update_errs", updateErrs,
		"missing_signal_counts", missingSignalCounts,
		"signal_kind_counts", signalKindCounts,
	)

	summary := fmt.Sprintf(
		"targets=%d processed=%d backfilled=%d would_backfill=%d skipped_has_breakdown=%d zero_signal=%d skipped_no_book=%d score_errs=%d update_errs=%d",
		targets, processed, backfilled, wouldBackfill, skippedHasBreakdown, zeroSignal, skippedNoBook, scoreErrs, updateErrs)
	for k, v := range missingSignalCounts {
		summary += fmt.Sprintf(" missing[%s]=%d", k, v)
	}

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, "Dry-run — "+summary+". Pass apply=true to persist ScoreBreakdowns.")
	} else {
		_ = reporter.UpdateProgress(3, 3, "Complete — "+summary+".")
	}
	return nil
}
