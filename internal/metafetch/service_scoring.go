// file: internal/metafetch/service_scoring.go
// version: 1.6.0
// guid: d2226468-bed1-4989-93f3-b0bc3a344424
// last-edited: 2026-07-10

package metafetch

import (
	"context"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// isGarbageValue returns true if a string value is effectively useless metadata.
// IsGarbageValue returns true if the string looks like garbage metadata
// (e.g. hex-only, or other patterns known to be non-meaningful).
func IsGarbageValue(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	garbage := []string{"unknown", "narrator", "various", "n/a", "none", "null", "undefined", "",
		"test", "untitled", "no title", "no author", "various authors", "various artists"}
	for _, g := range garbage {
		if lower == g {
			return true
		}
	}
	// Reject HTML fragments or error messages that may leak from Wikipedia/API errors
	// Use anchored checks to avoid matching legitimate titles/authors containing "error" as a substring
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") ||
		strings.Contains(lower, "403 forbidden") || strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "error ") ||
		strings.Contains(lower, "http error") || strings.Contains(lower, "internal server error") {
		return true
	}
	return false
}

// isBetterValue returns true if newVal should replace oldVal.
// Never replaces a good value with garbage.
func IsBetterValue(oldVal, newVal string) bool {
	if IsGarbageValue(newVal) {
		return false
	}
	if IsGarbageValue(oldVal) {
		return true
	}
	// Both are real values; allow the update (fetched data may be more accurate)
	return true
}

// isBetterStringPtr returns true if newVal should replace the existing *string.
func IsBetterStringPtr(oldPtr *string, newVal string) bool {
	if IsGarbageValue(newVal) {
		return false
	}
	if oldPtr == nil || IsGarbageValue(*oldPtr) {
		return true
	}
	// Both are real values; allow the update
	return true
}

// computeF1Base returns just the F1 token-overlap portion of the score, with
// no penalties or bonuses applied. It's the "base score" contribution from
// the significantWords pathway, extracted so alternative scorers (embedding,
// LLM, reranker) can supply their own base score and reuse the shared
// non-base adjustment function.
func computeF1Base(r metadata.BookMetadata, searchWords map[string]bool) float64 {
	resultWords := SignificantWords(r.Title)
	if len(searchWords) == 0 || len(resultWords) == 0 {
		return 0
	}

	// Recall: how many search words appear in the result?
	recallHits := 0
	for w := range searchWords {
		if resultWords[w] {
			recallHits++
		}
	}
	recall := float64(recallHits) / float64(len(searchWords))

	// Precision: how many result words appear in the search?
	precHits := 0
	for w := range resultWords {
		if searchWords[w] {
			precHits++
		}
	}
	precision := float64(precHits) / float64(len(resultWords))

	if recall+precision == 0 {
		return 0
	}
	return 2 * recall * precision / (recall + precision)
}

// applyNonBaseAdjustments applies the compilation penalty, length penalty,
// and rich-metadata bonus to a base score. These adjustments are meaningful
// regardless of which scorer tier produced the base score and are applied
// identically on every path.
//
// baseWordCount is the number of significant words in the search title —
// used for the length penalty. Pass 0 to disable the length penalty (e.g.
// when the length ratio is meaningless for a non-token-overlap scorer).
// ApplyNonBaseAdjustments applies bonuses/penalties to a base similarity score
// based on metadata heuristics (series, narrator, language, etc.).
func ApplyNonBaseAdjustments(baseScore float64, r metadata.BookMetadata, baseWordCount int) float64 {
	score := baseScore

	// Compilation penalty
	if isCompilation(r.Title) {
		score *= 0.15
	}

	// Length penalty: penalise results that are much longer than the search.
	// Only applies when baseWordCount > 0 (the F1 path).
	if baseWordCount > 0 {
		resultWords := SignificantWords(r.Title)
		nSearch := float64(baseWordCount)
		nResult := float64(len(resultWords))
		if nResult > 1.5*nSearch {
			score *= (1.5 * nSearch) / nResult
		}
	}

	// Rich-metadata bonus (capped at +0.15, additive)
	bonus := 0.0
	if r.Description != "" {
		bonus += 0.05
	}
	if r.CoverURL != "" {
		bonus += 0.05
	}
	if r.Narrator != "" {
		bonus += 0.05
	}
	if r.ISBN != "" {
		bonus += 0.05
	}
	if bonus > 0.15 {
		bonus = 0.15
	}

	return score + bonus
}

