// file: internal/plugins/dedup/calibrate_embedding_thresholds.go
// version: 1.1.0
// guid: 6f1d2e3a-4b5c-4d6e-8f90-1a2b3c4d5e6f
// last-edited: 2026-07-04

// Package dedup — op dedup.calibrate-embedding-thresholds (DEDUP-2/3).
//
// # Why this op exists
//
// The Layer-2 (embedding) book high/low cosine thresholds (0.95/0.85) and the
// confidence ramps were hand-calibrated for OpenAI text-embedding-3-large's
// cosine distribution. The corpus is mid-cutover to a local Ollama bge-m3 model
// (1024-dim) whose cosine distribution is different — the OpenAI-era thresholds
// have never been validated against it. If bge-m3's true-duplicate cosines
// cluster lower than 0.95, the high-confidence tier silently stops firing with
// no error, just missing candidates (recall collapse).
//
// This op is a READ-ONLY calibration harness. It scores the existing labeled
// gold dataset (true_dup / not_dup examples populated by dedup.dataset-backfill
// and dedup.mine-gold-labels) using whatever embeddings are actually stored for
// the target model, sweeps candidate cosine cut-points, and REPORTS the
// thresholds that hit a target precision. It NEVER writes the recommendation
// into config, never mutates candidates, and never triggers a re-embed or scan.
//
// # DEDUP-3 contamination guard
//
// database.CosineSimilarity returns 0 on a dimension mismatch, so mixing a
// 3072-dim OpenAI vector with a 1024-dim bge-m3 vector would silently produce
// cosine 0 and poison the sweep. The harness therefore compares only pairs
// where BOTH stored embeddings carry the target model's `.Model` tag and have
// equal vector length; any other pair is skipped and counted, never scored.
//
// # Deferred, owner-gated follow-up (NOT performed by this op)
//
// After the re-embed (dedup.reembed-embeddings) completes and an operator has
// reviewed this report and updated dedup.embedding_thresholds_by_model (the
// per-model override map added in DEDUP-2) accordingly, a dedup.full-scan must
// be run to regenerate embedding-layer candidates under the new thresholds.
// That step changes live matching behaviour on production data, so it is
// owner-gated and explicitly out of scope for this op — this harness only
// reports; it does not apply.
//
// Usage:
//
//	POST /api/v1/operations/v2  {"def_id":"dedup.calibrate-embedding-thresholds"}
//	POST /api/v1/operations/v2  {"def_id":"dedup.calibrate-embedding-thresholds",
//	                             "params":{"model":"bge-m3","target_precision":0.98}}
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Sweep-grid bounds for the candidate cosine cut-points. The grid is inclusive
// of both ends and stepped by sweepGridStep. These are the search space for the
// recommendation, NOT the recommendation itself.
const (
	sweepGridLo   = 0.80
	sweepGridHi   = 0.99
	sweepGridStep = 0.01

	// defaultTargetPrecisionHigh is the precision floor for the high band.
	// A high-tier embedding match should be a near-certain duplicate.
	defaultTargetPrecisionHigh = 0.98
	// defaultTargetPrecisionLow is the precision floor for the low/medium band.
	// The medium band feeds noisy-OR composition and LLM review rather than
	// auto-merge, so it tolerates more false positives in exchange for recall.
	// Rationale for a single looser default (0.90): the medium tier's whole job
	// is to surface plausible-but-uncertain pairs for a downstream check, so a
	// precision floor materially below the high band is intentional.
	defaultTargetPrecisionLow = 0.90

	// minSampleSizeForBestPrecision is the minimum (true_dup+not_dup) sample
	// size at/above a cut-point for that cut-point's precision to count toward
	// the "best precision achieved" diagnostic. Without this floor, a
	// high-cosine cut-point with a single lucky true_dup pair and zero
	// not_dup pairs would report a spurious 100% precision.
	minSampleSizeForBestPrecision = 5

	// notDupSampleLimit bounds the "highest-cosine not_dup" diagnostic sample
	// logged in the report — these are, by construction, the pairs actively
	// dragging precision down at high cut-points.
	notDupSampleLimit = 10
)

