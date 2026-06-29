// file: internal/dedup/collectors_acoustid.go
// version: 1.1.0
// guid: a7b3c841-d9e2-4f15-88a0-5bc12e347f6d
// last-edited: 2026-06-28

// Package dedup — acoustic-ID collector family (fable5 T013).
//
// # Design
//
// Two exported collectors live here; T014 will wire them into the Engine:
//
//   - CollectExactAcoustID: exact-tier lookup via the book_file_acoustid: Pebble
//     secondary index.  O(1) per segment.  Emits SigExactAcoustID (conf 0.99).
//
//   - CollectLSHAcoustID: sub-linear candidate fan-out using the fpidx: secondary
//     index built by T012 (LSHProbe), followed by WholeFileSimilarity Hamming
//     refinement.  Gated on IsLSHIndexBuilt() — if the index has never been built
//     the function logs INFO and returns immediately; it NEVER falls back to the
//     retired O(N) GetBookFileByAcoustIDFuzzy path.
//
// Confidence scaling for SigLSHAcoustID (SPEC 1 §3, config-driven):
//
//	hamming 0.85 → conf 0.90  (MinConfidence)
//	hamming 1.00 → conf 0.97  (MaxConfidence)
//	linear interpolation between these two end-points.
//
// Both collectors are pure functions over injected store interfaces so they
// are trivially unit-testable with fixture data (no PebbleDB required).

package dedup

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// ─── store interfaces ──────────────────────────────────────────────────────────
// Narrow interfaces so tests can inject stubs without pulling in the whole
// database.Store.  T014's orchestrator passes a *database.PebbleStore (which
// implements both) when wiring the Engine.

// ExactAcoustIDStore is the subset of database.BookFileStore required by
// CollectExactAcoustID.
type ExactAcoustIDStore interface {
	// GetBookFileByAcoustID returns the BookFile whose book_file_acoustid: index
	// entry matches fp exactly, or nil when no match exists.
	GetBookFileByAcoustID(fp string) (*database.BookFile, error)
}

// LSHAcoustIDStore is the subset of database.PebbleStore required by
// CollectLSHAcoustID.  All three methods are implemented by *database.PebbleStore;
// mocks that satisfy this interface are sufficient for unit tests.
type LSHAcoustIDStore interface {
	// IsLSHIndexBuilt reports whether the fpidx: secondary index has been fully
	// built at least once.  When false, CollectLSHAcoustID skips and returns nil
	// — it must never fall back to O(N) scanning.
	IsLSHIndexBuilt() bool

	// LSHProbe performs point-lookups for the supplied subprints and returns the
	// candidate fileIDs that accumulate >= LSHMinBandHits band hits.
	LSHProbe(subs []fingerprint.Subprint, bands []byte, maxCandidates int) (map[string]int, error)

	// GetBookFileByID fetches a BookFile by (bookID, fileID).  bookID may be
	// empty when the caller does not know it — the PebbleStore implements a
	// scan fallback in that case.
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
}

// ─── exact-tier collector ─────────────────────────────────────────────────────

// CollectExactAcoustID probes the book_file_acoustid: secondary index for an
// exact fingerprint match for each segment string on queryFile.  For each hit
// that belongs to a different book it appends a SigExactAcoustID signal to the
// returned slice.
//
// A single file may carry up to 7 segment strings (AcoustIDSeg0..6, deprecated
// but still populated on pre-whole-file rows); this collector checks all of them
// so old data keeps working.
//
// queryBookID is used only to suppress self-matches (hits whose BookID equals
// queryBookID are skipped).
//
// Returns nil, nil when queryFile has no segment strings.  Errors from the
// index lookup are logged and skipped (never returned) — a missing index entry
// is not an error; a real DB failure produces a single soft skip.
func CollectExactAcoustID(
	store ExactAcoustIDStore,
	queryFile *database.BookFile,
	queryBookID string,
) ([]unified.Signal, error) {
	if queryFile == nil {
		return nil, nil
	}
	if knownShortFingerprintFile(*queryFile) {
		slog.Debug("dedup/collectors_acoustid: exact lookup skipped short clip fingerprint",
			"file_id", queryFile.ID,
			"duration_sec", queryFile.AcoustIDFingerprintDurationSec)
		return nil, nil
	}
	segs := [7]string{
		queryFile.AcoustIDSeg0,
		queryFile.AcoustIDSeg1,
		queryFile.AcoustIDSeg2,
		queryFile.AcoustIDSeg3,
		queryFile.AcoustIDSeg4,
		queryFile.AcoustIDSeg5,
		queryFile.AcoustIDSeg6,
	}

	var signals []unified.Signal
	seen := make(map[string]bool) // dedupe by candidate bookID in this call

	for _, seg := range segs {
		if seg == "" {
			continue
		}
		// Degenerate-fingerprint guard: the sentinel "AQAAAA" (all-zero
		// chromaprint) matches every other file carrying the same sentinel at
		// similarity 1.0.  Skip it here — the writer no longer emits it, but
		// old rows still carry it.
		if !fingerprint.IsUsefulFingerprint(seg) {
			continue
		}

		hit, err := store.GetBookFileByAcoustID(seg)
		if err != nil {
			// Index lookup failure: log and continue; one bad seg doesn't abort.
			slog.Debug("dedup/collectors_acoustid: exact lookup error",
				"file_id", queryFile.ID, "err", err)
			continue
		}
		if hit == nil || hit.BookID == queryBookID || seen[hit.BookID] {
			continue
		}
		if knownShortFingerprintFile(*hit) {
			slog.Debug("dedup/collectors_acoustid: exact lookup hit skipped short clip fingerprint",
				"file_id", queryFile.ID,
				"hit_file_id", hit.ID,
				"hit_duration_sec", hit.AcoustIDFingerprintDurationSec)
			continue
		}
		seen[hit.BookID] = true

		const conf = 0.99 // SigExactAcoustID fixed confidence (SPEC 1 §3)
		signals = append(signals, unified.Signal{
			Kind:       unified.SigExactAcoustID,
			Raw:        1.0,
			Confidence: conf,
			Evidence: fmt.Sprintf("exact acoustid match: file %s -> book %s",
				hit.ID, hit.BookID),
		})
	}
	return signals, nil
}