// durationTiers is the ONE canonical, ratio-based classification of how
// closely a candidate's runtime matches a book's known duration. Both
// durationScoreMultiplier and computeDurationScore look up the same tier by
// ratio = |candidateDurationSec-bookDurationSec| / bookDurationSec, so they
// can never disagree about how close a duration match is (INIT-3-T2 —
// before this table existed the two functions used independent bucket
// systems — absolute-delta-seconds vs delta-ratio — that could and did
// disagree on the same pair; see golden fixtures in
// service_scoring_test.go for the divergent pre-unification behavior).
//
// Unknown semantics (handled by the callers below, NOT a table row): if
// either duration is <= 0, the comparison is UNKNOWN, never disqualifying —
// durationScoreMultiplier returns 1.0 (no adjustment) and
// computeDurationScore returns 0 (neutral).
//
// Tier boundaries are looked up by durationTier, which mirrors the historical
// computeDurationScore boundary semantics exactly: the first three tiers are
// upper-bound-EXCLUSIVE (ratio strictly less than MaxRatio) and the next two
// are upper-bound-INCLUSIVE (ratio <= MaxRatio), so a ratio landing exactly
// on 0.05/0.10/0.20 falls into the NEXT tier while one landing exactly on
// 0.50/1.00 stays in the CURRENT tier. This asymmetry is preserved on
// purpose: computeDurationScore's Score column must reproduce bit-for-bit
// what it already returned in production before this unification (see
// acceptance criteria — zero additive-score cells may change).
var durationTiers = []struct {
	MaxRatio   float64 // upper bound of |Δ|/bookDurationSec for this tier
	Multiplier float64 // durationScoreMultiplier result for this tier
	Score      float64 // computeDurationScore result for this tier
}{
	{MaxRatio: 0.05, Multiplier: 1.30, Score: 20},  // essentially identical — same edition
	{MaxRatio: 0.10, Multiplier: 1.20, Score: 15},  // very close — minor encoding difference
	{MaxRatio: 0.20, Multiplier: 1.10, Score: 10},  // close — probably correct
	{MaxRatio: 0.50, Multiplier: 1.00, Score: 0},   // acceptable range — no adjustment
	{MaxRatio: 1.00, Multiplier: 0.75, Score: -10}, // likely different edition, apply cautiously
	{MaxRatio: 0, Multiplier: 0.50, Score: -20},    // catch-all: ratio > 1.00 — almost certainly wrong book
}

// durationTier looks up the canonical (multiplier, score) pair for a
// duration-match ratio. See durationTiers for the boundary-inclusivity
// contract this switch implements.
func durationTier(ratio float64) (multiplier, score float64) {
	switch {
	case ratio < durationTiers[0].MaxRatio:
		return durationTiers[0].Multiplier, durationTiers[0].Score
	case ratio < durationTiers[1].MaxRatio:
		return durationTiers[1].Multiplier, durationTiers[1].Score
	case ratio < durationTiers[2].MaxRatio:
		return durationTiers[2].Multiplier, durationTiers[2].Score
	case ratio <= durationTiers[3].MaxRatio:
		return durationTiers[3].Multiplier, durationTiers[3].Score
	case ratio <= durationTiers[4].MaxRatio:
		return durationTiers[4].Multiplier, durationTiers[4].Score
	default:
		return durationTiers[5].Multiplier, durationTiers[5].Score
	}
}