// calibrateEmbeddingThresholdsParams are the JSON parameters accepted by the op.
type calibrateEmbeddingThresholdsParams struct {
	// Model is the embedding model whose stored vectors + thresholds are
	// calibrated. Optional; defaults to the currently-configured embed client
	// model (engine.EmbeddingModel()).
	Model string `json:"model"`
	// TargetPrecision is the precision floor for the HIGH band. Optional;
	// defaults to 0.98.
	TargetPrecision float64 `json:"target_precision"`
	// TargetPrecisionLow is the precision floor for the LOW/medium band.
	// Optional; defaults to 0.90.
	TargetPrecisionLow float64 `json:"target_precision_low"`
}

// calibrateEmbeddingThresholdsDef returns the OperationDef for
// dedup.calibrate-embedding-thresholds.
func (p *Plugin) calibrateEmbeddingThresholdsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.calibrate-embedding-thresholds",
		Plugin:      "dedup",
		DisplayName: "Calibrate embedding thresholds (report only)",
		Description: "Read-only DEDUP-2/3 harness: scores the labeled gold dataset with the " +
			"stored embeddings for a target model, sweeps candidate cosine cut-points, and " +
			"reports precision-targeted high/low threshold recommendations. Writes nothing — " +
			"applying the recommendation and running full-scan is an owner-gated follow-up.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.calibrate-embedding-thresholds",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runCalibrateEmbeddingThresholds,
	}
}

// embeddingGetter is the narrow read-only accessor the calibration logic needs.
// *database.EmbeddingStore satisfies it; tests supply a map-backed fake.
type embeddingGetter interface {
	Get(entityType, entityID string) (*database.Embedding, error)
}

// calibrationPair is a single labeled cosine used by the threshold sweep.
type calibrationPair struct {
	label  string // "true_dup" | "not_dup"
	cosine float64
	// entityAID/entityBID are carried through so diagnostics (e.g. the
	// highest-cosine not_dup sample) can identify which books a pair refers
	// to. They play no role in the sweep math itself.
	entityAID string
	entityBID string
}

// collectCalibrationPairs turns labeled examples into scored (label, cosine)
// pairs using only same-model, same-dimension embeddings for the target model.
//
// Skip rules (DEDUP-3 contamination guard) — a skipped pair is counted, never
// scored:
//   - either embedding missing (Get errors or returns nil) → skippedMissing
//   - either embedding's .Model != target model, or the two vectors differ in
//     length → skippedMismatch
//
// It is a pure function of its inputs (the getter is the only side channel), so
// the sweep and skip behaviour are unit-testable with synthetic data.
func collectCalibrationPairs(
	examples []database.LabeledExample,
	getter embeddingGetter,
	model string,
) (pairs []calibrationPair, skippedMissing, skippedMismatch int) {
	for i := range examples {
		ex := examples[i]
		a, aErr := getter.Get("book", ex.EntityAID)
		if aErr != nil || a == nil {
			skippedMissing++
			continue
		}
		b, bErr := getter.Get("book", ex.EntityBID)
		if bErr != nil || b == nil {
			skippedMissing++
			continue
		}
		if a.Model != model || b.Model != model {
			skippedMismatch++
			continue
		}
		if len(a.Vector) != len(b.Vector) {
			skippedMismatch++
			continue
		}
		cos := float64(database.CosineSimilarity(a.Vector, b.Vector))
		pairs = append(pairs, calibrationPair{
			label:     ex.Label,
			cosine:    cos,
			entityAID: ex.EntityAID,
			entityBID: ex.EntityBID,
		})
	}
	return pairs, skippedMissing, skippedMismatch
}

