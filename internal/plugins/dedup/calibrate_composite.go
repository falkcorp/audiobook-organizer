// file: internal/plugins/dedup/calibrate_composite.go
// version: 1.1.0
// guid: 4c2f7a91-8d3b-4e6a-9f10-5b7c2d1e8a34
// last-edited: 2026-07-11

// Package dedup — op dedup.calibrate-composite (INIT-1 T5).
//
// # Why this op exists
//
// The sibling op dedup.calibrate-embedding-thresholds sweeps a SINGLE embedding
// cosine cut-point. Per the 2026-07-08 findings, ~47% of true_dup pairs score
// below cosine 0.98, so no single embedding cut-point can serve the
// high-confidence tier. The composite noisy-OR scorer (unified.ComposeScore) is
// the right calibration surface because it fuses every signal a pair carries.
// This op replays each labeled pair's stored ScoreBreakdown signal set through
// unified.ComposeScore under candidate configs and reports the band thresholds
// (and, advisory-only, per-signal confidence bounds) that hit a target precision.
//
// # Two calibration surfaces, only one of them persistable
//
// unified.ComposeScore reads Signal.Confidence DIRECTLY and ignores the cfg's
// per-kind Min/MaxConfidence. So a config's confidence bounds only affect scoring
// via collector-side CLAMPING. To make a confidence-bound sweep meaningful this op
// re-clamps each primary signal's Confidence to the candidate [Min,Max] before
// composing — faithful to reusing ComposeScore, but note it can only explore the
// clamping regime (tighten a stored confidence, or raise one below a new floor);
// it cannot widen a value the collector already clamped away.
//
// CRITICAL persistence gap (found during implementation): the ONLY config-blob
// surface for dedup.signals.* is the four band thresholds (config.DedupSignalConfig).
// Per-kind confidences have NO field in the Config struct and would be silently
// dropped by UpdateConfig's JSON round-trip (same failure class as the retired
// flat keys). Adding a field is out of scope ("do NOT invent a new persistence
// path / no config.go changes"). Therefore:
//
//   - Round 1 — band thresholds under BASELINE (production) confidences — is the
//     APPLICABLE recommendation. Its target-met flags accurately predict prod
//     because prod runs baseline confidences + these bands. The apply path uses
//     ONLY these, and the apply gate is computed from the EXACT config being
//     persisted (baseline confidences + recommended bands).
//   - Round 2 — per-signal confidence bounds — is ADVISORY only. It is reported so
//     an operator can hand-edit config.yaml, but it is NEVER routed through
//     UpdateConfig, NEVER gates the band apply, and NEVER contributes to the
//     precision attributed to an applied band.
//
// # Discipline
//
// Dry-run (report only) is the default and only autonomous mode. {"apply":true}
// persists band thresholds via the existing config update service and echoes the
// previous values as the rollback record. Apply fires ONLY when every tunable band
// (CERTAIN, HIGH) met its target under baseline confidences — never partial,
// never target-not-met. Only CERTAIN and HIGH are tunable; MEDIUM/REVIEW are held
// at baseline and only reported.
//
// Usage:
//
//	POST /api/v1/operations/v2  {"def_id":"dedup.calibrate-composite"}
//	POST /api/v1/operations/v2  {"def_id":"dedup.calibrate-composite",
//	                             "params":{"target_precision_certain":0.98,"apply":true}}
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	// defaultTargetPrecisionCertain is the precision floor for the CERTAIN band
	// (auto-merge eligible), matching the embedding op's high default.
	defaultTargetPrecisionCertain = 0.98
	// defaultTargetPrecisionCompositeHigh is the precision floor for the HIGH band.
	defaultTargetPrecisionCompositeHigh = 0.90
	// defaultMinScoredPairs is the per-class fail-closed coverage floor. Below
	// this many scored pairs for EITHER class the op refuses to recommend.
	defaultMinScoredPairs = 500

	// bandSweepSpan / bandSweepStep define the ±span grid (score axis, 0..100)
	// swept coordinate-wise around each band's baseline minimum.
	bandSweepSpan = 10.0
	bandSweepStep = 0.5

	// confSweepSpan / confSweepStep define the ±span grid swept around each
	// per-kind confidence bound in the advisory round.
	confSweepSpan = 0.05
	confSweepStep = 0.01

	// minBandSampleSize is the minimum (true_dup+not_dup) count at/above a band
	// cut-point for that cut-point's precision to be trusted (mirrors the
	// embedding op's minSampleSizeForBestPrecision).
	minBandSampleSize = 5
)