// durationDeltaRatio computes the symmetric duration-match ratio shared by
// durationScoreMultiplier and computeDurationScore: |candidateDurationSec -
// bookDurationSec| / bookDurationSec. Callers must have already verified
// bookDurationSec > 0.
func durationDeltaRatio(bookDurationSec, candidateDurationSec int) float64 {
	delta := bookDurationSec - candidateDurationSec
	if delta < 0 {
		delta = -delta
	}
	return float64(delta) / float64(bookDurationSec)
}

// durationScoreMultiplier returns a score multiplier based on how closely the
// candidate's runtime matches the book's known duration, via a lookup into
// the canonical durationTiers table (shared with computeDurationScore).
//
// Both values are in seconds. If either is <= 0 (unknown), the multiplier is
// 1.0 (no adjustment) — UNKNOWN, never disqualifying. The multiplier is
// symmetric — only the ratio of the absolute delta to the book's duration
// matters, not the direction (candidate longer vs shorter).
//
// Ratio-based scale (monotonic in the match ratio; replaces the former
// absolute-delta-second buckets so the multiplier can never disagree with
// computeDurationScore's ratio-based additive score on the same pair):
//
//	ratio <  5%       → ×1.30  (essentially identical — almost certainly the same edition)
//	ratio <  5–10%     → ×1.20  (very close — same edition, minor encoding difference)
//	ratio < 10–20%     → ×1.10  (close — probably correct)
//	ratio ≤ 20–50%     → ×1.00  (acceptable range — no adjustment)
//	ratio ≤ 50–100%    → ×0.75  (likely different edition, apply cautiously)
//	ratio  > 100%      → ×0.50  (almost certainly wrong edition or different book)
func durationScoreMultiplier(bookDurationSec, candidateDurationSec int) float64 {
	if bookDurationSec <= 0 || candidateDurationSec <= 0 {
		return 1.0
	}
	multiplier, _ := durationTier(durationDeltaRatio(bookDurationSec, candidateDurationSec))
	return multiplier
}

// computeDurationScore returns an additive score component (in points) based on
// how closely a candidate's runtime matches the book's known duration. Unlike
// durationScoreMultiplier (which scales the overall score multiplicatively),
// this function produces a human-readable breakdown value that surfaces in the
// MetadataCandidate.DurationScore field. Both functions share the same
// canonical durationTiers lookup (see durationTier), so this Score column is
// unchanged from the pre-unification implementation.
//
// Both values are in seconds. If either is <= 0 (unknown), the result is 0.
// The delta ratio = |candidate_dur - book_dur| / book_dur:
//
//	ratio < 0.05  → +20  (within 5% — essentially the same edition)
//	ratio < 0.10  → +15  (within 10%)
//	ratio < 0.20  → +10  (within 20%)
//	ratio > 1.00  → -20  (more than 2× off — almost certainly wrong book)
//	ratio > 0.50  → -10  (more than 50% off — likely wrong edition)
//	otherwise      →   0  (neutral)
func computeDurationScore(bookDurationSec, candidateDurationSec int) float64 {
	if bookDurationSec <= 0 || candidateDurationSec <= 0 {
		return 0
	}
	_, score := durationTier(durationDeltaRatio(bookDurationSec, candidateDurationSec))
	return score
}

