// file: internal/plugins/dedup/breakdown_backfill.go
// version: 1.2.1
// guid: ec0f5e9d-2f6d-485d-9f24-ad3d917d1834
// last-edited: 2026-08-20

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
// # Band histogram
//
// Both dry-run and apply report a band histogram over the SCORED pairs, plus a
// risk split of the CERTAIN band. The band alone is not the merge gate:
// dedup.auto-resolve additionally requires >=2 distinct kinds from its primary
// allow-list (exact_file, exact_acoustid, isbn_asin, metadata_hash), which
// excludes metadata_fuzzy and the embedding kinds. Since this op is what GIVES
// these pre-T015 rows a Band and a breakdown at all — and a nil breakdown is
// itself an auto-resolve refusal reason — an apply=true run CREATES auto-merge
// eligibility that did not exist, which is what the CERTAIN split is for. The
// distinct-kind count comes from dedup.DistinctAutoResolvePrimaryKinds, the
// same function the eligibility check calls, so the report cannot drift from
// the gate.
//
// SCOPE OF THAT NUMBER — it is the corroboration clause evaluated ALONE, and
// is neither an upper nor a lower bound on eligibility. autoResolveEligible
// refuses independently on active suppressors, implausible audio on either
// side, and conflicting identifiers (all of which would shrink it), and it
// accepts a "0"/"1" row outright when the pair carries a whole-book-signature
// true_dup label (which would grow it). None of those inputs are reachable
// from this op: its entire store surface is ListCandidates +
// UpdateCandidateScore, and widening it to tighten a reported number is a
// worse trade than reporting the number with its scope stated.
//
// Note also that the rescore path composes with NIL suppressors
// (rescore.go: ComposeScore(signals, nil, …)), so every breakdown this op
// writes carries an empty Suppressors list. A "certain_with_suppressors"
// counter here would be a structural zero, not a measurement.
//
// That emptiness used to mean the suppressor guard in autoResolveEligible
// passed vacuously on these rows — and, since no production path populates the
// field at all, on every other row too. It no longer does: autoResolveEligible
// now evaluates PairEligibility LIVE against both book records rather than
// trusting the stored list, so a suppressed pair is refused at the gate whether
// or not its breakdown ever recorded the fact. The structural zero here is
// therefore still a true statement about what this op writes, and no longer a
// statement about what auto-resolve can catch.
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
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

// breakdownBackfillBelowBandKey names the report bucket for a composed score
// that landed below the REVIEW floor (Band == ""). This op deliberately
// bypasses the scan's below-band skip, so those rows are real and persisted —
// a bare "" key in the JSON histogram would read as a bug rather than a band.
const breakdownBackfillBelowBandKey = "BELOW"

// breakdownBackfillBandOrder is the band order used in the human-readable
// summary line (highest confidence first). The JSON map is unordered.
var breakdownBackfillBandOrder = []string{
	unified.BandCertain, unified.BandHigh, unified.BandMedium, unified.BandReview,
	breakdownBackfillBelowBandKey,
}

// bandKey maps a composed score's band to its histogram bucket, naming the
// below-band case instead of emitting an empty key.
func bandKey(band string) string {
	if band == "" {
		return breakdownBackfillBelowBandKey
	}
	return band
}