// bandRecommendation is the sweep result for one band.
type bandRecommendation struct {
	// Met is true when at least one cut-point reached the target precision.
	Met bool `json:"met"`
	// Threshold is the recommended cosine cut-point (valid only when Met).
	Threshold float64 `json:"threshold"`
	// Precision is the precision achieved at Threshold (valid only when Met).
	Precision float64 `json:"precision"`
	// Recall is true_dup pairs at/above Threshold ÷ total true_dup pairs
	// (valid only when Met) — reported so an operator can weigh the tradeoff.
	Recall float64 `json:"recall"`

	// BestPrecisionAchieved is the highest precision reached at any cut-point
	// in the sweep range that had at least minSampleSizeForBestPrecision
	// pairs at/above it. Populated even when Met is false, so a miss reports
	// "how close we got" instead of a bare boolean. Valid only when
	// BestPrecisionSampleSize > 0 — a zero sample size means no cut-point in
	// the sweep range had enough samples to be trustworthy.
	BestPrecisionAchieved float64 `json:"best_precision_achieved"`
	// BestPrecisionThreshold is the cut-point at which BestPrecisionAchieved
	// was observed. Valid only when BestPrecisionSampleSize > 0.
	BestPrecisionThreshold float64 `json:"best_precision_threshold"`
	// BestPrecisionSampleSize is the (true_dup+not_dup) sample size at/above
	// BestPrecisionThreshold. Zero means every cut-point in the sweep range
	// had fewer than minSampleSizeForBestPrecision pairs at/above it, so no
	// best-achieved figure could be trusted.
	BestPrecisionSampleSize int `json:"best_precision_sample_size"`
}

// sweepThreshold finds the LOWEST cut-point in [sweepGridLo, sweepGridHi]
// (stepped by sweepGridStep) whose precision >= targetPrecision, maximising
// recall subject to the precision floor. Precision at cut-point t is
// (#true_dup with cos>=t) ÷ (#(true_dup+not_dup) with cos>=t); a cut-point with
// no pairs at/above it is not eligible (precision undefined). If no cut-point
// reaches the target, the returned recommendation has Met=false — the caller
// must report that explicitly rather than fabricating a value. Even on a
// miss, the recommendation still carries BestPrecision* fields describing the
// closest the sweep got (see minSampleSizeForBestPrecision), so a target-miss
// report shows "how close" rather than a bare boolean.
func sweepThreshold(pairs []calibrationPair, targetPrecision float64) bandRecommendation {
	totalTrue := 0
	for _, pr := range pairs {
		if pr.label == "true_dup" {
			totalTrue++
		}
	}

	var bestPrecision float64
	var bestThreshold float64
	var bestSampleSize int

	// Iterate cut-points ascending so the FIRST meeting the target is the
	// lowest (max recall). Use integer stepping to avoid float accumulation.
	span := sweepGridHi - sweepGridLo
	steps := int(span/sweepGridStep + 0.5) //nolint:gocritic // runtime float→int, not a const conversion
	for i := 0; i <= steps; i++ {
		t := sweepGridLo + float64(i)*sweepGridStep
		tp, fp := 0, 0
		for _, pr := range pairs {
			if pr.cosine < t {
				continue
			}
			switch pr.label {
			case "true_dup":
				tp++
			case "not_dup":
				fp++
			}
		}
		if tp+fp == 0 {
			continue // no pairs at/above this cut-point — precision undefined
		}
		precision := float64(tp) / float64(tp+fp)
		if tp+fp >= minSampleSizeForBestPrecision && precision > bestPrecision {
			bestPrecision = precision
			bestThreshold = t
			bestSampleSize = tp + fp
		}
		if precision >= targetPrecision {
			recall := 0.0
			if totalTrue > 0 {
				recall = float64(tp) / float64(totalTrue)
			}
			return bandRecommendation{
				Met: true, Threshold: t, Precision: precision, Recall: recall,
				BestPrecisionAchieved:   bestPrecision,
				BestPrecisionThreshold:  bestThreshold,
				BestPrecisionSampleSize: bestSampleSize,
			}
		}
	}
	rec := bandRecommendation{Met: false}
	if bestSampleSize > 0 {
		rec.BestPrecisionAchieved = bestPrecision
		rec.BestPrecisionThreshold = bestThreshold
		rec.BestPrecisionSampleSize = bestSampleSize
	}
	return rec
}

