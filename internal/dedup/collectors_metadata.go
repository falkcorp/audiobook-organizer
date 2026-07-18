// file: internal/dedup/collectors_metadata.go
// version: 1.1.1
// guid: e1f2a3b4-c5d6-4e7f-8a0b-1c2d3e4f5a6b
// last-edited: 2026-07-18

// Package dedup — metadata-based collector family (fable5 T014).
//
// # Design
//
// Two collector functions handle metadata-derived signals:
//
//   - CollectDuration: wraps the duration-gate logic from checkDurationMatch
//     (engine.go:547-700) into a SigDuration signal emitter.  The ±2% window,
//     Levenshtein title check, and the dedup:duration-match / dedup:duration-
//     abridged side-effect tags are ALL preserved verbatim — only the candidate
//     upsert is replaced by signal emission.
//
//   - CollectMetaFuzzy: NEW collector per SPEC 1 §3/TASK-014.  Computes
//     normalized title+author Levenshtein similarity between a query book and a
//     candidate set (embedding top-K + LSH candidates — never O(N²) title scan).
//     Emits SigMetaFuzzy with confidence scaled 0.70–0.85 over Levenshtein
//     similarity range 0.50–1.00.
//
// SigDuration is a SUPPORTING signal (boost only, never drives composite score
// above the candidate threshold on its own).  SigMetaFuzzy is a PRIMARY signal
// (participates in noisy-OR product).
//
// # Candidate source for CollectMetaFuzzy
//
// The candidate set comes from embedding top-K + LSH candidates only — never
// O(N²) title scan.  This matches the TASK-014 spec constraint:
// "candidate source = embedding top-K + LSH candidates ONLY (never O(N²)
// title scan)".  The caller is responsible for passing the candidate IDs; this
// collector is a pure function over that set.

package dedup

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// ─── store interfaces ──────────────────────────────────────────────────────────

// DurationCollectorStore is the subset of database.Store required by
// CollectDuration for the book-query and alt-title path.  Tag side-effects
// use the full database.Store passed separately so that
// database.EnsureSingletonBookTag (which requires the full Store interface)
// can be called without a type assertion.
//
// In production code, the caller passes the same *database.PebbleStore
// (or MockStore) for both parameters.
type DurationCollectorStore interface {
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
}

// MetaFuzzyStore is the subset of database.Store required by CollectMetaFuzzy.
type MetaFuzzyStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
}

// ─── duration collector ────────────────────────────────────────────────────────

// DurationCollectorConfig holds calibration parameters for CollectDuration.
type DurationCollectorConfig struct {
	// MatchTolerance is the maximum fractional difference in duration for a
	// match signal to be emitted.  Default 0.02 (2%).
	// Source: durationMatchTolerance constant in engine.go.
	MatchTolerance float64

	// AbridgedThreshold is the minimum fractional duration difference for a
	// pair to be tagged as "likely abridged/unabridged" rather than a
	// duplicate.  Default 0.20 (20%).
	// Source: durationAbridgedThreshold constant in engine.go.
	AbridgedThreshold float64

	// LevenshteinMax is the maximum normalized-title Levenshtein distance for
	// the duration signal to still emit.  Default 6.
	// Source: durationLevenshteinMax constant in engine.go.
	LevenshteinMax int
}

// DefaultDurationCollectorConfig returns the engine.go defaults.
func DefaultDurationCollectorConfig() DurationCollectorConfig {
	return DurationCollectorConfig{
		MatchTolerance:    durationMatchTolerance,    // 0.02 from engine.go
		AbridgedThreshold: durationAbridgedThreshold, // 0.20 from engine.go
		LevenshteinMax:    durationLevenshteinMax,    // 6 from engine.go
	}
}

// allNormalizedTitleFormsForStore is a free function equivalent of
// Engine.allNormalizedTitleForms for use in collectors that don't have an
// Engine reference.
func allNormalizedTitleFormsForStore(store interface {
	GetBookAlternativeTitles(bookID string) ([]database.BookAlternativeTitle, error)
}, book *database.Book) []string {
	forms := []string{normalizeTitle(book.Title)}
	seen := map[string]struct{}{forms[0]: {}}
	if store != nil {
		if alts, err := store.GetBookAlternativeTitles(book.ID); err == nil {
			for _, alt := range alts {
				norm := normalizeTitle(alt.Title)
				if norm == "" {
					continue
				}
				if _, dup := seen[norm]; dup {
					continue
				}
				seen[norm] = struct{}{}
				forms = append(forms, norm)
			}
		} else {
			slog.Info("dedup/collectors_metadata: alt title lookup for", "book", book.ID, "err", err)
		}
	}
	return forms
}