// ─── LSH probe collector ──────────────────────────────────────────────────────

// lshConfidenceScale maps a Hamming similarity in [minHamming, maxHamming] to a
// confidence in [minConf, maxConf] using linear interpolation.  Values outside
// the range are clamped to the nearest bound.
//
//	hamming == minHamming (0.85) → minConf (0.90 default)
//	hamming == maxHamming (1.00) → maxConf (0.97 default)
//
// Parameters come from LSHAcoustIDConfig so calibration is config-driven without
// requiring a code change (SPEC 1 §3).
func lshConfidenceScale(hamming, minHamming, maxHamming, minConf, maxConf float64) float64 {
	// Clamp inputs to the defined window.
	if hamming <= minHamming {
		return minConf
	}
	if hamming >= maxHamming {
		return maxConf
	}
	// Linear interpolation across the [minHamming, maxHamming] window.
	frac := (hamming - minHamming) / (maxHamming - minHamming)
	return minConf + frac*(maxConf-minConf)
}

// LSHAcoustIDConfig holds the calibration parameters for CollectLSHAcoustID.
// Callers that don't need custom tuning should call DefaultLSHAcoustIDConfig.
type LSHAcoustIDConfig struct {
	// MinHamming is the minimum Hamming similarity for a candidate to be
	// accepted after Hamming refinement.  Default 0.85.
	MinHamming float64

	// MaxCandidates caps the number of candidates returned by LSHProbe.
	// A value <= 0 disables the cap.  Default 200.
	MaxCandidates int

	// MinConfidence and MaxConfidence are the linear interpolation end-points
	// for the Confidence field of emitted signals.  Defaults: 0.90 and 0.97.
	MinConfidence float64
	MaxConfidence float64
}

// DefaultLSHAcoustIDConfig returns the SPEC 1 §3 default calibration.
func DefaultLSHAcoustIDConfig() LSHAcoustIDConfig {
	return LSHAcoustIDConfig{
		MinHamming:    0.85,
		MaxCandidates: 200,
		MinConfidence: 0.90,
		MaxConfidence: 0.97,
	}
}