// calibrateCompositeParams are the JSON parameters accepted by the op.
type calibrateCompositeParams struct {
	// TargetPrecisionCertain is the precision floor for the CERTAIN band. Default 0.98.
	TargetPrecisionCertain float64 `json:"target_precision_certain"`
	// TargetPrecisionHigh is the precision floor for the HIGH band. Default 0.90.
	TargetPrecisionHigh float64 `json:"target_precision_high"`
	// MinScoredPairs is the per-class fail-closed coverage floor. Default 500.
	MinScoredPairs int `json:"min_scored_pairs"`
	// Apply, when true AND every tunable band met its target under baseline
	// confidences, persists the recommended BAND thresholds. Default false
	// (report only). Operator-gated — never sent autonomously.
	Apply bool `json:"apply"`
}

// calibrateCompositeDef returns the OperationDef for dedup.calibrate-composite.
func (p *Plugin) calibrateCompositeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.calibrate-composite",
		Plugin:      "dedup",
		DisplayName: "Calibrate composite scorer (report; apply gated)",
		Description: "Replays each labeled pair's stored ScoreBreakdown signals through " +
			"unified.ComposeScore under coordinate-wise config variants against the " +
			"pair-deduped gold set, and recommends noisy-OR band thresholds hitting a target " +
			"precision. Per-signal confidence bounds are swept ADVISORY-only (no config-blob " +
			"surface). Dry-run by default; apply=true writes dedup.signals.* band thresholds — " +
			"operator-gated, refuses partial/target-not-met recommendations.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.calibrate-composite",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCalibrateComposite,
	}
}

// compositePair is one labeled pair plus the primary+supporting signal set
// recovered from its stored ScoreBreakdown. It is scored under candidate configs.
type compositePair struct {
	trueDup bool // true = true_dup, false = not_dup
	signals []models.Signal
}

// primaryKinds are the signal kinds that feed the noisy-OR product and whose
// confidence bounds are meaningful to sweep (supporting kinds use boosts only).
var primaryKinds = []unified.SignalKind{
	unified.SigExactFile, unified.SigExactAcoustID, unified.SigISBNASIN,
	unified.SigLSHAcoustID, unified.SigEmbedHigh, unified.SigMetaSrcHash,
	unified.SigMetaFuzzy, unified.SigEmbedMedium,
}

func isPrimaryKind(k unified.SignalKind) bool {
	for _, pk := range primaryKinds {
		if pk == k {
			return true
		}
	}
	return false
}

// cloneScoreConfig deep-copies a ScoreConfig, including its Signals map, so a
// candidate variant never aliases the baseline's per-kind entries.
func cloneScoreConfig(cfg unified.ScoreConfig) unified.ScoreConfig {
	out := cfg
	out.Signals = make(map[string]unified.KindConfig, len(cfg.Signals))
	for k, v := range cfg.Signals {
		out.Signals[k] = v
	}
	return out
}

// scoreWithClamp composes a pair's signals under cfg, re-clamping each primary
// signal's Confidence to that kind's [MinConfidence, MaxConfidence] first. This
// replays collector-side clamping so a confidence-bound change actually moves the
// composite score (ComposeScore itself reads Confidence verbatim). Supporting
// signals are passed through unchanged (they contribute boosts, not confidence).
func scoreWithClamp(signals []models.Signal, cfg unified.ScoreConfig) float64 {
	clamped := make([]models.Signal, len(signals))
	for i, s := range signals {
		cs := s
		if isPrimaryKind(s.Kind) {
			if kc, ok := cfg.Signals[string(s.Kind)]; ok {
				if cs.Confidence < kc.MinConfidence {
					cs.Confidence = kc.MinConfidence
				}
				if cs.Confidence > kc.MaxConfidence {
					cs.Confidence = kc.MaxConfidence
				}
			}
		}
		clamped[i] = cs
	}
	return unified.ComposeScore(clamped, nil, cfg, [2]string{}).Score
}