// CollectDuration scans books by the same author for duration-based similarity
// signals and emits SigDuration for pairs within the ±2% window (supporting
// signal — adds a bounded boost to composite score, never drives it alone).
//
// Side-effects preserved from checkDurationMatch (engine.go:547-700):
//
//   - dedup:duration-match tag applied to BOTH books when duration within ±2%
//     AND Levenshtein title distance ≤ cfg.LevenshteinMax.
//   - dedup:duration-abridged tag applied to BOTH books when duration differs
//     10-20% AND Levenshtein title distance ≤ cfg.LevenshteinMax.
//
// These side-effect tags are preserved because the UI and reporting layer
// depend on them for "show me likely abridged editions" filters.
//
// Logic unchanged from checkDurationMatch; emission shape only.
//
// tagStore is the database.Store used for side-effect tag writes; it must be
// non-nil if tag side-effects are desired (pass nil to disable).  In production
// the engine passes its bookStore field which is a database.Store superset.
func CollectDuration(
	store DurationCollectorStore,
	tagStore database.Store, // may be nil — side-effect tags silently skipped
	book *database.Book,
	cfg DurationCollectorConfig,
) ([]unified.Signal, error) {
	if book == nil {
		return nil, nil
	}
	if book.AuthorID == nil {
		return nil, nil
	}
	if book.Duration == nil || *book.Duration <= 0 {
		return nil, nil
	}
	if !hasUsableTitle(book.Title) {
		return nil, nil
	}

	othersCore, err := store.GetBooksByAuthorIDCore(*book.AuthorID)
	if err != nil {
		return nil, fmt.Errorf("CollectDuration get books by author: %w", err)
	}
	others := booksFromCore(othersCore)

	bookDur := float64(*book.Duration)
	bookNorm := normalizeTitle(book.Title)
	bookForms := allNormalizedTitleFormsForStore(store, book)
	bookSeriesNum := seriesNumberOf(book)

	var signals []unified.Signal
	// M2 (2026-07 error-correction sweep): EnsureSingletonBookTag failures
	// below used to be swallowed with `_ =`. Side-effect tags only — never
	// gate signal emission — so behavior stays log-and-continue; just count
	// and warn once per call instead of failing silently.
	tagErrs := 0

	for i := range others {
		other := &others[i]
		if other.ID == book.ID {
			continue
		}
		if other.Duration == nil || *other.Duration <= 0 {
			continue
		}
		if !hasUsableTitle(other.Title) {
			continue
		}

		otherDur := float64(*other.Duration)
		diff := bookDur - otherDur
		if diff < 0 {
			diff = -diff
		}
		base := bookDur
		if otherDur > base {
			base = otherDur
		}
		pct := diff / base

		// Short-circuit: completely unrelated durations (> 20% diff).
		if pct >= cfg.AbridgedThreshold {
			continue
		}

		otherForms := allNormalizedTitleFormsForStore(store, other)
		titleDist := minLevenshteinBetweenForms(bookForms, otherForms)
		otherNorm := normalizeTitle(other.Title)

		// Series-volume guard (same as checkDurationMatch).
		otherSeriesNum := seriesNumberOf(other)
		if bookSeriesNum != "" && otherSeriesNum != "" && bookSeriesNum != otherSeriesNum {
			continue
		}
		if titlesDifferOnlyInDigits(bookNorm, otherNorm) {
			continue
		}

		// Exact duration match (±2%) + recognizable title → emit SigDuration
		// as a supporting signal.
		if pct <= cfg.MatchTolerance && titleDist <= cfg.LevenshteinMax {
			// SigDuration carries the fractional duration closeness as Raw
			// (1.0 = identical duration, lower = more different but still
			// within tolerance).
			rawCloseness := 1.0 - pct/cfg.MatchTolerance
			signals = append(signals, unified.Signal{
				Kind:       unified.SigDuration,
				Raw:        rawCloseness,
				Confidence: 0, // supporting signal — ComposeScore uses boost, not confidence
				Evidence: fmt.Sprintf(
					"duration match %.2f%% difference: book %s (%.0fs) ↔ %s (%.0fs)",
					pct*100, book.ID, bookDur, other.ID, otherDur,
				),
			})

			// Side-effect: tag both books with dedup:duration-match so the
			// UI can filter "books the engine matched on duration signal".
			// Preserved verbatim from checkDurationMatch side-effect
			// (engine.go:655-658).
			if tagStore != nil {
				if err := database.EnsureSingletonBookTag(tagStore, book.ID, "dedup:duration-match", "dedup:duration-match", "system"); err != nil {
					tagErrs++
				}
				if err := database.EnsureSingletonBookTag(tagStore, other.ID, "dedup:duration-match", "dedup:duration-match", "system"); err != nil {
					tagErrs++
				}
			}
			continue
		}

		// Duration mismatch 10-20% with near-same title — likely abridged
		// edition.  No signal emitted but both sides get the tag.
		// Preserved verbatim from checkDurationMatch side-effect
		// (engine.go:672-675).
		if pct >= 0.10 && titleDist <= cfg.LevenshteinMax {
			if tagStore != nil {
				if err := database.EnsureSingletonBookTag(tagStore, book.ID, "dedup:duration-abridged", "dedup:duration-abridged", "system"); err != nil {
					tagErrs++
				}
				if err := database.EnsureSingletonBookTag(tagStore, other.ID, "dedup:duration-abridged", "dedup:duration-abridged", "system"); err != nil {
					tagErrs++
				}
			}
		}
	}

	if tagErrs > 0 {
		slog.Warn("dedup/collectors_metadata: EnsureSingletonBookTag errors", "book", book.ID, "errors", tagErrs)
	}

	return signals, nil
}