// CollectLSHAcoustID performs the sub-linear AcoustID fan-out via the fpidx:
// secondary index (T012) and emits SigLSHAcoustID signals for candidate book
// files that survive the Hamming refinement step.
//
// # Gate: IsLSHIndexBuilt
//
// When the index has never been built (IsLSHIndexBuilt returns false), the
// function logs an INFO message and returns (nil, nil) immediately.  It does
// NOT fall back to GetBookFileByAcoustIDFuzzy — the O(N) path is retired.
// Callers that need near-duplicate candidates when the index is absent must
// schedule a lsh-index-build op first.
//
// # Probe -> Hamming pipeline
//
//  1. fingerprint.Subprints derives up to LSHBandCount (64) subprints from
//     queryFile.AcoustIDFingerprint.
//  2. LSHProbe performs one point-lookup per subprint and counts band hits per
//     candidate fileID; only candidates with >= LSHMinBandHits (2) are returned.
//  3. For each candidate, WholeFileSimilarity computes the Hamming similarity
//     over the trimmed middle slices.  Candidates below cfg.MinHamming (0.85)
//     are dropped.
//  4. Accepted candidates emit a Signal with Raw = hamming, Confidence =
//     lshConfidenceScale(hamming).
//
// queryBookID is used to suppress self-matches.  cfg controls calibration;
// pass DefaultLSHAcoustIDConfig() for spec defaults.
func CollectLSHAcoustID(
	store LSHAcoustIDStore,
	queryFile *database.BookFile,
	queryBookID string,
	cfg LSHAcoustIDConfig,
) ([]unified.Signal, error) {
	if queryFile == nil || len(queryFile.AcoustIDFingerprint) == 0 {
		// Nothing to probe — caller should log at the appropriate level.
		return nil, nil
	}
	if knownShortFingerprintFile(*queryFile) {
		slog.Debug("dedup/collectors_acoustid: lsh probe skipped short clip fingerprint",
			"file_id", queryFile.ID,
			"duration_sec", queryFile.AcoustIDFingerprintDurationSec)
		return nil, nil
	}

	// Gate: skip with INFO when the index has never been built.  This is the
	// permanent replacement for the ACOUSTID_FUZZY_ENABLED env variable — when
	// the index is absent we acknowledge it and return, rather than silently
	// degrading to O(N) scanning that can stall the registry.
	if !store.IsLSHIndexBuilt() {
		slog.Info("dedup/collectors_acoustid: LSH index not yet built; skipping LSH probe",
			"file_id", queryFile.ID,
			"hint", "run lsh-index-build op to enable sub-linear acoustid matching")
		return nil, nil
	}

	// Step 1: derive subprints from the whole-file fingerprint.
	subs, bands, err := fingerprint.Subprints(queryFile.AcoustIDFingerprint)
	if err != nil {
		return nil, fmt.Errorf("dedup/collectors_acoustid: subprints: %w", err)
	}
	if len(subs) == 0 {
		// Fingerprint too short to sample — not an error; nothing to probe.
		return nil, nil
	}

	// Step 2: LSH fan-out — point-lookups + band-hit filter.
	candidates, err := store.LSHProbe(subs, bands, cfg.MaxCandidates)
	if err != nil {
		return nil, fmt.Errorf("dedup/collectors_acoustid: lsh probe: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	slog.Debug("dedup/collectors_acoustid: lsh probe returned candidates",
		"file_id", queryFile.ID,
		"candidate_count", len(candidates))

	// Step 3 + 4: Hamming refinement and signal emission.
	var signals []unified.Signal
	seen := make(map[string]bool) // dedupe by candidate bookID

	for candFileID := range candidates {
		if candFileID == queryFile.ID {
			continue
		}

		// Fetch the candidate BookFile to get its whole-file fingerprint.
		// We pass "" as bookID because the caller may not know it; the
		// PebbleStore implementation handles the "" case with a scan fallback.
		candFile, err := store.GetBookFileByID("", candFileID)
		if err != nil {
			slog.Debug("dedup/collectors_acoustid: get candidate file error",
				"cand_file_id", candFileID, "err", err)
			continue
		}
		if candFile == nil {
			continue
		}
		if candFile.BookID == queryBookID || seen[candFile.BookID] {
			continue
		}
		if len(candFile.AcoustIDFingerprint) == 0 {
			// Candidate was indexed but fingerprint was subsequently cleared —
			// skip; stale index entry, harmless.
			continue
		}
		if knownShortFingerprintFile(*candFile) {
			slog.Debug("dedup/collectors_acoustid: lsh candidate skipped short clip fingerprint",
				"file_id", queryFile.ID,
				"cand_file_id", candFile.ID,
				"cand_duration_sec", candFile.AcoustIDFingerprintDurationSec)
			continue
		}

		// Hamming refinement: compare middle slices of the two fingerprints.
		hamming, simErr := fingerprint.WholeFileSimilarity(
			queryFile.AcoustIDFingerprint,
			candFile.AcoustIDFingerprint,
		)
		if simErr != nil {
			slog.Debug("dedup/collectors_acoustid: hamming similarity error",
				"query_file", queryFile.ID, "cand_file", candFileID, "err", simErr)
			continue
		}

		// Hamming-refine rejection: discard candidates below the threshold.
		// This is the primary noise filter after the LSH pre-filter step; a
		// low hamming similarity means the subprint collision was coincidental.
		if hamming < cfg.MinHamming {
			continue
		}

		seen[candFile.BookID] = true

		conf := lshConfidenceScale(hamming, cfg.MinHamming, 1.0, cfg.MinConfidence, cfg.MaxConfidence)
		signals = append(signals, unified.Signal{
			Kind:       unified.SigLSHAcoustID,
			Raw:        hamming,
			Confidence: conf,
			Evidence: fmt.Sprintf(
				"lsh acoustid: file %s -> book %s (hamming %.4f, conf %.4f)",
				candFile.ID, candFile.BookID, hamming, conf,
			),
		})
	}

	if len(signals) > 0 {
		slog.Debug("dedup/collectors_acoustid: lsh probe emitted signals",
			"file_id", queryFile.ID,
			"signal_count", len(signals))
	}

	return signals, nil
}