// scoreAll returns each pair's composite score under cfg (with clamping).
func scoreAll(pairs []compositePair, cfg unified.ScoreConfig) []float64 {
	scores := make([]float64, len(pairs))
	for i := range pairs {
		scores[i] = scoreWithClamp(pairs[i].signals, cfg)
	}
	return scores
}

// bandStat is the cumulative classifier stat at one band cut-point: everything
// scoring >= Min is treated as a duplicate at that band or higher.
type bandStat struct {
	Min       float64 `json:"min"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	N         int     `json:"n"` // TP+FP at/above the cut-point
}

// cumulativeBandStat computes precision/recall for the cut-point cutMin over the
// fixed score/label arrays. precision = TP/(TP+FP); recall = TP/totalTrue. A
// cut-point with no pairs at/above it has undefined precision (N==0).
func cumulativeBandStat(scores []float64, pairs []compositePair, cutMin float64, totalTrue int) bandStat {
	tp, fp := 0, 0
	for i := range pairs {
		if scores[i] < cutMin {
			continue
		}
		if pairs[i].trueDup {
			tp++
		} else {
			fp++
		}
	}
	st := bandStat{Min: cutMin, TP: tp, FP: fp, N: tp + fp}
	if tp+fp > 0 {
		st.Precision = float64(tp) / float64(tp+fp)
	}
	if totalTrue > 0 {
		st.Recall = float64(tp) / float64(totalTrue)
	}
	return st
}

// bandRec is the sweep result for one tunable band.
type bandRec struct {
	Met      bool     `json:"met"`
	Baseline bandStat `json:"baseline"`      // stat at the baseline band minimum
	Rec      bandStat `json:"rec,omitempty"` // stat at the recommended minimum (valid only when Met)
}

// sweepBandParallel evaluates every candidate cut-point in
// [gridLo, gridHi] (step bandSweepStep) IN PARALLEL — each candidate config
// variant is scored on its own goroutine from a bounded pool sized to
// runtime.NumCPU() — and returns the LOWEST cut-point whose precision >=
// targetPrecision (max recall subject to the precision floor) with a trustworthy
// sample size. scores are precomputed once under baseline confidences and shared
// read-only; ComposeScore is pure so the workers share nothing mutable. Each
// worker writes only its own pre-indexed result slot, so the pass is race-clean.
func sweepBandParallel(
	ctx context.Context,
	scores []float64,
	pairs []compositePair,
	totalTrue int,
	gridLo, gridHi, targetPrecision float64,
) (bandRec, error) {
	// Build the ascending candidate grid (lowest first = max recall). Integer
	// stepping (gridLo + i*step) keeps values exact — 0.5 is a power of two, so
	// a recommended min lands precisely on a grid boundary, never 91.4999…
	var cands []float64
	if gridHi >= gridLo {
		steps := int((gridHi-gridLo)/bandSweepStep + 0.5)
		for i := 0; i <= steps; i++ {
			t := gridLo + float64(i)*bandSweepStep
			if t > gridHi+1e-9 {
				break
			}
			cands = append(cands, t)
		}
	}
	stats := make([]bandStat, len(cands))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i := range cands {
		i := i
		g.Go(func() error {
			select {
			case <-gctx.Done():
				return gctx.Err()
			default:
			}
			stats[i] = cumulativeBandStat(scores, pairs, cands[i], totalTrue)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return bandRec{}, err
	}

	// First (lowest) candidate meeting the precision floor with enough samples.
	for _, st := range stats {
		if st.N >= minBandSampleSize && st.Precision >= targetPrecision {
			return bandRec{Met: true, Rec: st}, nil
		}
	}
	return bandRec{Met: false}, nil
}

// confSuggestion is one advisory per-kind confidence-bound recommendation.
type confSuggestion struct {
	Kind          string  `json:"kind"`
	Bound         string  `json:"bound"` // "min_confidence" | "max_confidence"
	From          float64 `json:"from"`
	To            float64 `json:"to"`
	CertainRecall float64 `json:"certain_recall_after"`
	HighRecall    float64 `json:"high_recall_after"`
}

// runCalibrateComposite implements the op. Dry-run reports; apply (operator-gated,
// all tunable bands met) persists band thresholds only.
func (p *Plugin) runCalibrateComposite(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}

	var params calibrateCompositeParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	targetCertain := params.TargetPrecisionCertain
	if targetCertain <= 0 {
		targetCertain = defaultTargetPrecisionCertain
	}
	targetHigh := params.TargetPrecisionHigh
	if targetHigh <= 0 {
		targetHigh = defaultTargetPrecisionCompositeHigh
	}
	minScored := params.MinScoredPairs
	if minScored <= 0 {
		minScored = defaultMinScoredPairs
	}

	log := reporter.Logger()
	log.Info("calibrate-composite start",
		"apply", params.Apply,
		"target_precision_certain", targetCertain,
		"target_precision_high", targetHigh,
		"min_scored_pairs", minScored)

	// --- Load labeled examples (both classes) ---
	_ = reporter.UpdateProgress(0, 4, "Loading labeled examples…")
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

	// --- Collapse to one row per canonical pair, then recover signals ---
	_ = reporter.UpdateProgress(1, 4, "Collapsing to unique pairs + parsing breakdowns…")
	rowsIn := len(examples)
	deduped := dataset.DedupeByPair(examples)
	pairsOut := len(deduped)

	var pairs []compositePair
	var scoredTrue, scoredNot, skippedNoBreakdown, skippedLabel int
	for i := range deduped {
		ex := deduped[i]
		switch ex.Label {
		case "true_dup", "not_dup":
			// keep
		default:
			skippedLabel++ // unsure / unlabeled / unknown
			continue
		}
		sigs, ok := parseBreakdownSignals(ex.ScoreBreakdown)
		if !ok {
			// nil / unparseable / empty-signal breakdown — counted, NEVER scored
			// as zero (a zero score would poison the not_dup precision math).
			skippedNoBreakdown++
			continue
		}
		cp := compositePair{trueDup: ex.Label == "true_dup", signals: sigs}
		pairs = append(pairs, cp)
		if cp.trueDup {
			scoredTrue++
		} else {
			scoredNot++
		}
	}

	log.Info("calibrate-composite coverage",
		"rows_in", rowsIn, "pairs_out", pairsOut,
		"scored_true_dup", scoredTrue, "scored_not_dup", scoredNot,
		"skipped_no_breakdown", skippedNoBreakdown, "skipped_label", skippedLabel)

	// --- Fail-closed coverage floor ---
	if scoredTrue < minScored || scoredNot < minScored {
		log.Info("calibrate-composite report",
			"status", "insufficient-coverage",
			"rows_in", rowsIn, "pairs_out", pairsOut,
			"scored_true_dup", scoredTrue, "scored_not_dup", scoredNot,
			"skipped_no_breakdown", skippedNoBreakdown,
			"min_scored_pairs", minScored)
		_ = reporter.UpdateProgress(4, 4, fmt.Sprintf(
			"insufficient-coverage — scored true_dup=%d not_dup=%d < min %d; recommending nothing.",
			scoredTrue, scoredNot, minScored))
		return nil
	}

	// --- Baseline config (production load; fall back to defaults) ---
	baseCfg, lerr := unified.LoadScoreConfig()
	if lerr != nil {
		log.Warn("calibrate-composite: LoadScoreConfig failed, using DefaultScoreConfig", "error", lerr)
		baseCfg = unified.DefaultScoreConfig()
	}
	totalTrue := scoredTrue

	// Baseline scores under production confidences — computed ONCE and shared
	// read-only across the parallel band sweep.
	_ = reporter.UpdateProgress(2, 4, "Scoring pairs at baseline + sweeping bands…")
	baseScores := scoreAll(pairs, baseCfg)

	baselineCertain := cumulativeBandStat(baseScores, pairs, baseCfg.BandCertainMin, totalTrue)
	baselineHigh := cumulativeBandStat(baseScores, pairs, baseCfg.BandHighMin, totalTrue)
	baselineMedium := cumulativeBandStat(baseScores, pairs, baseCfg.BandMediumMin, totalTrue)
	baselineReview := cumulativeBandStat(baseScores, pairs, baseCfg.BandReviewMin, totalTrue)

	// --- Round 1: band-threshold sweep (APPLICABLE) ---
	// Sweep HIGH first, then constrain CERTAIN's grid to sit STRICTLY above the
	// recommended HIGH min. This bakes strict CERTAIN > HIGH > MEDIUM > REVIEW
	// ordering into the search space rather than dropping a valid recommendation
	// after the fact, so a cleanly separable set (both bands want the same cut)
	// yields certain = high + one step, not a suppressed pair.
	highLo := maxf(0, baseCfg.BandHighMin-bandSweepSpan)
	if lo := baseCfg.BandMediumMin + bandSweepStep; lo > highLo {
		highLo = lo
	}
	highHi := minf(baseCfg.BandCertainMin-bandSweepStep, baseCfg.BandHighMin+bandSweepSpan)
	highRec, err := sweepBandParallel(ctx, baseScores, pairs, totalTrue, highLo, highHi, targetHigh)
	if err != nil {
		return err
	}
	highRec.Baseline = baselineHigh

	certainLo := maxf(0, baseCfg.BandCertainMin-bandSweepSpan)
	if lo := baseCfg.BandHighMin + bandSweepStep; lo > certainLo {
		certainLo = lo
	}
	// If HIGH moved, CERTAIN must clear the NEW high min, not just the baseline.
	if highRec.Met {
		if lo := highRec.Rec.Min + bandSweepStep; lo > certainLo {
			certainLo = lo
		}
	}
	certainHi := minf(100, baseCfg.BandCertainMin+bandSweepSpan)
	certainRec, err := sweepBandParallel(ctx, baseScores, pairs, totalTrue, certainLo, certainHi, targetCertain)
	if err != nil {
		return err
	}
	certainRec.Baseline = baselineCertain

	// Defensive final ordering assertion — the constrained grid above makes this
	// unreachable, but never emit a non-monotonic band config.
	orderingConflict := false
	if certainRec.Met && highRec.Met && certainRec.Rec.Min <= highRec.Rec.Min {
		orderingConflict = true
		log.Warn("calibrate-composite: band ordering conflict — certain<=high recommendation, not applicable",
			"certain_min", certainRec.Rec.Min, "high_min", highRec.Rec.Min)
	}

	// --- Round 2: per-kind confidence-bound sweep (ADVISORY only) ---
	// Evaluated against the round-1 recommended band thresholds (or baseline when
	// a band was not met). Never persisted; never gates the band apply.
	certainBandForConf := recOrBaselineMin(certainRec, baseCfg.BandCertainMin)
	highBandForConf := recOrBaselineMin(highRec, baseCfg.BandHighMin)
	_ = reporter.UpdateProgress(3, 4, "Sweeping per-kind confidence bounds (advisory)…")
	confSuggestions, err := sweepConfidenceAdvisory(
		ctx, pairs, baseCfg, totalTrue,
		certainBandForConf, targetCertain, highBandForConf, targetHigh)
	if err != nil {
		return err
	}

	// --- Recommended config being considered for APPLY: baseline confidences +
	// recommended bands (this is exactly what prod would run) ---
	recCfg := cloneScoreConfig(baseCfg)
	if certainRec.Met && !orderingConflict {
		recCfg.BandCertainMin = certainRec.Rec.Min
	}
	if highRec.Met && !orderingConflict {
		recCfg.BandHighMin = highRec.Rec.Min
	}

	// The apply gate: every TUNABLE band met its target under baseline confidences,
	// and no ordering conflict. Computed from the exact config being persisted.
	allTargetsMet := certainRec.Met && highRec.Met && !orderingConflict

	baselineJSON, _ := json.Marshal(baseCfg)
	recJSON, _ := json.Marshal(recCfg)

	// --- Report ---
	fields := []any{
		"status", "ok",
		"rows_in", rowsIn,
		"pairs_out", pairsOut,
		"scored_true_dup", scoredTrue,
		"scored_not_dup", scoredNot,
		"skipped_no_breakdown", skippedNoBreakdown,
		"target_precision_certain", targetCertain,
		"target_precision_high", targetHigh,
		"baseline_certain_min", baseCfg.BandCertainMin,
		"baseline_certain_precision", baselineCertain.Precision,
		"baseline_certain_recall", baselineCertain.Recall,
		"baseline_certain_n", baselineCertain.N,
		"baseline_high_min", baseCfg.BandHighMin,
		"baseline_high_precision", baselineHigh.Precision,
		"baseline_high_recall", baselineHigh.Recall,
		"baseline_high_n", baselineHigh.N,
		"baseline_medium_precision", baselineMedium.Precision,
		"baseline_medium_recall", baselineMedium.Recall,
		"baseline_medium_n", baselineMedium.N,
		"baseline_review_precision", baselineReview.Precision,
		"baseline_review_recall", baselineReview.Recall,
		"baseline_review_n", baselineReview.N,
		"certain_target_met", certainRec.Met && !orderingConflict,
		"high_target_met", highRec.Met && !orderingConflict,
		"band_ordering_conflict", orderingConflict,
		"all_targets_met", allTargetsMet,
		"baseline_config_json", string(baselineJSON),
		"recommended_config_json", string(recJSON),
		"advisory_confidence_suggestions", len(confSuggestions),
	}
	if certainRec.Met && !orderingConflict {
		fields = append(fields,
			"recommended_certain_min", certainRec.Rec.Min,
			"recommended_certain_precision", certainRec.Rec.Precision,
			"recommended_certain_recall", certainRec.Rec.Recall,
			"recommended_certain_n", certainRec.Rec.N)
	} else {
		fields = append(fields, "certain_target_not_met", true)
	}
	if highRec.Met && !orderingConflict {
		fields = append(fields,
			"recommended_high_min", highRec.Rec.Min,
			"recommended_high_precision", highRec.Rec.Precision,
			"recommended_high_recall", highRec.Rec.Recall,
			"recommended_high_n", highRec.Rec.N)
	} else {
		fields = append(fields, "high_target_not_met", true)
	}
	log.Info("calibrate-composite report", fields...)
	for _, s := range confSuggestions {
		log.Info("calibrate-composite advisory confidence suggestion (config.yaml edit only, NOT applied)",
			"kind", s.Kind, "bound", s.Bound, "from", s.From, "to", s.To,
			"certain_recall_after", s.CertainRecall, "high_recall_after", s.HighRecall)
	}

	summary := fmt.Sprintf(
		"scored_true=%d scored_not=%d skipped_no_breakdown=%d certain=%s high=%s advisory_conf=%d",
		scoredTrue, scoredNot, skippedNoBreakdown,
		describeBandRec(certainRec, orderingConflict), describeBandRec(highRec, orderingConflict), len(confSuggestions))

	// --- Apply path (operator-gated; bands only; all tunable targets met) ---
	if !params.Apply {
		_ = reporter.UpdateProgress(4, 4, "Dry-run — "+summary+
			". Bands are report-only until apply=true (operator-gated); confidence suggestions require a config.yaml edit.")
		log.Info("calibrate-composite: dry-run only; nothing written")
		return nil
	}
	if !allTargetsMet {
		log.Warn("calibrate-composite: apply requested but not every tunable band met its target — writing nothing",
			"certain_met", certainRec.Met, "high_met", highRec.Met, "ordering_conflict", orderingConflict)
		_ = reporter.UpdateProgress(4, 4, "Apply requested but target(s) not met — refused, nothing written. "+summary)
		return nil
	}

	if err := p.applyBandThresholds(recCfg, baseCfg, log); err != nil {
		return err
	}
	_ = reporter.UpdateProgress(4, 4, fmt.Sprintf(
		"Applied band thresholds certain=%.2f high=%.2f (previous certain=%.2f high=%.2f — rollback record). %s",
		recCfg.BandCertainMin, recCfg.BandHighMin, baseCfg.BandCertainMin, baseCfg.BandHighMin, summary))
	return nil
}

// applyBandThresholds persists the recommended band thresholds via the existing
// config update service (the ONLY dedup.signals.* blob surface). It writes all
// four band mins — recommended for tunable bands, baseline for held ones — so the
// persisted config stays internally ordered, and loudly logs the previous values
// as the rollback record.
func (p *Plugin) applyBandThresholds(recCfg, prevCfg unified.ScoreConfig, log *slog.Logger) error {
	if p.store == nil {
		return fmt.Errorf("main store not available; cannot persist config")
	}
	log.Info("calibrate-composite APPLY: persisting band thresholds",
		"new_certain_min", recCfg.BandCertainMin, "new_high_min", recCfg.BandHighMin,
		"new_medium_min", recCfg.BandMediumMin, "new_review_min", recCfg.BandReviewMin,
		"previous_certain_min", prevCfg.BandCertainMin, "previous_high_min", prevCfg.BandHighMin,
		"previous_medium_min", prevCfg.BandMediumMin, "previous_review_min", prevCfg.BandReviewMin,
		"rollback", "re-run apply with the previous_* values to restore")

	payload := map[string]any{
		"dedup": map[string]any{
			"signals": map[string]any{
				"band_certain_min": recCfg.BandCertainMin,
				"band_high_min":    recCfg.BandHighMin,
				"band_medium_min":  recCfg.BandMediumMin,
				"band_review_min":  recCfg.BandReviewMin,
			},
		},
	}
	svc := config.NewUpdateService(p.store)
	if err := svc.ApplyUpdates(payload); err != nil {
		return fmt.Errorf("persist band thresholds: %w", err)
	}
	log.Info("calibrate-composite APPLY: band thresholds persisted (survives restart via config blob)")
	return nil
}

// recOrBaselineMin returns the recommended band minimum when met, else the baseline.
func recOrBaselineMin(r bandRec, baseline float64) float64 {
	if r.Met {
		return r.Rec.Min
	}
	return baseline
}

func describeBandRec(r bandRec, orderingConflict bool) string {
	if !r.Met || orderingConflict {
		return "target-not-met"
	}
	return fmt.Sprintf("min=%.2f(p=%.3f,r=%.3f)", r.Rec.Min, r.Rec.Precision, r.Rec.Recall)
}

// parseBreakdownSignals recovers the signal set from a stored ScoreBreakdown JSON
// snapshot. Returns ok=false for nil/empty/unparseable payloads OR a parsed
// breakdown carrying no signals (both are un-scorable and must be skipped+counted,
// never treated as a zero score).
func parseBreakdownSignals(raw json.RawMessage) ([]models.Signal, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var uds models.UnifiedDedupScore
	if err := json.Unmarshal(raw, &uds); err != nil {
		return nil, false
	}
	if len(uds.Signals) == 0 {
		return nil, false
	}
	return uds.Signals, true
}

// sweepConfidenceAdvisory sweeps each primary kind's MinConfidence and
// MaxConfidence over a ±confSweepSpan grid (coordinate-wise, one bound at a time)
// and records any change that IMPROVES total target-band recall without dropping
// either met target below its precision floor. Advisory only — the returned
// suggestions are reported for a manual config.yaml edit and are never persisted.
//
// Each candidate config variant re-scores the whole pair slice, so the OUTER
// variant loop is sharded across a bounded pool sized to runtime.NumCPU(); every
// worker writes only its own pre-indexed slot (race-clean) and ComposeScore is
// pure so nothing mutable is shared.
func sweepConfidenceAdvisory(
	ctx context.Context,
	pairs []compositePair,
	baseCfg unified.ScoreConfig,
	totalTrue int,
	certainMin, targetCertain, highMin, targetHigh float64,
) ([]confSuggestion, error) {
	// Which targets are met at baseline confidences — a suggestion must not break these.
	baseScores := scoreAll(pairs, baseCfg)
	baseCertain := cumulativeBandStat(baseScores, pairs, certainMin, totalTrue)
	baseHigh := cumulativeBandStat(baseScores, pairs, highMin, totalTrue)
	certainMetBase := baseCertain.N >= minBandSampleSize && baseCertain.Precision >= targetCertain
	highMetBase := baseHigh.N >= minBandSampleSize && baseHigh.Precision >= targetHigh
	baseObjective := baseCertain.Recall + baseHigh.Recall

	working := cloneScoreConfig(baseCfg)
	var suggestions []confSuggestion

	type variant struct {
		kind  unified.SignalKind
		bound string
		value float64
	}

	for _, kind := range primaryKinds {
		kc, ok := working.Signals[string(kind)]
		if !ok {
			continue
		}
		for _, bound := range []string{"min_confidence", "max_confidence"} {
			base := kc.MinConfidence
			if bound == "max_confidence" {
				base = kc.MaxConfidence
			}
			// Build candidate values around the current bound value.
			var variants []variant
			for d := -confSweepSpan; d <= confSweepSpan+1e-9; d += confSweepStep {
				v := clampUnit(base + d)
				variants = append(variants, variant{kind: kind, bound: bound, value: v})
			}

			results := make([]struct {
				certain, high bandStat
				valid         bool
				value         float64
			}, len(variants))

			g, gctx := errgroup.WithContext(ctx)
			g.SetLimit(runtime.NumCPU())
			for i := range variants {
				i := i
				g.Go(func() error {
					select {
					case <-gctx.Done():
						return gctx.Err()
					default:
					}
					cand := cloneScoreConfig(working)
					ck := cand.Signals[string(kind)]
					if variants[i].bound == "min_confidence" {
						ck.MinConfidence = variants[i].value
					} else {
						ck.MaxConfidence = variants[i].value
					}
					// Keep min<=max; an invalid combination is skipped, not scored.
					if ck.MinConfidence > ck.MaxConfidence {
						results[i].valid = false
						return nil
					}
					cand.Signals[string(kind)] = ck
					sc := scoreAll(pairs, cand)
					results[i].certain = cumulativeBandStat(sc, pairs, certainMin, totalTrue)
					results[i].high = cumulativeBandStat(sc, pairs, highMin, totalTrue)
					results[i].valid = true
					results[i].value = variants[i].value
					return nil
				})
			}
			if err := g.Wait(); err != nil {
				return nil, err
			}

			// Pick the variant maximizing total target-band recall subject to
			// every baseline-met target staying met (anti-over-suppression).
			bestObjective := baseObjective
			bestIdx := -1
			for i, r := range results {
				if !r.valid {
					continue
				}
				if certainMetBase && !(r.certain.N >= minBandSampleSize && r.certain.Precision >= targetCertain) {
					continue
				}
				if highMetBase && !(r.high.N >= minBandSampleSize && r.high.Precision >= targetHigh) {
					continue
				}
				obj := r.certain.Recall + r.high.Recall
				if obj > bestObjective+1e-9 {
					bestObjective = obj
					bestIdx = i
				}
			}
			if bestIdx >= 0 {
				chosen := results[bestIdx]
				suggestions = append(suggestions, confSuggestion{
					Kind: string(kind), Bound: bound, From: base, To: chosen.value,
					CertainRecall: chosen.certain.Recall, HighRecall: chosen.high.Recall,
				})
				// Adopt into the working cfg so later coordinates compound (advisory only).
				wk := working.Signals[string(kind)]
				if bound == "min_confidence" {
					wk.MinConfidence = chosen.value
				} else {
					wk.MaxConfidence = chosen.value
				}
				working.Signals[string(kind)] = wk
				kc = wk
				baseObjective = bestObjective
			}
		}
	}
	return suggestions, nil
}

func clampUnit(v float64) float64 {
	if v < 0.0001 {
		return 0.0001
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