// ─── metadata-fuzzy collector ─────────────────────────────────────────────────

// MetaFuzzyConfig holds calibration parameters for CollectMetaFuzzy.
type MetaFuzzyConfig struct {
	// MinLevSimilarity is the minimum normalized Levenshtein similarity (0..1)
	// for a candidate to be accepted.  Values below this are rejected.
	// Default 0.50 (maps to min confidence 0.70).
	MinLevSimilarity float64

	// MinConfidence and MaxConfidence are the linear interpolation end-points
	// for the Confidence field of emitted signals.
	// Default: 0.70 and 0.85 per SPEC 1 §3.
	MinConfidence float64
	MaxConfidence float64
}

// DefaultMetaFuzzyConfig returns the SPEC 1 §3 defaults.
func DefaultMetaFuzzyConfig() MetaFuzzyConfig {
	return MetaFuzzyConfig{
		MinLevSimilarity: 0.50,
		MinConfidence:    0.70,
		MaxConfidence:    0.85,
	}
}

// metaFuzzyConfidence maps normalized Levenshtein similarity in
// [cfg.MinLevSimilarity, 1.00] to confidence in
// [cfg.MinConfidence, cfg.MaxConfidence] using linear interpolation.
func metaFuzzyConfidence(levSim float64, cfg MetaFuzzyConfig) float64 {
	if levSim <= cfg.MinLevSimilarity {
		return cfg.MinConfidence
	}
	if levSim >= 1.0 {
		return cfg.MaxConfidence
	}
	frac := (levSim - cfg.MinLevSimilarity) / (1.0 - cfg.MinLevSimilarity)
	return cfg.MinConfidence + frac*(cfg.MaxConfidence-cfg.MinConfidence)
}

// normalizedLevenshteinSimilarity computes a similarity score in [0, 1] for
// two strings: 1 - (levenshteinDistance / max(len(a), len(b))).
// Returns 1.0 for two equal strings and 0.0 when the strings are maximally
// different.  Two empty strings return 1.0.
func normalizedLevenshteinSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	la := len([]rune(a))
	lb := len([]rune(b))
	maxLen := la
	if lb > maxLen {
		maxLen = lb
	}
	if maxLen == 0 {
		return 1.0
	}
	dist := levenshteinDistance(a, b) // reuses engine.go's function
	sim := 1.0 - float64(dist)/float64(maxLen)
	if sim < 0 {
		sim = 0
	}
	return sim
}

