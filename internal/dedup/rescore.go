// file: internal/dedup/rescore.go
// version: 1.0.0
// guid: 8b1d4f27-6a90-4c3e-9d21-0f5a7c2e8b64
// last-edited: 2026-07-12

// Package dedup — per-pair signal gather shared by the operational unified scan
// and the dedup.rescore-labeled-examples op (ScorePairsForBook).
//
// # Why this file exists
//
// The operational unified scan (runUnifiedScoringForBook) drops every pair whose
// composite score falls below the review band (`if composed.Band == "" { continue }`)
// and never re-snapshots a dismissed pair's LabeledExample. As a result the
// calibration gold set (dedup.calibrate-composite) has almost no scored not_dup
// pairs — the below-band negatives the scan discards ARE the calibration signal.
//
// ScorePairsForBook recomputes each labeled pair's ScoreBreakdown using the SAME
// collectors + unified.ComposeScore as the operational scan (via the shared
// collectPairSignals helper — there is NO second copy of the scoring math), with
// two deliberate divergences:
//
//  1. It scores an explicitly injected work list of (A, B) pairs — dismissed
//     pairs are in no book's candidate list, so they must be fed in by ID.
//  2. It does NOT drop below-band pairs — the caller persists the breakdown for
//     every pair that produced at least one signal.
//
// The embedding cosine is sourced from the labeled example's stored Similarity
// (passed in via RescorePairInput.EmbeddingCos, gated by the caller on
// Layer=="embedding"), mirroring the scan's embeddingMap construction exactly —
// it is NOT recomputed, so no signal is injected that the deployed scorer would
// not have produced.

package dedup

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// pairKey indexes the embedding cosine map in both pair directions.
type pairKey = [2]string

// pairSignalBatches holds the per-book precomputed signal batches (and the
// per-book configs) that collectPairSignals filters per candidate. Bundling
// them lets runUnifiedScoringForBook and ScorePairsForBook share ONE copy of the
// gather logic, which is what keeps their scoring bit-identical.
type pairSignalBatches struct {
	exactHashSigs   []unified.Signal
	isbnSigs        []unified.Signal
	metaSrcSigs     []unified.Signal
	durationSigs    []unified.Signal
	exactAcoustSigs []unified.Signal
	lshAcoustSigs   []unified.Signal
	// embeddingMap holds the pair's embedding cosine indexed in both directions.
	embeddingMap map[pairKey]float64
	embCfg       EmbeddingCollectorConfig
	fuzCfg       MetaFuzzyConfig
	authorName   string
}

// collectPairSignals gathers every evidence signal for the (book, candID) pair
// from the precomputed per-book batches. Extracted verbatim from
// runUnifiedScoringForBook's inner loop so the operational scan and
// dedup.rescore-labeled-examples score pairs through the identical code path.
func (de *Engine) collectPairSignals(book *database.Book, candID string, b pairSignalBatches) []unified.Signal {
	var signals []unified.Signal

	// Exact-file hash signals (pre-computed).
	for _, s := range b.exactHashSigs {
		if isSigForPair(s, book.ID, candID) {
			signals = append(signals, s)
		}
	}

	// ISBN/ASIN (pre-computed).
	for _, s := range b.isbnSigs {
		if isSigForPair(s, book.ID, candID) {
			signals = append(signals, s)
		}
	}

	// Metadata source hash (pre-computed).
	for _, s := range b.metaSrcSigs {
		if isSigForPair(s, book.ID, candID) {
			signals = append(signals, s)
		}
	}

	// Embedding signal — O(1) map lookup (PH-3b).
	if cos, ok := b.embeddingMap[pairKey{book.ID, candID}]; ok {
		cos32 := float32(cos)
		if cos >= b.embCfg.HighThreshold {
			signals = append(signals, unified.Signal{
				Kind:       unified.SigEmbedHigh,
				Raw:        cos,
				Confidence: embedHighConfidence(cos32, b.embCfg.HighThreshold),
				Evidence: fmt.Sprintf(
					"embedding cosine %.4f (high tier): book %s ↔ %s",
					cos32, book.ID, candID),
			})
		} else if cos >= b.embCfg.LowThreshold {
			signals = append(signals, unified.Signal{
				Kind:       unified.SigEmbedMedium,
				Raw:        cos,
				Confidence: embedMediumConfidence(cos32, b.embCfg.LowThreshold, b.embCfg.HighThreshold),
				Evidence: fmt.Sprintf(
					"embedding cosine %.4f (medium tier): book %s ↔ %s",
					cos32, book.ID, candID),
			})
		}
	}

	// Duration signal (pre-computed).
	for _, s := range b.durationSigs {
		if isDurationSigFor(s.Evidence, book.ID, candID) {
			signals = append(signals, s)
		}
	}

	// Metadata-fuzzy (per-candidate by design — takes candIDs param).
	if sigs, err := CollectMetaFuzzy(de.bookStore, book, b.authorName, []string{candID}, b.fuzCfg); err == nil {
		signals = append(signals, sigs...)
	}

	// AcoustID signals (pre-computed).
	for _, s := range b.exactAcoustSigs {
		if isSigForBookID(s.Evidence, candID) {
			signals = append(signals, s)
		}
	}
	for _, s := range b.lshAcoustSigs {
		if isSigForBookID(s.Evidence, candID) {
			signals = append(signals, s)
		}
	}

	return signals
}

// RescorePairInput identifies one pair to rescore against book A. OtherID is the
// B-side book ID. EmbeddingCos carries the labeled example's stored embedding
// cosine ONLY when the pair's stored layer is "embedding" (nil otherwise), so the
// embedding signal is reconstructed exactly as the operational scan would have
// produced it — never recomputed for a non-embedding-layer pair.
type RescorePairInput struct {
	OtherID      string
	EmbeddingCos *float64
}