// pickBestMatchFromScored takes pre-computed base scores from any tier and
// returns the single best-matching result above the tier-appropriate
// threshold, applying the full stack of author/narrator/audiobook bonus
// multipliers. It's shared between the F1-only package-level
// bestTitleMatchWithContext and the scorer-backed bestTitleMatchForBook
// method, so the bonus logic lives in one place.
//
// baseScores must be aligned to results (same length, same order).
// baseTier drives the minimum score threshold and the length-penalty
// behavior inside applyNonBaseAdjustments: "f1" uses the historical 0.35
// threshold and applies the length penalty; other tiers (e.g. "embedding")
// use MetadataEmbeddingBestMatchMin (default 0.70) and disable the length
// penalty since their base scores have no token-overlap ratio.
//
// bookDurationSec is the book's known file duration in seconds (0 = unknown,
// disables the duration adjustment).
//
// For the F1 tier we preserve the historical "skip bonuses when base==0"
// behavior of scoreOneResult: a result whose F1 base is zero contributes a
// final score of zero, so it can never win regardless of rich-metadata
// bonuses or author/narrator multipliers. This keeps the package-level
// bestTitleMatchWithContext bit-for-bit equivalent to its pre-refactor
// implementation, which the existing test suite locks in.
// transcriptionHints carries the audio-derived (Whisper) title/author/narrator
// for a book. These come from the book's own intro narration, so a candidate
// that matches them is strong evidence of a correct match. Empty fields are
// ignored. Passed optionally (variadic) so existing callers stay unchanged.
type transcriptionHints struct {
	title, author, narrator string
}

func (th transcriptionHints) empty() bool {
	return th.title == "" && th.author == "" && th.narrator == ""
}

// containsCI reports whether a contains b (or vice-versa), case-insensitively.
// Mirrors the substring-both-ways comparison the curated author/narrator boosts
// already use, so transcription matching behaves consistently.
func containsCI(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	al, bl := strings.ToLower(a), strings.ToLower(b)
	return strings.Contains(al, bl) || strings.Contains(bl, al)
}

// transcriptionBoost multiplies score when a candidate matches the audio-derived
// fields. Multipliers stack on top of the curated-field boosts; scores are
// intentionally NOT clamped — bonuses add on top (see scoring memo). An exact
// (normalized) transcribed-title match is the strongest signal because the title
// is read aloud verbatim in the intro.
// Returns (adjusted score, true) when any boost was applied; (score, false) otherwise.
func transcriptionBoost(score float64, r metadata.BookMetadata, th transcriptionHints) (float64, bool) {
	// The transcribed TITLE is the anchor — it is read aloud verbatim in the
	// intro. The author/narrator boosts only apply when the title ALSO agrees;
	// otherwise a same-author, wrong-title candidate gets multiplied to the top
	// ("matches the author but not the actual book"). Audio author/narrator
	// agreement without a title match is a tiebreaker, not a score driver, so it
	// must not multiply the score on its own.
	titleHintPresent := th.title != ""
	titleMatched := false
	if titleHintPresent && r.Title != "" {
		if util.NormalizeTitle(r.Title) == util.NormalizeTitle(th.title) {
			score *= 2.0
			titleMatched = true
		} else if containsCI(r.Title, th.title) {
			score *= 1.4
			titleMatched = true
		}
	}
	// Suppress the author/narrator boosts only when we HAVE a transcribed title
	// that this candidate fails to match — that is the "matches the author but
	// not the actual book" case, where a same-author, wrong-title candidate must
	// not be carried to the top. When no transcribed title is available at all,
	// author/narrator agreement remains a legitimate tiebreaker on the
	// title-based F1 base.
	if titleHintPresent && !titleMatched {
		return score, false
	}
	boosted := titleMatched
	if th.author != "" && containsCI(r.Author, th.author) {
		score *= 1.6
		boosted = true
	}
	if th.narrator != "" && containsCI(r.Narrator, th.narrator) {
		score *= 1.4
		boosted = true
	}
	return score, boosted
}

// transcribedTitleAgrees reports whether a candidate title matches the book's
// audio-derived (transcribed) title — by exact normalized equality or a
// case-insensitive substring either way. Used as a hard gate before
// auto-applying metadata to a book that has a transcribed title, so an
// author-driven, wrong-title candidate can't overwrite good data.
func transcribedTitleAgrees(candidateTitle, transcribedTitle string) bool {
	if candidateTitle == "" || transcribedTitle == "" {
		return false
	}
	if util.NormalizeTitle(candidateTitle) == util.NormalizeTitle(transcribedTitle) {
		return true
	}
	return containsCI(candidateTitle, transcribedTitle)
}