// metaTitleAuthorSimilarity computes a combined title+author similarity score
// in [0, 1].  Title gets 70% weight, author 30% — titles are more distinctive
// for audiobooks (same author writes long series; different titles strongly
// discriminate).
//
// Both sides use normalized forms; titles use the minimum-distance form pair
// (to benefit from alternative-title matching).
func metaTitleAuthorSimilarity(
	bookTitleForms []string, bookAuthorNorm string,
	otherTitleForms []string, otherAuthorNorm string,
) float64 {
	// Find the closest title form pair.
	bestTitleSim := 0.0
	for _, bt := range bookTitleForms {
		for _, ot := range otherTitleForms {
			s := normalizedLevenshteinSimilarity(bt, ot)
			if s > bestTitleSim {
				bestTitleSim = s
			}
		}
	}

	authorSim := normalizedLevenshteinSimilarity(
		NormalizeAuthorName(bookAuthorNorm),
		NormalizeAuthorName(otherAuthorNorm),
	)

	return 0.70*bestTitleSim + 0.30*authorSim
}

// CollectMetaFuzzy emits SigMetaFuzzy signals for books in the candidate set
// that are similar to the query book based on normalized title+author
// Levenshtein similarity.
//
// # Candidate source (TASK-014 constraint)
//
// candidateIDs MUST come from embedding top-K + LSH probe candidates — never
// from an O(N²) title scan over all books.  The orchestrator in engine.go
// builds this set by collecting embedding and acoustid candidate entity IDs
// before calling this function.
//
// # Signal emission
//
// A SigMetaFuzzy signal is emitted for each candidate whose combined
// title+author similarity score is ≥ cfg.MinLevSimilarity.  The confidence
// is linearly scaled from cfg.MinConfidence (0.70) to cfg.MaxConfidence (0.85)
// over the similarity range [cfg.MinLevSimilarity, 1.00].
//
// WHY: metadata alone is not a near-certain duplicate indicator (it can match
// different editions), so the maximum confidence is capped at 0.85.  Combined
// with a high-cosine embedding signal via noisy-OR, the composite reaches
// the HIGH band (≥ 90).
func CollectMetaFuzzy(
	store MetaFuzzyStore,
	book *database.Book,
	bookAuthorName string, // pre-resolved author name for the query book
	candidateIDs []string, // embedding + LSH candidate book IDs (not O(N²))
	cfg MetaFuzzyConfig,
) ([]unified.Signal, error) {
	if book == nil || len(candidateIDs) == 0 {
		return nil, nil
	}
	if !hasUsableTitle(book.Title) {
		return nil, nil
	}

	bookTitleForms := allNormalizedTitleFormsForStore(store, book)

	var signals []unified.Signal
	// M2: GetBookByID errors here used to be a bare `continue` with no
	// counter, indistinguishable from "candidate no longer exists".
	lookupErrs := 0

	for _, candID := range candidateIDs {
		if candID == book.ID {
			continue
		}

		other, err := store.GetBookByID(candID)
		if err != nil {
			lookupErrs++
			continue
		}
		if other == nil {
			continue
		}
		if !hasUsableTitle(other.Title) {
			continue
		}

		// Resolve other book's author name.
		otherAuthorName := ""
		if other.AuthorID != nil {
			if a, aErr := store.GetAuthorByID(*other.AuthorID); aErr == nil && a != nil {
				otherAuthorName = a.Name
			}
		}

		otherTitleForms := allNormalizedTitleFormsForStore(store, other)
		sim := metaTitleAuthorSimilarity(
			bookTitleForms, bookAuthorName,
			otherTitleForms, otherAuthorName,
		)

		if sim < cfg.MinLevSimilarity {
			continue
		}

		conf := metaFuzzyConfidence(sim, cfg)
		signals = append(signals, unified.Signal{
			Kind:       unified.SigMetaFuzzy,
			Raw:        sim,
			Confidence: conf,
			Evidence: fmt.Sprintf(
				"metadata fuzzy title+author sim %.4f (conf %.4f): book %s ↔ %s",
				sim, conf, book.ID, other.ID,
			),
		})
	}

	if lookupErrs > 0 {
		slog.Warn("dedup/collectors_metadata: CollectMetaFuzzy candidate lookup errors",
			"book", book.ID, "errors", lookupErrs, "total_candidates", len(candidateIDs))
	}

	return signals, nil
}
