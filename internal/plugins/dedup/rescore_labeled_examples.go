// file: internal/plugins/dedup/rescore_labeled_examples.go
// version: 1.1.0
// guid: 3e9c1a70-5d84-4b62-8f01-6a2d7c0e9b53
// last-edited: 2026-07-13

// Package dedup — op dedup.rescore-labeled-examples.
//
// # Why this op exists
//
// dedup.calibrate-composite needs >=500 SCORED pairs per class, where a pair is
// "scored" only if its LabeledExample carries a parseable ScoreBreakdown with
// >=1 signal. On prod, 2,069 of 2,303 labeled pairs have no usable breakdown
// anywhere, because:
//   - the operational unified scan drops below-band pairs before persisting
//     (`if composed.Band == "" { continue }`), and not_dup pairs are
//     disproportionately low-scoring, and
//   - labeling a pair not_dup DISMISSES its candidate, so it leaves the pending
//     set and is never re-scored; its LabeledExample keeps a stale/nil
//     ScoreBreakdown forever. Joining back to the candidate recovers nothing
//     because the dismissed candidate is pruned/breakdown-less.
//
// This op recomputes each labeled pair's ScoreBreakdown with the engine's EXISTING
// scorer (Engine.ScorePairsForBook → the same collectors + unified.ComposeScore
// the operational scan uses — no scorer fork) and persists it onto the
// LabeledExample, where calibrate-composite's PRIMARY read looks.
//
// # Two deliberate divergences from the operational scan
//
//  1. The labeled pairs (including dismissed ones, which are in no candidate list)
//     are INJECTED as the work list.
//  2. The `Band == ""` skip is BYPASSED — below-band labeled pairs get their
//     breakdown persisted. Those negatives ARE the calibration signal.
//
// (Pairs that produce zero signals stay reported-but-unscorable — never persisted,
// since a zero-signal breakdown is what the calibrator already treats as missing.)
//
// # Data safety
//
// Persistence is a NARROW read-modify-write: GetLabeledExample → set ONLY Score,
// ScoreBreakdown, Band → UpsertLabeledExample. Label, LabelSource (esp. "human"),
// LabelReason, DecidedAt and every other field are left untouched. If the example
// vanished between listing and writing, the op SKIPS it — it never creates a row.
//
// Dry-run (report only) is the default. {"apply":true} writes.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// rescoreLabeledExamplesParams are the JSON parameters accepted by the op.
type rescoreLabeledExamplesParams struct {
	// Apply, if true, narrow-writes each recomputed ScoreBreakdown onto its
	// LabeledExample. Default false (dry-run) — reports counts, writes nothing.
	Apply bool `json:"apply"`
}

// pairScorer is the narrow engine surface the op depends on. Abstracted so the
// runner can be unit-tested with a deterministic fake in place of a real Engine.
type pairScorer interface {
	ScorePairsForBook(ctx context.Context, aID string, inputs []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error)
}

// labeledExampleStore is the narrow store surface the op needs for the
// read-modify-write persist (database.EmbeddingStore satisfies it).
type labeledExampleStore interface {
	ListLabeledExamples(f database.LabeledExampleFilter) ([]database.LabeledExample, error)
	GetLabeledExample(candidateID int64) (*database.LabeledExample, error)
	UpsertLabeledExample(ex database.LabeledExample) error
}

// rescoreLabeledExamplesDef returns the OperationDef for
// dedup.rescore-labeled-examples.
func (p *Plugin) rescoreLabeledExamplesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.rescore-labeled-examples",
		Plugin:      "dedup",
		DisplayName: "Rescore labeled examples (populate ScoreBreakdowns for calibration)",
		Description: "Recomputes each labeled dedup pair's ScoreBreakdown with the engine's " +
			"existing scorer and narrow-writes it onto the LabeledExample, INCLUDING below-band " +
			"and dismissed pairs, so dedup.calibrate-composite can meet its per-class coverage " +
			"floor. Dry-run by default; apply=true writes only Score/ScoreBreakdown/Band, never " +
			"the label fields.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.rescore-labeled-examples",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRescoreLabeledExamples,
	}
}