func pickBestMatchFromScored(
	results []metadata.BookMetadata,
	baseScores []float64,
	baseTier string,
	searchWords map[string]bool,
	bookAuthor, bookNarrator string,
	bookDurationSec int,
	hints ...transcriptionHints,
) []metadata.BookMetadata {
	const f1MinScore = 0.35

	var th transcriptionHints
	if len(hints) > 0 {
		th = hints[0]
	}

	minScore := f1MinScore
	if baseTier != "f1" {
		minScore = config.AppConfig.MetadataScoring.EmbeddingBestMatch
	}

	bestIdx := -1
	bestScore := 0.0
	for i, r := range results {
		baseScore := baseScores[i]

		var score float64
		if baseTier == "f1" {
			// Preserve scoreOneResult's early-return-on-zero behavior so the
			// F1 path stays bit-for-bit identical to the pre-refactor code.
			if baseScore == 0 {
				continue
			}
			score = ApplyNonBaseAdjustments(baseScore, r, len(searchWords))
		} else {
			// Non-F1 tiers (embedding, etc.) skip the length penalty by
			// passing baseWordCount=0; the cosine-based base has no
			// token-overlap ratio for the penalty to be meaningful.
			score = ApplyNonBaseAdjustments(baseScore, r, 0)
		}

		// Author-based scoring: boost matches, penalize mismatches or missing.
		if bookAuthor != "" {
			if r.Author != "" {
				rAuthorLower := strings.ToLower(r.Author)
				bAuthorLower := strings.ToLower(bookAuthor)
				if strings.Contains(rAuthorLower, bAuthorLower) || strings.Contains(bAuthorLower, rAuthorLower) {
					score *= 1.5
				} else {
					score *= 0.7
				}
			} else {
				score *= 0.75
			}
		}

		// Narrator-based scoring: boost matches as secondary tiebreaker.
		if bookNarrator != "" && r.Narrator != "" {
			rNarrLower := strings.ToLower(r.Narrator)
			bNarrLower := strings.ToLower(bookNarrator)
			if strings.Contains(rNarrLower, bNarrLower) || strings.Contains(bNarrLower, rNarrLower) {
				score *= 1.3
			}
		}

		// Audiobook-specific: boost results with narrator, penalize without.
		if r.Narrator != "" {
			score *= 1.15
		} else {
			score *= 0.85
		}

		// Transcription-match boost: when the candidate matches the book's own
		// audio-derived title/author/narrator, that is strong evidence of a
		// correct match. No-op when no transcription hints were supplied.
		if !th.empty() {
			score, _ = transcriptionBoost(score, r, th)
		}

		// Duration-based scoring: compare candidate runtime against the book's
		// known file duration. Strong bonus when they match closely; penalty when
		// they diverge significantly (wrong edition, abridged vs. unabridged, etc.).
		score *= durationScoreMultiplier(bookDurationSec, r.DurationSec)

		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx >= 0 && bestScore >= minScore {
		return []metadata.BookMetadata{results[bestIdx]}
	}
	return nil
}

// scoreOneResult computes a quality score in [0, ~1.15] for a single result
// against a set of search-title significant words. It preserves the
// pre-refactor signature and behavior, composing computeF1Base and
// applyNonBaseAdjustments. Existing callers are unchanged.
func ScoreOneResult(r metadata.BookMetadata, searchWords map[string]bool) float64 {
	base := computeF1Base(r, searchWords)
	if base == 0 {
		return 0 // preserve original early-return behavior (skips bonus)
	}
	return ApplyNonBaseAdjustments(base, r, len(searchWords))
}

// allZero reports whether every score in the slice is exactly 0. A scorer
// (e.g. EmbeddingScorer) can return err == nil with a fully-populated but
// degenerate all-zero result — e.g. when every candidate's cached vector was
// stale (wrong embedding model/dimension) and CosineSimilarity silently
// returned 0 for each pair. Treating that as a successful "embedding" tier
// result would suppress the F1 fallback and drop every candidate below the
// downstream EmbeddingMinScore threshold, so ScoreBaseCandidates checks for
// it explicitly.
func allZero(scores []float64) bool {
	for _, s := range scores {
		if s != 0 {
			return false
		}
	}
	return true
}

// scoreBaseCandidates picks the highest-available base scorer tier and
// returns one base score per input result, aligned to input order, along
// with a short tier name for logs and UI badges ("embedding", "f1", ...).
//
// The fallback chain is:
//  1. If MetadataEmbeddingScoringEnabled AND a scorer is injected AND the
//     scorer succeeds → use those scores. Tier = scorer.Name().
//  2. Otherwise, compute F1 inline. Tier = "f1".
//
// Any scorer error is logged and falls through to the F1 tier. The search
// path must never fail because of a scorer problem — F1 is always reachable
// as a last resort since it only depends on the in-memory result data.
func (mfs *Service) ScoreBaseCandidates(
	ctx context.Context,
	book *database.Book,
	results []metadata.BookMetadata,
	searchWords map[string]bool,
) ([]float64, string) {
	if config.AppConfig.MetadataScoring.EmbeddingEnabled && mfs.metadataScorer != nil && len(results) > 0 {
		query := ai.Query{
			BookID:   book.ID,
			Title:    book.Title,
			Narrator: derefStr(book.Narrator),
		}
		if book.AuthorID != nil {
			if author, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
				query.Author = author.Name
			}
		}

		cands := make([]ai.Candidate, len(results))
		for i, r := range results {
			cands[i] = ai.Candidate{
				Title:    r.Title,
				Author:   r.Author,
				Narrator: r.Narrator,
			}
		}

		scores, err := mfs.metadataScorer.Score(ctx, query, cands)
		degenerate := len(scores) > 0 && allZero(scores)
		if err == nil && len(scores) == len(results) && !degenerate {
			return scores, mfs.metadataScorer.Name()
		}
		switch {
		case degenerate:
			slog.Warn("metadata-scorer returned all-zero scores, falling back to F1", "name", mfs.metadataScorer.Name(), "count", len(scores))
		case err != nil:
			slog.Warn("metadata-scorer failed, falling back to F1", "name", mfs.metadataScorer.Name(), "error", err)
		default:
			slog.Warn("metadata-scorer returned scores for results, falling back to F1", "name", mfs.metadataScorer.Name(), "scoreCount", len(scores), "resultCount", len(results))
		}
	}

	// F1 fallback tier.
	scores := make([]float64, len(results))
	for i, r := range results {
		scores[i] = computeF1Base(r, searchWords)
	}
	return scores, "f1"
}