// notDupHighCosineSample returns up to limit not_dup-labeled pairs sorted
// descending by cosine — by construction, these are the pairs actively
// dragging precision down at high cut-points, useful for distinguishing
// "gold labels are wrong" from "the model genuinely can't separate this
// pair" (see file-level doc comment). Does not mutate pairs.
func notDupHighCosineSample(pairs []calibrationPair, limit int) []calibrationPair {
	var notDup []calibrationPair
	for _, pr := range pairs {
		if pr.label == "not_dup" {
			notDup = append(notDup, pr)
		}
	}
	sort.Slice(notDup, func(i, j int) bool { return notDup[i].cosine > notDup[j].cosine })
	if limit >= 0 && len(notDup) > limit {
		notDup = notDup[:limit]
	}
	return notDup
}

// runCalibrateEmbeddingThresholds implements the op. It is read-only: it reads
// the labeled dataset and the embedding store and reports; it never writes.
func (p *Plugin) runCalibrateEmbeddingThresholds(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}

	// --- Parse params ---
	var params calibrateEmbeddingThresholdsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	model := params.Model
	if model == "" {
		if p.engine == nil {
			return fmt.Errorf("model param empty and dedup engine not available to resolve default")
		}
		model = p.engine.EmbeddingModel()
	}
	if model == "" {
		return fmt.Errorf("could not resolve a target embedding model (no param, no wired embed client)")
	}
	targetHigh := params.TargetPrecision
	if targetHigh <= 0 {
		targetHigh = defaultTargetPrecisionHigh
	}
	targetLow := params.TargetPrecisionLow
	if targetLow <= 0 {
		targetLow = defaultTargetPrecisionLow
	}

	log := reporter.Logger()
	log.Info("calibrate-embedding-thresholds start",
		"model", model,
		"target_precision_high", targetHigh,
		"target_precision_low", targetLow)

	// --- Load labeled dataset (both classes) ---
	_ = reporter.UpdateProgress(0, 3, "Loading labeled examples…")
	trueDup, err := p.embeddingStore.ListLabeledExamples(database.LabeledExampleFilter{Label: "true_dup", Limit: 1_000_000})
	if err != nil {
		return fmt.Errorf("list true_dup examples: %w", err)
	}
	notDup, err := p.embeddingStore.ListLabeledExamples(database.LabeledExampleFilter{Label: "not_dup", Limit: 1_000_000})
	if err != nil {
		return fmt.Errorf("list not_dup examples: %w", err)
	}
	if reporter.IsCanceled() {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	examples := make([]database.LabeledExample, 0, len(trueDup)+len(notDup))
	examples = append(examples, trueDup...)
	examples = append(examples, notDup...)

	// --- Score pairs (same-model, same-dim only) ---
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Scoring %d labeled pairs…", len(examples)))
	pairs, skippedMissing, skippedMismatch := collectCalibrationPairs(examples, p.embeddingStore, model)

	var sampleTrue, sampleNot int
	for _, pr := range pairs {
		switch pr.label {
		case "true_dup":
			sampleTrue++
		case "not_dup":
			sampleNot++
		}
	}

	// --- Sweep both bands ---
	_ = reporter.UpdateProgress(2, 3, "Sweeping candidate thresholds…")
	high := sweepThreshold(pairs, targetHigh)
	low := sweepThreshold(pairs, targetLow)

	// --- Diagnostic sample: highest-cosine not_dup pairs ---
	// These are, by construction, the pairs actively dragging precision down
	// at high cut-points. Reported unconditionally (cheap, always useful) so
	// an operator can tell "gold labels are wrong" (a listed pair is actually
	// a duplicate) apart from "model ceiling" (the pair is genuinely
	// different but scores close) without re-running anything.
	notDupSample := notDupHighCosineSample(pairs, notDupSampleLimit)
	if len(notDupSample) > 0 {
		log.Info("calibrate-embedding-thresholds highest-cosine not_dup sample",
			"count", len(notDupSample), "limit", notDupSampleLimit)
		for i, pr := range notDupSample {
			var titleA, titleB string
			if p.store != nil {
				if bookA, err := p.store.GetBookByID(pr.entityAID); err == nil && bookA != nil {
					titleA = bookA.Title
				}
				if bookB, err := p.store.GetBookByID(pr.entityBID); err == nil && bookB != nil {
					titleB = bookB.Title
				}
			}
			log.Info("calibrate-embedding-thresholds not_dup sample pair",
				"rank", i+1,
				"cosine", pr.cosine,
				"entity_a_id", pr.entityAID,
				"entity_a_title", titleA,
				"entity_b_id", pr.entityBID,
				"entity_b_title", titleB)
		}
	}

	// --- Report (structured log fields — read-only, no result sink to write) ---
	fields := []any{
		"model", model,
		"sample_true_dup", sampleTrue,
		"sample_not_dup", sampleNot,
		"skipped_dimension_mismatch", skippedMismatch,
		"skipped_missing_embedding", skippedMissing,
		"target_precision_high", targetHigh,
		"target_precision_low", targetLow,
	}
	if high.Met {
		fields = append(fields,
			"recommended_high_threshold", high.Threshold,
			"recommended_high_precision", high.Precision,
			"recommended_high_recall", high.Recall)
	} else {
		fields = append(fields, "no_high_threshold_met_target", true)
		if high.BestPrecisionSampleSize > 0 {
			fields = append(fields,
				"high_best_precision_achieved", high.BestPrecisionAchieved,
				"high_best_precision_threshold", high.BestPrecisionThreshold,
				"high_best_precision_sample_size", high.BestPrecisionSampleSize)
		}
	}
	if low.Met {
		fields = append(fields,
			"recommended_low_threshold", low.Threshold,
			"recommended_low_precision", low.Precision,
			"recommended_low_recall", low.Recall)
	} else {
		fields = append(fields, "no_low_threshold_met_target", true)
		if low.BestPrecisionSampleSize > 0 {
			fields = append(fields,
				"low_best_precision_achieved", low.BestPrecisionAchieved,
				"low_best_precision_threshold", low.BestPrecisionThreshold,
				"low_best_precision_sample_size", low.BestPrecisionSampleSize)
		}
	}
	// no_threshold_met_target is set when NEITHER band reached its target, so a
	// consumer scanning for a single flag still sees the total-miss case.
	if !high.Met && !low.Met {
		fields = append(fields, "no_threshold_met_target", true)
	}
	log.Info("calibrate-embedding-thresholds report", fields...)

	summary := fmt.Sprintf(
		"model=%s sample_true=%d sample_not=%d skipped_dim=%d skipped_missing=%d high=%s low=%s",
		model, sampleTrue, sampleNot, skippedMismatch, skippedMissing,
		describeBand(high), describeBand(low),
	)
	log.Info("calibrate-embedding-thresholds complete", "summary", summary)
	_ = reporter.UpdateProgress(3, 3, "Calibration report complete — "+summary+
		". Recommendation is report-only; applying thresholds + full-scan is owner-gated.")
	return nil
}

// describeBand renders a band recommendation for the human-readable summary.
func describeBand(b bandRecommendation) string {
	if !b.Met {
		if b.BestPrecisionSampleSize > 0 {
			return fmt.Sprintf("target-not-met(best_p=%.3f@thr=%.2f,n=%d)",
				b.BestPrecisionAchieved, b.BestPrecisionThreshold, b.BestPrecisionSampleSize)
		}
		return "target-not-met"
	}
	return fmt.Sprintf("thr=%.2f(p=%.3f,r=%.3f)", b.Threshold, b.Precision, b.Recall)
}