// runRescoreLabeledExamples wires the plugin's engine + store into the testable
// runner.
func (p *Plugin) runRescoreLabeledExamples(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	return runRescoreLabeledExamplesWith(ctx, p.engine, p.embeddingStore, rawParams, reporter)
}

// labeledRef is one labeled pair to rescore, grouped under its EntityAID.
type labeledRef struct {
	candidateID  int64
	otherID      string // EntityBID (the pair's B side)
	trueDup      bool   // true = true_dup, false = not_dup
	embeddingCos *float64
}

// aGroup bundles all labeled pairs sharing a canonical A book so book A's per-book
// precompute runs once per group. Groups are disjoint by A; each pair's own
// LabeledExample is keyed by a distinct candidateID, so parallel writes never
// collide.
type aGroup struct {
	aID  string
	refs []labeledRef
}

// runRescoreLabeledExamplesWith is the testable core: it lists labeled examples,
// groups them by canonical A, scores each group through the engine's real scorer
// in a bounded worker pool, and (apply=true) narrow-writes the recomputed
// breakdown onto each LabeledExample.
func runRescoreLabeledExamplesWith(
	ctx context.Context,
	scorer pairScorer,
	store labeledExampleStore,
	rawParams json.RawMessage,
	reporter sdk.Reporter,
) error {
	var params rescoreLabeledExamplesParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	log := reporter.Logger()
	log.Info("rescore-labeled-examples start", "apply", params.Apply)

	// --- Load the calibration classes (true_dup + not_dup, incl. dismissed) ---
	_ = reporter.UpdateProgress(0, 3, "Loading labeled examples…")
	trueDup, err := store.ListLabeledExamples(database.LabeledExampleFilter{Label: "true_dup", Limit: 1_000_000})
	if err != nil {
		return fmt.Errorf("list true_dup examples: %w", err)
	}
	notDup, err := store.ListLabeledExamples(database.LabeledExampleFilter{Label: "not_dup", Limit: 1_000_000})
	if err != nil {
		return fmt.Errorf("list not_dup examples: %w", err)
	}
	if reporter.IsCanceled() {
		return context.Canceled
	}

	// --- Group by canonical A book ---
	groupByA := make(map[string]*aGroup)
	addRef := func(ex database.LabeledExample, trueDupClass bool) {
		ref := labeledRef{candidateID: ex.CandidateID, otherID: ex.EntityBID, trueDup: trueDupClass}
		// Mirror the operational scan's embeddingMap: only an embedding-layer pair
		// contributes a stored cosine; every other layer has its own signal.
		if ex.Layer == "embedding" && ex.Similarity != nil {
			cos := *ex.Similarity
			ref.embeddingCos = &cos
		}
		g, ok := groupByA[ex.EntityAID]
		if !ok {
			g = &aGroup{aID: ex.EntityAID}
			groupByA[ex.EntityAID] = g
		}
		g.refs = append(g.refs, ref)
	}
	for i := range trueDup {
		addRef(trueDup[i], true)
	}
	for i := range notDup {
		addRef(notDup[i], false)
	}

	groups := make([]aGroup, 0, len(groupByA))
	for _, g := range groupByA {
		groups = append(groups, *g)
	}

	totalLabeled := len(trueDup) + len(notDup)
	log.Info("rescore-labeled-examples: loaded",
		"total_labeled", totalLabeled, "true_dup", len(trueDup), "not_dup", len(notDup),
		"a_groups", len(groups))

	// --- Score + (apply) persist, sharded across A-groups ---
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Scoring %d labeled pairs across %d A-groups…", totalLabeled, len(groups)))

	var (
		mu             sync.Mutex
		scoredTrue     int
		scoredNot      int
		zeroSignalTrue int
		zeroSignalNot  int
		wouldWrite     int
		wrote          int
		humanPreserved int
		scoreErrs      int
		getErrs        int
		upsertErrs     int
		missingAtWrite int
		skippedNoBook  int
	)

	err = registry.RunItems(ctx, reporter, groups, func(ctx context.Context, g aGroup) error {
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
			mu.Unlock()
			log.Warn("rescore-labeled-examples: score error", "a_id", g.aID, "error", sErr)
			return nil // partial progress beats aborting the whole op
		}

		// Zip results back to refs by INDEX (ScorePairsForBook returns one result
		// per input, in order). Keying by OtherID would silently collapse two
		// labeled rows for the same (A,B) pair — a candidate re-created under a new
		// candidateID leaves the old label row behind — dropping one and
		// double-counting the other. A short return (nil A-book, cancellation)
		// leaves the trailing refs unscored → skipped_no_book, so the counts still
		// reconcile against total_labeled.
		if len(results) < len(g.refs) {
			mu.Lock()
			skippedNoBook += len(g.refs) - len(results)
			mu.Unlock()
		}

		for i := 0; i < len(results); i++ {
			res := results[i]
			ref := g.refs[i]

			// Zero-signal pairs are unscorable — reported by class, never persisted.
			if res.NumSignals == 0 || res.Score == nil {
				mu.Lock()
				if ref.trueDup {
					zeroSignalTrue++
				} else {
					zeroSignalNot++
				}
				mu.Unlock()
				continue
			}

			mu.Lock()
			if ref.trueDup {
				scoredTrue++
			} else {
				scoredNot++
			}
			wouldWrite++
			mu.Unlock()

			if !params.Apply {
				continue
			}

			// --- NARROW read-modify-write (data safety) ---
			cur, gErr := store.GetLabeledExample(ref.candidateID)
			if gErr != nil {
				mu.Lock()
				getErrs++
				mu.Unlock()
				log.Warn("rescore-labeled-examples: get error", "candidate_id", ref.candidateID, "error", gErr)
				continue
			}
			if cur == nil {
				// Deleted between listing and writing — never re-create it.
				mu.Lock()
				missingAtWrite++
				mu.Unlock()
				continue
			}

			raw, mErr := json.Marshal(res.Score)
			if mErr != nil {
				mu.Lock()
				scoreErrs++
				mu.Unlock()
				log.Warn("rescore-labeled-examples: marshal error", "candidate_id", ref.candidateID, "error", mErr)
				continue
			}

			isHuman := cur.LabelSource == "human"
			// Set ONLY the score fields; leave Label/LabelSource/LabelReason/
			// DecidedAt and every other field exactly as they were.
			cur.Score = res.Score.Score
			cur.ScoreBreakdown = raw
			cur.Band = res.Score.Band

			if uErr := store.UpsertLabeledExample(*cur); uErr != nil {
				log.Error("rescore-labeled-examples: upsert error", "candidate_id", ref.candidateID, "error", uErr)
				mu.Lock()
				upsertErrs++
				mu.Unlock()
				continue
			}
			mu.Lock()
			wrote++
			if isHuman {
				humanPreserved++
			}
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

	// --- Report (zero_signal-by-class is the decision input for a follow-up) ---
	_ = reporter.UpdateProgress(2, 3, "Summarizing…")
	log.Info("rescore-labeled-examples report",
		"apply", params.Apply,
		"total_labeled", totalLabeled,
		"scored_true_dup", scoredTrue,
		"scored_not_dup", scoredNot,
		"zero_signal_true_dup", zeroSignalTrue,
		"zero_signal_not_dup", zeroSignalNot,
		"would_write", wouldWrite,
		"wrote", wrote,
		"human_labels_preserved", humanPreserved,
		"score_errs", scoreErrs,
		"get_errs", getErrs,
		"upsert_errs", upsertErrs,
		"missing_at_write", missingAtWrite,
		"skipped_no_book", skippedNoBook,
	)

	summary := fmt.Sprintf(
		"scored true_dup=%d not_dup=%d; zero_signal true_dup=%d not_dup=%d; would_write=%d wrote=%d human_preserved=%d upsert_errs=%d",
		scoredTrue, scoredNot, zeroSignalTrue, zeroSignalNot, wouldWrite, wrote, humanPreserved, upsertErrs)

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, "Dry-run — "+summary+". Pass apply=true to persist ScoreBreakdowns.")
	} else {
		_ = reporter.UpdateProgress(3, 3, "Complete — "+summary+".")
	}
	return nil
}