// bestTitleMatchForBook is the scorer-aware sibling of
// bestTitleMatchWithContext. It routes through scoreBaseCandidates so
// callers that have a *database.Book in hand (e.g. the automatic metadata
// fetch paths) get embedding-based scoring when available, falling back
// silently to the F1 path when the scorer is disabled or errors.
//
// The package-level bestTitleMatch[WithContext] functions still exist and
// still use F1 — they're kept for the test suite and for code paths that
// don't have a Book in scope. This method is the preferred entry point
// for production call sites that do.
func (mfs *Service) bestTitleMatchForBook(
	book *database.Book,
	results []metadata.BookMetadata,
	bookAuthor, bookNarrator string,
	titles ...string,
) []metadata.BookMetadata {
	// Union of significant words from all title variants. Needed by both
	// the F1 fallback path (via scoreBaseCandidates) and by
	// pickBestMatchFromScored for the length penalty.
	searchWords := map[string]bool{}
	for _, t := range titles {
		for w := range SignificantWords(t) {
			searchWords[w] = true
		}
	}

	baseScores, baseTier := mfs.ScoreBaseCandidates(context.Background(), book, results, searchWords)
	bookDurationSec := 0
	if book.Duration != nil {
		bookDurationSec = *book.Duration
	}
	return pickBestMatchFromScored(results, baseScores, baseTier, searchWords, bookAuthor, bookNarrator, bookDurationSec, hintsFromBook(book))
}