// RescorePairResult is one scored pair. Score is nil when the pair produced no
// signals (unscorable — the caller reports it but never persists a zero-signal
// breakdown, which would poison the calibrator's not_dup precision math).
type RescorePairResult struct {
	OtherID    string
	Score      *unified.UnifiedDedupScore
	NumSignals int
}

// ScorePairsForBook recomputes the ScoreBreakdown for a work list of pairs that
// all share book A (aID), using the SAME collectors + unified.ComposeScore as the
// operational unified scan (via collectPairSignals). It runs book A's per-book
// precompute ONCE and scores every B against it.
//
// Two deliberate divergences from runUnifiedScoringForBook:
//   - the work list is injected explicitly (dismissed pairs are in no candidate
//     list), and
//   - the below-band skip (`if composed.Band == "" { continue }`) is BYPASSED —
//     every pair with >=1 signal gets a composed score returned regardless of band.
//
// It performs NO candidate lifecycle work (no eligibility delete, no upsert): its
// sole job is to manufacture breakdowns for an already-labeled calibration set.
// Eligibility filtering would drop the very below-band negatives we need. Signal
// gathering is symmetric, so scoring from the canonical-A side matches what the
// operational scan produces from whichever endpoint owned the candidate.
//
// CONTRACT: on success it returns exactly ONE result per input, in the SAME order
// as inputs. Callers rely on positional (index) zipping to map results back to
// their labeled example — two inputs with the same OtherID (a pair re-created
// under a new candidateID while the old label row persists) are distinct entries
// and MUST NOT be de-duplicated by OtherID. A short/empty return (nil book,
// mid-flight cancellation) means the trailing inputs were not scored.
func (de *Engine) ScorePairsForBook(ctx context.Context, aID string, inputs []RescorePairInput) ([]RescorePairResult, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	book, err := de.bookStore.GetBookByID(aID)
	if err != nil {
		return nil, fmt.Errorf("ScorePairsForBook: get book %s: %w", aID, err)
	}
	if book == nil {
		return nil, nil
	}

	// Resolve author name (identical to CheckBook / the operational scan).
	authorName := ""
	if book.AuthorID != nil {
		if author, aErr := de.bookStore.GetAuthorByID(*book.AuthorID); aErr == nil && author != nil {
			authorName = author.Name
		}
	}

	// Per-book configs — identical to runUnifiedScoringForBook.
	cfg := de.getScoreConfig()
	embCfg := DefaultEmbeddingCollectorConfig()
	embHigh, embLow := de.resolvedBookThresholds()
	embCfg.HighThreshold = embHigh
	embCfg.LowThreshold = embLow
	durCfg := DefaultDurationCollectorConfig()
	fuzCfg := DefaultMetaFuzzyConfig()
	lshCfg := DefaultLSHAcoustIDConfig()

	bookFiles, _ := de.bookStore.GetBookFiles(book.ID)

	// Per-book precompute — identical collectors to the operational scan. The
	// duration collector's tagStore is nil here: tags are a pure side effect that
	// does NOT affect the emitted signals, so skipping them keeps scoring
	// bit-identical while avoiding surprise writes during a rescore.
	allExactHashSigs, _ := CollectExactFileHash(de.bookStore, book)
	allISBNSigs, _ := CollectISBNASIN(ctx, de.bookStore, de.isbnIndexStore, book)
	allMetaSrcSigs, _ := CollectMetaSrcHash(de.bookStore, book)
	allDurationSigs, _ := CollectDuration(de.bookStore, nil, book, durCfg)

	var allExactAcoustSigs, allLSHAcoustSigs []unified.Signal
	if de.acoustidBookFileStore != nil {
		for _, bf := range bookFiles {
			sigs, _ := CollectExactAcoustID(de.acoustidBookFileStore, &bf, book.ID)
			allExactAcoustSigs = append(allExactAcoustSigs, sigs...)
		}
	}
	if de.lshAcoustIDStore != nil {
		for _, bf := range bookFiles {
			sigs, _ := CollectLSHAcoustID(de.lshAcoustIDStore, &bf, book.ID, lshCfg)
			allLSHAcoustSigs = append(allLSHAcoustSigs, sigs...)
		}
	}

	// Embedding map from the caller-supplied stored cosines (mirrors the scan's
	// embeddingMap, which reads candidate.Similarity gated on Layer=="embedding").
	embeddingMap := make(map[pairKey]float64, len(inputs))
	for _, in := range inputs {
		if in.EmbeddingCos != nil {
			embeddingMap[pairKey{aID, in.OtherID}] = *in.EmbeddingCos
			embeddingMap[pairKey{in.OtherID, aID}] = *in.EmbeddingCos
		}
	}

	batches := pairSignalBatches{
		exactHashSigs:   allExactHashSigs,
		isbnSigs:        allISBNSigs,
		metaSrcSigs:     allMetaSrcSigs,
		durationSigs:    allDurationSigs,
		exactAcoustSigs: allExactAcoustSigs,
		lshAcoustSigs:   allLSHAcoustSigs,
		embeddingMap:    embeddingMap,
		embCfg:          embCfg,
		fuzCfg:          fuzCfg,
		authorName:      authorName,
	}

	results := make([]RescorePairResult, 0, len(inputs))
	for _, in := range inputs {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		signals := de.collectPairSignals(book, in.OtherID, batches)
		res := RescorePairResult{OtherID: in.OtherID, NumSignals: len(signals)}
		if len(signals) > 0 {
			// BYPASS the band gate: compose and return the score regardless of band.
			composed := unified.ComposeScore(signals, nil, cfg, canonicalPairIDs(aID, in.OtherID))
			res.Score = &composed
		}
		results = append(results, res)
	}
	return results, nil
}