// signalSetKey renders a pair's evidence as a stable, sorted, "+"-joined set of
// DISTINCT signal kinds ("isbn_asin+metadata_fuzzy"), so the histogram can be
// grouped by what a pair is actually made of.
//
// WHY the whole set rather than the single strongest signal: the composed score
// is a noisy-OR product, so two mid-confidence signals can outrank one strong
// one (0.90+0.90 → 99.0 beats a lone 0.95 → 95.0). A "top signal" column would
// misattribute exactly those rows — the ones worth looking at. The key space is
// bounded by the fixed signal-kind list, so there is no cap and nothing is
// silently dropped.
//
// Deliberate asymmetry with certain_primary_kind_counts: this key lists EVERY
// distinct kind present, including zero-confidence supporting signals (a
// duration match contributes its configured boost regardless of Confidence),
// whereas DistinctAutoResolvePrimaryKinds ignores Confidence == 0. So a set may
// name a kind that counted toward the score but not toward corroboration.
func signalSetKey(signals []unified.Signal) string {
	seen := make(map[string]bool, len(signals))
	kinds := make([]string, 0, len(signals))
	for _, s := range signals {
		k := string(s.Kind)
		if seen[k] {
			continue
		}
		seen[k] = true
		kinds = append(kinds, k)
	}
	if len(kinds) == 0 {
		return "none"
	}
	sort.Strings(kinds)
	return strings.Join(kinds, "+")
}

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

	// BandCounts is the band histogram over every SCORED pair (zero-signal
	// pairs are excluded — they are counted by ZeroSignal and have no band).
	// Keys: CERTAIN/HIGH/MEDIUM/REVIEW plus BELOW for sub-REVIEW scores.
	BandCounts map[string]int `json:"band_counts"`

	// CertainSignalsEq1 counts CERTAIN pairs carrying exactly ONE signal of
	// any kind — the shape that shows up in the sample log.
	CertainSignalsEq1 int `json:"certain_signals_eq_1"`

	// CertainPrimaryKindCounts buckets CERTAIN pairs by how many DISTINCT
	// auto-resolve primary signal kinds they carry ("0"/"1"/"2+"), using
	// dedup.DistinctAutoResolvePrimaryKinds — the same rule
	// autoResolveEligible enforces. This is the CORROBORATION CLAUSE ALONE:
	// the "2+" bucket is not an upper bound (the other guards refuse
	// independently) nor a lower bound ("0"/"1" rows carrying a
	// whole-book-signature true_dup label are eligible anyway). See the
	// package doc for why the op does not evaluate the rest.
	CertainPrimaryKindCounts map[string]int `json:"certain_primary_kind_counts"`

	// CertainSignalSets counts CERTAIN pairs by their full distinct signal
	// set ("exact_file", "isbn_asin+duration", …). This is what answers
	// "can a fuzzy title match alone reach CERTAIN" with data instead of an
	// argument from the calibration table.
	CertainSignalSets map[string]int `json:"certain_signal_sets"`
}

// breakdownBackfillDef returns the OperationDef for dedup.breakdown-backfill.
func (p *Plugin) breakdownBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.breakdown-backfill",
		Liveness:    sdk.LivenessRunItems,
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
		bandCounts       = map[string]int{}
		certainSigsEq1   int
		certainPrimaries = map[string]int{}
		certainSets      = map[string]int{}
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

		for i := range results {
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
			// Histogram every SCORED pair — apply and dry-run alike. Counting
			// here (not inside the !Apply arm below) keeps an apply=true report
			// from showing an all-zero histogram that reads as a regression.
			band := bandKey(res.Score.Band)
			bandCounts[band]++
			if band == unified.BandCertain {
				if res.NumSignals == 1 {
					certainSigsEq1++
				}
				// The auto-merge gate is "≥2 distinct primary kinds", NOT the
				// band — a CERTAIN pair in the "0"/"1" bucket is one that
				// dedup.auto-resolve would refuse.
				switch n := len(dedupengine.DistinctAutoResolvePrimaryKinds(res.Score.Signals)); {
				case n >= 2:
					certainPrimaries["2+"]++
				case n == 1:
					certainPrimaries["1"]++
				default:
					certainPrimaries["0"]++
				}
				certainSets[signalSetKey(res.Score.Signals)]++
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

		BandCounts:               bandCounts,
		CertainSignalsEq1:        certainSigsEq1,
		CertainPrimaryKindCounts: certainPrimaries,
		CertainSignalSets:        certainSets,
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
		"band_counts", bandCounts,
	)
	// Risk profile of the CERTAIN band, logged on its own line: the band alone
	// does not authorise a merge, the distinct-primary-kind count does.
	log.Info("breakdown-backfill band histogram",
		"band_counts", bandCounts,
		"certain_signals_eq_1", certainSigsEq1,
		"certain_primary_kind_counts", certainPrimaries,
		"certain_signal_sets", certainSets,
	)

	summary := fmt.Sprintf(
		"targets=%d processed=%d backfilled=%d would_backfill=%d skipped_has_breakdown=%d zero_signal=%d skipped_no_book=%d score_errs=%d update_errs=%d",
		targets, processed, backfilled, wouldBackfill, skippedHasBreakdown, zeroSignal, skippedNoBook, scoreErrs, updateErrs)
	for k, v := range missingSignalCounts {
		summary += fmt.Sprintf(" missing[%s]=%d", k, v)
	}
	// Bands in a fixed high→low order (the JSON map is unordered); zero
	// buckets are printed too, so an absent band reads as measured-zero rather
	// than as a dropped key.
	for _, b := range breakdownBackfillBandOrder {
		summary += fmt.Sprintf(" %s=%d", b, bandCounts[b])
	}
	summary += fmt.Sprintf(" certain_1sig=%d certain_primary[0]=%d certain_primary[1]=%d certain_primary[2+]=%d",
		certainSigsEq1, certainPrimaries["0"], certainPrimaries["1"], certainPrimaries["2+"])

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, "Dry-run — "+summary+". Pass apply=true to persist ScoreBreakdowns.")
	} else {
		_ = reporter.UpdateProgress(3, 3, "Complete — "+summary+".")
	}
	return nil
}