// hintsFromBook extracts the audio-derived transcription fields from a book for
// transcription-match scoring. Garbage values are dropped so a noisy transcript
// can't skew the boost.
func hintsFromBook(book *database.Book) transcriptionHints {
	clean := func(p *string) string {
		if p == nil {
			return ""
		}
		s := strings.TrimSpace(*p)
		if s == "" || IsGarbageValue(s) {
			return ""
		}
		return s
	}
	return transcriptionHints{
		title:    clean(book.TranscribedTitle),
		author:   clean(book.TranscribedAuthor),
		narrator: clean(book.TranscribedNarrator),
	}
}

// rerankTopK asks the LLM scorer to re-judge the ambiguous top candidates
// after the base scorer has produced initial rankings. "Ambiguous" means
// candidates whose Score lands within MetadataLLMRerankEpsilon of the best
// candidate's Score. At most MetadataLLMRerankTopK candidates are sent to
// the LLM, even if more fall inside the epsilon window, to cap per-search
// cost.
//
// On success, the returned slice is the same candidates with updated Score
// values for the top-K slots, re-sorted descending by Score. On any failure
// (LLM disabled, backend error, fewer than 2 ambiguous candidates to resolve)
// the input slice is returned unchanged so the search path degrades cleanly.
func (mfs *Service) RerankTopK(
	ctx context.Context,
	book *database.Book,
	candidates []MetadataCandidate,
) []MetadataCandidate {
	if len(candidates) < 2 || mfs.llmScorer == nil {
		return candidates
	}

	// Sort descending by current score so the "ambiguous top" is contiguous
	// at index 0.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	epsilon := config.AppConfig.MetadataScoring.LLMRerankEpsilon
	topK := config.AppConfig.MetadataScoring.LLMRerankTopK
	if topK <= 0 {
		topK = 5
	}

	bestScore := candidates[0].Score
	ambiguousEnd := 1
	for ambiguousEnd < len(candidates) && ambiguousEnd < topK {
		if bestScore-candidates[ambiguousEnd].Score > epsilon {
			break
		}
		ambiguousEnd++
	}
	if ambiguousEnd < 2 {
		// Only one candidate within epsilon — nothing to resolve.
				slog.Debug("metadata-search rerank skipped — only 1 candidate within epsilon of best", "epsilon", epsilon, "bestScore", bestScore)
		return candidates
	}

	topCands := candidates[:ambiguousEnd]
		slog.Debug("metadata-search rerank firing on top candidates", "count", len(topCands), "epsilon", epsilon, "bestScore", bestScore)

	// Resolve the book's author name for the query payload.
	authorName := ""
	if book.AuthorID != nil {
		if author, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			authorName = author.Name
		}
	}
	query := ai.Query{
		BookID:   book.ID,
		Title:    book.Title,
		Author:   authorName,
		Narrator: derefStr(book.Narrator),
	}

	llmCands := make([]ai.Candidate, len(topCands))
	for i, c := range topCands {
		llmCands[i] = ai.Candidate{
			Title:    c.Title,
			Author:   c.Author,
			Narrator: c.Narrator,
		}
	}

	llmScores, err := mfs.llmScorer.Score(ctx, query, llmCands)
	if err != nil || len(llmScores) != len(topCands) {
		if err != nil {
						slog.Warn("metadata-search rerank LLM call failed, keeping base scores", "error", err)
		} else {
						slog.Warn("metadata-search rerank returned scores for candidates, keeping base scores", "llmScoreCount", len(llmScores), "candidateCount", len(topCands))
		}
		return candidates
	}

	// Replace top-K base scores with LLM scores — but rescale them back into
	// the window's original [origMin, origMax] range first. The LLM scores
	// come back hard-clamped to [0,1] (see LLMScorer.Score), while the
	// untouched tail carries the full unclamped boost-multiplier stack
	// (author/narrator/series, routinely 1.5-4.0 by design). Assigning the
	// clamped LLM score directly would compare apples to oranges once the
	// full list is re-sorted below: a tail candidate scoring >1.0 could
	// outrank even an LLM-certain (1.0) reranked winner. Rescaling preserves
	// the LLM's relative ranking within the window while keeping the window
	// on the same scale as the tail.
	//
	// Do not apply the author/narrator/series bonus multipliers again when
	// rescaling. The LLM prompt already sees those fields and judges them as
	// part of its score; re-multiplying would double-count the same
	// evidence.
	origMax := bestScore
	origMin := candidates[ambiguousEnd-1].Score
	for i := range topCands {
		normFinal := llmScores[i]
		if origMax == origMin {
			// No spread in the original window — every candidate rescales
			// to the same point.
			candidates[i].Score = origMax
			continue
		}
		candidates[i].Score = origMin + normFinal*(origMax-origMin)
	}

	// Resort the full list so the reranked top-K is in correct order against
	// the untouched tail.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	return candidates
}

// applySeriesPositionFilter rejects the top result if it claims a different
// series position than the book's known position. If the result has no
// SeriesPosition or the book has no known position, results pass through.
func ApplySeriesPositionFilter(
	results []metadata.BookMetadata,
	knownPosition int,
) []metadata.BookMetadata {
	if len(results) == 0 || knownPosition <= 0 {
		return results
	}
	wantPos := strconv.Itoa(knownPosition)
	best := results[0]
	if best.SeriesPosition != "" && best.SeriesPosition != wantPos {
				slog.Debug("scorer rejecting result (series position ! expected )", "title", best.Title, "seriesPosition", best.SeriesPosition, "wantPos", wantPos)
		return nil
	}
	return results
}

// bestTitleMatch filters results to find the single best match for the given
// title variants using precision+recall+penalty scoring.
//
// It replaces the old recall-only word-overlap function. A result must score
// at least 0.35 to be returned; if none qualify, nil is returned so the
// caller can fall through to the next source or report "no metadata found".
func BestTitleMatch(results []metadata.BookMetadata, titles ...string) []metadata.BookMetadata {
	return BestTitleMatchWithContext(results, "", "", titles...)
}
func BestTitleMatchWithContext(results []metadata.BookMetadata, bookAuthor, bookNarrator string, titles ...string) []metadata.BookMetadata {
	// Union of significant words from all title variants.
	searchWords := map[string]bool{}
	for _, t := range titles {
		for w := range SignificantWords(t) {
			searchWords[w] = true
		}
	}

	// F1 base scores aligned to results — the helper applies bonuses,
	// multipliers, and the 0.35 threshold for the "f1" tier.
	baseScores := make([]float64, len(results))
	for i, r := range results {
		baseScores[i] = computeF1Base(r, searchWords)
	}

	return pickBestMatchFromScored(results, baseScores, "f1", searchWords, bookAuthor, bookNarrator, 0)
}

var scoreTitleStop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"that": true, "this": true, "are": true, "was": true, "were": true,
	"been": true, "have": true, "has": true, "had": true, "not": true,
	"but": true, "its": true, "our": true, "your": true, "their": true,
	"all": true, "any": true, "can": true, "will": true, "may": true,
	"into": true,
}
var compilationRe = regexp.MustCompile(`\b\d+\s+books\b`)
var compilationPhrases = []string{
	"box set", "boxset", "box-set",
	"collection",
	"complete series", "complete collection",
	"books set", "book set",
	"omnibus",
	"anthology",
	"compendium",
	"series collection", "series set",
}
var trailingNumberRe = regexp.MustCompile(
	`(?i)(?:,?\s*(?:book|volume|vol\.?|part|pt\.?|#)\s*)?(\d+(?:\.\d+)?)\s*(?:\(.*\))?\s*$`)

var seriesNumRe = regexp.MustCompile(`(\d+(?:\.\d+)?)`)
