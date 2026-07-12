// file: internal/plugins/metafetch/calibrate_scoring.go
// version: 1.1.0
// guid: 4e1c8b2a-6d90-4f37-9b1a-2c3d4e5f6a70
// last-edited: 2026-07-11

// Package metafetch — op metafetch.calibrate-scoring (INIT-3-T1).
//
// # Why this op exists
//
// The metadata-candidate scorer's weights (transcription/series boosts, the
// compilation penalty, the rich-metadata bonus, the duration-tier table, the
// F1 floor) were extracted into config.AppConfig.MetadataScoring by TASK-02 so
// an operator can retune them. But nothing tells an operator whether a change
// would help: the only ground truth for "the scorer picked the right
// candidate" is the historical record of which candidate was actually applied.
//
// This op is a READ-ONLY calibration harness. It replays every persisted
// metadata-candidate cache (MetadataCandidateCache) against the book it belongs
// to, uses that book's MetadataSourceHash (stamped at metadata-apply time in
// internal/metafetch/service_apply.go as sha256("{source}:{canonical_id}")) to
// identify which cached candidate was applied, re-ranks the cached candidates
// under the current scoring knobs AND under a small sweep grid around each
// knob, and REPORTS the top-1 accuracy (fraction of books whose applied
// candidate ranks first) at each sweep point. It NEVER writes config, never
// mutates a book, a cache, or a candidate. Mirrors the READ-ONLY discipline of
// dedup.calibrate-embedding-thresholds: reports only; applying a knob change is
// an owner-gated follow-up performed by editing config, not by this op.
//
// # Circularity caveat (reviewed; emitted verbatim in the report)
//
// Most applied candidates were CHOSEN by the current scorer, so top-1 accuracy
// over the full applied set is biased toward re-deriving today's weights.
// Results are segmented by apply origin where determinable: books carrying a
// manual metadata-field override (recorded with source "manual" in the
// metadata-field-state store) are treated as the manual/override segment — the
// primary NON-circular signal — and the remainder as the auto segment. If no
// manual segment is determinable, the report says so and flags the whole sweep
// as circular-biased.
//
// # Scorer fidelity (stated in the report)
//
// The harness scores with a self-contained, knob-parameterized re-implementation
// of the metafetch scoring formulas (f1 base + non-base adjustments + duration
// tiers + transcription boosts + series-number boosts). Sweeping the real
// scorer directly is not possible without mutating the process-global
// config.AppConfig, which would be unsafe under the pooled replay loop; so the
// core (f1 base + non-base adjustments) is pinned bit-for-bit against the real
// exported metafetch.ScoreOneResult at default knobs by a unit test
// (calibrate_scoring_test.go), and the remaining per-knob layers mirror the
// exact formulas in internal/metafetch/service_scoring.go. The composition
// order of those layers is harness-defined and NOT bit-identical to the
// production ScoreBaseCandidates orchestration, so recommendations are
// directional, one-factor-at-a-time (OFAT) signals for v1, not turnkey values.
//
// Usage:
//
//	POST /api/v1/operations/v2  {"def_id":"metafetch.calibrate-scoring"}
//	POST /api/v1/operations/v2  {"def_id":"metafetch.calibrate-scoring",
//	                             "params":{"sample_limit":2000,"sweep_steps":4}}
package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	// defaultSampleLimit bounds how many applied books are pulled into the
	// evaluation set. 0 = no limit (evaluate every applied book).
	defaultSampleLimit = 5000
	// defaultSweepSteps is the number of grid points swept per knob.
	defaultSweepSteps = 4
	// maxSweepSteps guards against a pathological params value.
	maxSweepSteps = 25
	// enumPageSize is the cursor page size used to enumerate applied books.
	enumPageSize = 1000
	// sweepFactorLo/Hi bound the one-factor-at-a-time multiplicative grid around
	// each knob's current value: most knobs sweep [0.5×, 1.5×] current, while the
	// zero-based pointer knobs (see zeroBasedKnobs) sweep [0, 1.5×] so 0 — a
	// reachable, non-clamped operator value (spec C2) — can be recommended.
	sweepFactorLo = 0.5
	sweepFactorHi = 1.5
)

// circularityCaveat is emitted verbatim in the report so a consumer can never
// read a top-1 number without the bias warning attached.
const circularityCaveat = "Most applied candidates were CHOSEN by the current scorer, so top-1 accuracy over the full applied set is biased toward re-deriving today's weights. The manual/override segment is the primary non-circular signal; the auto segment and the overall figure are circular-biased. If the manual segment is empty, treat the entire sweep as circular-biased."

// calibrateScoringParams are the JSON parameters accepted by the op.
type calibrateScoringParams struct {
	// SampleLimit caps the evaluation-set size (applied books with a cache).
	// Optional; 0 or omitted defaults to defaultSampleLimit. The cap takes the
	// first-N applied books in PebbleDB key order (not a random sample) — an
	// ordering bias noted in the report's coverage caveat. Pass a large value to
	// evaluate the whole library.
	SampleLimit int `json:"sample_limit"`
	// SweepSteps is the number of grid points per knob. Optional; defaults to
	// defaultSweepSteps, clamped to [1, maxSweepSteps].
	SweepSteps int `json:"sweep_steps"`
}

// calibrateScoringDef returns the OperationDef for metafetch.calibrate-scoring.
func (p *Plugin) calibrateScoringDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "metafetch.calibrate-scoring",
		Plugin:      "metafetch",
		DisplayName: "Calibrate metadata scoring (report only)",
		Description: "Read-only INIT-3-T1 harness: replays persisted metadata-candidate caches " +
			"against applied books (MetadataSourceHash ground truth), sweeps the TASK-02 scoring " +
			"knobs one-factor-at-a-time, and reports top-1 accuracy per sweep point. Writes " +
			"nothing — applying a knob change is an owner-gated config edit.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "metafetch.calibrate-scoring",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runCalibrateScoring,
	}
}

// ---------------------------------------------------------------------------
// Knobs
// ---------------------------------------------------------------------------

// durationTierCount is the number of duration tiers (mirrors the fixed-length
// durationTiers table in internal/metafetch/service_scoring.go).
const durationTierCount = 6

// sweepKnobs is a fully-resolved (no pointers, no nil) snapshot of the TASK-02
// scoring knobs. Every field maps 1:1 to config.MetadataScoringConfig; the
// resolution rules (pointer-nil → default, slice-length-mismatch → default)
// mirror internal/metafetch/service_scoring.go's scoringKnobs exactly.
type sweepKnobs struct {
	TranscriptionTitleExactBoost  float64
	TranscriptionTitleSubstrBoost float64
	TranscriptionAuthorBoost      float64
	TranscriptionNarratorBoost    float64

	CompilationPenalty     float64
	RichMetadataFieldBonus float64
	RichMetadataBonusCap   float64
	F1MinScore             float64

	SeriesNameMatchBoost     float64
	SeriesNumberExactBoost   float64
	SeriesNumberWrongPenalty float64

	DurationTierMultipliers [durationTierCount]float64
	DurationTierScores      [durationTierCount]float64
}

// defaultDurationTierMultipliers / defaultDurationTierScores mirror the
// Multiplier/Score columns of the durationTiers table in
// internal/metafetch/service_scoring.go.
var (
	defaultDurationTierMultipliers = [durationTierCount]float64{1.30, 1.20, 1.10, 1.00, 0.75, 0.50}
	defaultDurationTierScores      = [durationTierCount]float64{20, 15, 10, 0, -10, -20}
)

// defaultSweepKnobs returns the built-in literal defaults — identical to what
// scoringKnobs() resolves from a zero-value MetadataScoringConfig.
func defaultSweepKnobs() sweepKnobs {
	return sweepKnobs{
		TranscriptionTitleExactBoost:  2.0,
		TranscriptionTitleSubstrBoost: 1.4,
		TranscriptionAuthorBoost:      1.6,
		TranscriptionNarratorBoost:    1.4,
		CompilationPenalty:            0.15,
		RichMetadataFieldBonus:        0.05,
		RichMetadataBonusCap:          0.15,
		F1MinScore:                    0.35,
		SeriesNameMatchBoost:          1.4,
		SeriesNumberExactBoost:        2.0,
		SeriesNumberWrongPenalty:      0.5,
		DurationTierMultipliers:       defaultDurationTierMultipliers,
		DurationTierScores:            defaultDurationTierScores,
	}
}

// knobsFromConfig resolves the current process config into a sweepKnobs,
// mirroring scoringKnobs()'s fail-open semantics (a 0/nil/mismatched field
// falls back to the built-in literal, so an untuned deployment reproduces
// today's behavior).
func knobsFromConfig(cfg config.MetadataScoringConfig) sweepKnobs {
	k := defaultSweepKnobs()
	if cfg.TranscriptionTitleExactBoost != 0 {
		k.TranscriptionTitleExactBoost = cfg.TranscriptionTitleExactBoost
	}
	if cfg.TranscriptionTitleSubstrBoost != 0 {
		k.TranscriptionTitleSubstrBoost = cfg.TranscriptionTitleSubstrBoost
	}
	if cfg.TranscriptionAuthorBoost != 0 {
		k.TranscriptionAuthorBoost = cfg.TranscriptionAuthorBoost
	}
	if cfg.TranscriptionNarratorBoost != 0 {
		k.TranscriptionNarratorBoost = cfg.TranscriptionNarratorBoost
	}
	if cfg.RichMetadataFieldBonus != 0 {
		k.RichMetadataFieldBonus = cfg.RichMetadataFieldBonus
	}
	if cfg.SeriesNameMatchBoost != 0 {
		k.SeriesNameMatchBoost = cfg.SeriesNameMatchBoost
	}
	if cfg.SeriesNumberExactBoost != 0 {
		k.SeriesNumberExactBoost = cfg.SeriesNumberExactBoost
	}
	if cfg.SeriesNumberWrongPenalty != 0 {
		k.SeriesNumberWrongPenalty = cfg.SeriesNumberWrongPenalty
	}
	// Pointer knobs: nil = unset (fall back to default); a present pointer is
	// honored even when it points at 0 (0 is a legitimate operator value).
	if cfg.CompilationPenalty != nil {
		k.CompilationPenalty = *cfg.CompilationPenalty
	}
	if cfg.RichMetadataBonusCap != nil {
		k.RichMetadataBonusCap = *cfg.RichMetadataBonusCap
	}
	if cfg.F1MinScore != nil {
		k.F1MinScore = *cfg.F1MinScore
	}
	// Duration tiers: both arrays must be exactly durationTierCount long or the
	// built-in table is used unmodified.
	if len(cfg.DurationTierMultipliers) == durationTierCount && len(cfg.DurationTierScores) == durationTierCount {
		for i := 0; i < durationTierCount; i++ {
			k.DurationTierMultipliers[i] = cfg.DurationTierMultipliers[i]
			k.DurationTierScores[i] = cfg.DurationTierScores[i]
		}
	}
	return k
}

// ---------------------------------------------------------------------------
// Knob-parameterized scoring (replica of internal/metafetch/service_scoring.go)
// ---------------------------------------------------------------------------

// candFields is the subset of a candidate's fields the scorer reads. Both
// metafetch.MetadataCandidate and metadata.BookMetadata map onto it (the latter
// only in the pin test), so the scorer is independent of either concrete type.
type candFields struct {
	Title          string
	Author         string
	Narrator       string
	Description    string
	CoverURL       string
	ISBN           string
	Series         string
	SeriesPosition string
	DurationSec    int
}

func fieldsFromCandidate(c metafetch.MetadataCandidate) candFields {
	return candFields{
		Title:          c.Title,
		Author:         c.Author,
		Narrator:       c.Narrator,
		Description:    c.Description,
		CoverURL:       c.CoverURL,
		ISBN:           c.ISBN,
		Series:         c.Series,
		SeriesPosition: c.SeriesPosition,
		DurationSec:    c.DurationSec,
	}
}

// compilationRe / compilationPhrases mirror service_scoring.go exactly. Kept in
// sync by the pin test (a "Box Set" candidate exercises this path).
var scoreCalibCompilationRe = regexp.MustCompile(`\b\d+\s+books\b`)

var scoreCalibCompilationPhrases = []string{
	"box set", "boxset", "box-set",
	"collection",
	"complete series", "complete collection",
	"books set", "book set",
	"omnibus",
	"anthology",
	"compendium",
	"series collection", "series set",
}

func isCompilationTitle(title string) bool {
	lower := strings.ToLower(title)
	for _, phrase := range scoreCalibCompilationPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return scoreCalibCompilationRe.MatchString(lower)
}

// trailingNumberRe / seriesNumRe mirror service_scoring.go's series-number
// extraction (service_search.go applies the exact/wrong boosts from these).
var (
	scoreCalibTrailingNumberRe = regexp.MustCompile(`(?i)(?:,?\s*(?:book|volume|vol\.?|part|pt\.?|#)\s*)?(\d+(?:\.\d+)?)\s*(?:\(.*\))?\s*$`)
	scoreCalibSeriesNumRe      = regexp.MustCompile(`(\d+(?:\.\d+)?)`)
)

func trailingNumber(title string) string {
	clean := regexp.MustCompile(`(?i)\s*\((un)?abridged\)\s*$`).ReplaceAllString(title, "")
	clean = regexp.MustCompile(`\s*\[.*?\]\s*$`).ReplaceAllString(clean, "")
	clean = strings.TrimSpace(clean)
	m := scoreCalibTrailingNumberRe.FindStringSubmatch(clean)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func normalizeSeriesNumber(pos string) string {
	m := scoreCalibSeriesNumRe.FindStringSubmatch(pos)
	if len(m) >= 2 {
		if strings.HasSuffix(m[1], ".0") {
			return strings.TrimSuffix(m[1], ".0")
		}
		return m[1]
	}
	return ""
}

// f1Base mirrors computeF1Base: token-overlap F1 of the candidate title against
// the search words, using metafetch.SignificantWords for both sides.
func f1Base(title string, searchWords map[string]bool) float64 {
	resultWords := metafetch.SignificantWords(title)
	if len(searchWords) == 0 || len(resultWords) == 0 {
		return 0
	}
	recallHits := 0
	for w := range searchWords {
		if resultWords[w] {
			recallHits++
		}
	}
	recall := float64(recallHits) / float64(len(searchWords))
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

// nonBaseAdjust mirrors ApplyNonBaseAdjustments: compilation penalty, length
// penalty, and capped rich-metadata bonus, all parameterized by k.
func nonBaseAdjust(base float64, f candFields, baseWordCount int, k sweepKnobs) float64 {
	score := base
	if isCompilationTitle(f.Title) {
		score *= k.CompilationPenalty
	}
	if baseWordCount > 0 {
		resultWords := metafetch.SignificantWords(f.Title)
		nSearch := float64(baseWordCount)
		nResult := float64(len(resultWords))
		if nResult > 1.5*nSearch {
			score *= (1.5 * nSearch) / nResult
		}
	}
	bonus := 0.0
	if f.Description != "" {
		bonus += k.RichMetadataFieldBonus
	}
	if f.CoverURL != "" {
		bonus += k.RichMetadataFieldBonus
	}
	if f.Narrator != "" {
		bonus += k.RichMetadataFieldBonus
	}
	if f.ISBN != "" {
		bonus += k.RichMetadataFieldBonus
	}
	if bonus > k.RichMetadataBonusCap {
		bonus = k.RichMetadataBonusCap
	}
	return score + bonus
}

// scoreCore mirrors metafetch.ScoreOneResult: the f1 base plus non-base
// adjustments, preserving the "base == 0 → 0 (skip bonus)" early return. This
// is the portion pinned bit-for-bit against the real scorer at default knobs.
func scoreCore(f candFields, searchWords map[string]bool, k sweepKnobs) float64 {
	base := f1Base(f.Title, searchWords)
	if base == 0 {
		return 0
	}
	return nonBaseAdjust(base, f, len(searchWords), k)
}

// durationTierValues mirrors durationTier's tier lookup (fixed edges, swept
// Multiplier/Score values).
func durationTierValues(ratio float64, k sweepKnobs) (multiplier, score float64) {
	switch {
	case ratio < 0.05:
		return k.DurationTierMultipliers[0], k.DurationTierScores[0]
	case ratio < 0.10:
		return k.DurationTierMultipliers[1], k.DurationTierScores[1]
	case ratio < 0.20:
		return k.DurationTierMultipliers[2], k.DurationTierScores[2]
	case ratio <= 0.50:
		return k.DurationTierMultipliers[3], k.DurationTierScores[3]
	case ratio <= 1.00:
		return k.DurationTierMultipliers[4], k.DurationTierScores[4]
	default:
		return k.DurationTierMultipliers[5], k.DurationTierScores[5]
	}
}

// transcriptionHints carries the audio-derived title/author/narrator for a book.
type transcriptionHints struct{ title, author, narrator string }

func containsCI(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	al, bl := strings.ToLower(a), strings.ToLower(b)
	return strings.Contains(al, bl) || strings.Contains(bl, al)
}

// normalizeTitleForTranscription mirrors util.NormalizeTitle's role in
// transcriptionBoost's exact-title comparison: lowercase + collapse to
// significant-word tokens. metafetch.SignificantWords already lowercases and
// drops stop/punctuation, so equal significant-word sets => normalized-equal.
func normalizeTitleTokens(s string) string {
	words := metafetch.SignificantWords(s)
	toks := make([]string, 0, len(words))
	for w := range words {
		toks = append(toks, w)
	}
	sort.Strings(toks)
	return strings.Join(toks, " ")
}

// transcriptionBoost mirrors service_scoring.go's transcriptionBoost: the
// transcribed title is the anchor; author/narrator boosts apply only when the
// title also agrees (or no transcribed title is present).
func transcriptionBoost(score float64, f candFields, th transcriptionHints, k sweepKnobs) float64 {
	titleHintPresent := th.title != ""
	titleMatched := false
	if titleHintPresent && f.Title != "" {
		if normalizeTitleTokens(f.Title) == normalizeTitleTokens(th.title) {
			score *= k.TranscriptionTitleExactBoost
			titleMatched = true
		} else if containsCI(f.Title, th.title) {
			score *= k.TranscriptionTitleSubstrBoost
			titleMatched = true
		}
	}
	if titleHintPresent && !titleMatched {
		return score
	}
	if th.author != "" && containsCI(f.Author, th.author) {
		score *= k.TranscriptionAuthorBoost
	}
	if th.narrator != "" && containsCI(f.Narrator, th.narrator) {
		score *= k.TranscriptionNarratorBoost
	}
	return score
}

// rankScore is the full harness ranking score for one candidate against one
// book under knobs k. Composition order (harness-defined, documented in the
// package comment): core (pinned) → F1 floor → duration → transcription →
// series-number. Every swept knob participates so a sweep moves ranks.
func rankScore(f candFields, book *database.Book, searchWords map[string]bool, hints transcriptionHints, k sweepKnobs) float64 {
	score := scoreCore(f, searchWords, k)
	if score < k.F1MinScore {
		// Below the F1 floor a candidate is disqualified (mirrors the min-score
		// gate the production f1 tier applies before ranking).
		return 0
	}

	// Duration: multiplicative tier multiplier plus the additive tier Score,
	// scaled to the base-score magnitude so both DurationTier knobs matter.
	bookDur := 0
	if book != nil && book.Duration != nil {
		bookDur = *book.Duration
	}
	if bookDur > 0 && f.DurationSec > 0 {
		delta := bookDur - f.DurationSec
		if delta < 0 {
			delta = -delta
		}
		ratio := float64(delta) / float64(bookDur)
		mult, addScore := durationTierValues(ratio, k)
		score = score*mult + addScore/100.0
	}

	// Transcription boosts against the book's audio-derived hints.
	score = transcriptionBoost(score, f, hints, k)

	// Series-name boost: applied when the candidate's series agrees with the
	// book's series name (best-effort; book.Series is a joined relation that
	// may be absent, in which case this is a no-op — noted in the report).
	if book != nil && book.Series != nil && book.Series.Name != "" && f.Series != "" {
		if containsCI(f.Series, book.Series.Name) {
			score *= k.SeriesNameMatchBoost
		}
	}

	// Series-number boost/penalty (mirrors service_search.go lines ~499-516):
	// expected number from the book title, candidate number from SeriesPosition
	// or the candidate title's trailing number.
	if book != nil {
		if expectedNum := trailingNumber(book.Title); expectedNum != "" {
			candNum := ""
			if f.SeriesPosition != "" {
				candNum = normalizeSeriesNumber(f.SeriesPosition)
			}
			if candNum == "" {
				candNum = trailingNumber(f.Title)
			}
			if candNum == expectedNum {
				score *= k.SeriesNumberExactBoost
			} else if candNum != "" {
				score *= k.SeriesNumberWrongPenalty
			}
		}
	}
	return score
}

// hintsFromBook mirrors metafetch.hintsFromBook: audio-derived title/author/
// narrator from the book's transcription fields.
func hintsFromBook(book *database.Book) transcriptionHints {
	if book == nil {
		return transcriptionHints{}
	}
	th := transcriptionHints{}
	if book.TranscribedTitle != nil {
		th.title = *book.TranscribedTitle
	}
	if book.TranscribedAuthor != nil {
		th.author = *book.TranscribedAuthor
	}
	if book.TranscribedNarrator != nil {
		th.narrator = *book.TranscribedNarrator
	}
	return th
}

// ---------------------------------------------------------------------------
// Ground-truth applied-candidate identity
// ---------------------------------------------------------------------------

// candidateCanonicalID mirrors metafetch.metadataCanonicalID: ASIN > ISBN-13 >
// ISBN-10 > ISBN. Returns "" when no external identifier is present.
func candidateCanonicalID(c metafetch.MetadataCandidate) string {
	if c.ASIN != "" {
		return c.ASIN
	}
	if c.ISBN != "" && len(c.ISBN) == 13 {
		return c.ISBN
	}
	if c.ISBN != "" {
		return c.ISBN
	}
	return ""
}

// candidateSourceHash reproduces service_apply.go's MetadataSourceHash exactly:
// sha256("{source}:{canonical_id}") as a lowercase hex string. Returns "" when
// the candidate has no canonical ID (it could never have produced a hash).
func candidateSourceHash(c metafetch.MetadataCandidate) string {
	id := candidateCanonicalID(c)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(c.Source+":"+id)))
}

// ---------------------------------------------------------------------------
// Sweep grid
// ---------------------------------------------------------------------------

// sweepConfig is one point in the sweep: the knob name, the value that knob was
// set to, and the fully-resolved knobs for scoring.
type sweepConfig struct {
	knob  string
	value float64
	knobs sweepKnobs
}

// knobSetter mutates one scalar knob (or scales a whole duration-tier array) to
// v. Used to build the one-factor-at-a-time grid from a base.
type knobSetter struct {
	name string
	base func(k sweepKnobs) float64 // current value of this knob (for factor grid)
	set  func(k *sweepKnobs, v float64)
}

// knobSetters enumerates every swept knob. Duration-tier arrays are swept as a
// single knob each by scaling all tiers by the factor.
func knobSetters(base sweepKnobs) []knobSetter {
	return []knobSetter{
		{"transcription_title_exact_boost", func(k sweepKnobs) float64 { return k.TranscriptionTitleExactBoost }, func(k *sweepKnobs, v float64) { k.TranscriptionTitleExactBoost = v }},
		{"transcription_title_substr_boost", func(k sweepKnobs) float64 { return k.TranscriptionTitleSubstrBoost }, func(k *sweepKnobs, v float64) { k.TranscriptionTitleSubstrBoost = v }},
		{"transcription_author_boost", func(k sweepKnobs) float64 { return k.TranscriptionAuthorBoost }, func(k *sweepKnobs, v float64) { k.TranscriptionAuthorBoost = v }},
		{"transcription_narrator_boost", func(k sweepKnobs) float64 { return k.TranscriptionNarratorBoost }, func(k *sweepKnobs, v float64) { k.TranscriptionNarratorBoost = v }},
		{"compilation_penalty", func(k sweepKnobs) float64 { return k.CompilationPenalty }, func(k *sweepKnobs, v float64) { k.CompilationPenalty = v }},
		{"rich_metadata_field_bonus", func(k sweepKnobs) float64 { return k.RichMetadataFieldBonus }, func(k *sweepKnobs, v float64) { k.RichMetadataFieldBonus = v }},
		{"rich_metadata_bonus_cap", func(k sweepKnobs) float64 { return k.RichMetadataBonusCap }, func(k *sweepKnobs, v float64) { k.RichMetadataBonusCap = v }},
		{"f1_min_score", func(k sweepKnobs) float64 { return k.F1MinScore }, func(k *sweepKnobs, v float64) { k.F1MinScore = v }},
		{"series_name_match_boost", func(k sweepKnobs) float64 { return k.SeriesNameMatchBoost }, func(k *sweepKnobs, v float64) { k.SeriesNameMatchBoost = v }},
		{"series_number_exact_boost", func(k sweepKnobs) float64 { return k.SeriesNumberExactBoost }, func(k *sweepKnobs, v float64) { k.SeriesNumberExactBoost = v }},
		{"series_number_wrong_penalty", func(k sweepKnobs) float64 { return k.SeriesNumberWrongPenalty }, func(k *sweepKnobs, v float64) { k.SeriesNumberWrongPenalty = v }},
		{"duration_tier_multipliers", func(k sweepKnobs) float64 { return 1.0 }, func(k *sweepKnobs, v float64) {
			for i := range k.DurationTierMultipliers {
				k.DurationTierMultipliers[i] = defaultDurationTierMultipliers[i] * v
			}
		}},
		{"duration_tier_scores", func(k sweepKnobs) float64 { return 1.0 }, func(k *sweepKnobs, v float64) {
			for i := range k.DurationTierScores {
				k.DurationTierScores[i] = defaultDurationTierScores[i] * v
			}
		}},
	}
}

// zeroBasedKnobs are the pointer-typed knobs whose grid starts at 0 rather than
// sweepFactorLo×current: 0 is a legitimate, reachable operator value for these
// (spec C2), so the grid must be able to recommend it. The remaining knobs sweep
// multiplicatively around their current value.
var zeroBasedKnobs = map[string]bool{
	"compilation_penalty":     true,
	"rich_metadata_bonus_cap": true,
	"f1_min_score":            true,
}

// buildSweepGrid produces the OFAT sweep configs: for each knob, `steps` points
// across [lo, sweepFactorHi]×(current value), where lo is 0 for the zero-based
// pointer knobs (spec C2) and sweepFactorLo otherwise. Duration-tier arrays scale
// by the raw factor. One knob varies per config; all others stay at their current
// value.
func buildSweepGrid(current sweepKnobs, steps int) []sweepConfig {
	if steps < 1 {
		steps = 1
	}
	var out []sweepConfig
	for _, ks := range knobSetters(current) {
		cur := ks.base(current)
		lo := sweepFactorLo
		if zeroBasedKnobs[ks.name] {
			lo = 0
		}
		for i := 0; i < steps; i++ {
			factor := lo
			if steps > 1 {
				factor = lo + (sweepFactorHi-lo)*float64(i)/float64(steps-1)
			}
			v := cur * factor
			k := current
			ks.set(&k, v)
			out = append(out, sweepConfig{knob: ks.name, value: v, knobs: k})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------

type sweepPoint struct {
	Value        float64 `json:"value"`
	Top1Accuracy float64 `json:"top1_accuracy"`
	MeanRank     float64 `json:"mean_rank"`
}

type knobSweep struct {
	Knob   string       `json:"knob"`
	Points []sweepPoint `json:"points"`
}

type segmentAccuracy struct {
	N            int     `json:"n"`
	Top1Accuracy float64 `json:"top1_accuracy"`
	MeanRank     float64 `json:"mean_rank"`
}

// calibrationReport is the read-only result. It is JSON-marshaled and logged
// (the sdk.Reporter has no result sink); the report-shape test asserts on this
// struct directly.
type calibrationReport struct {
	SampleSize              int                        `json:"sample_size"`
	Evaluated               int                        `json:"evaluated"`
	SkipCounts              map[string]int             `json:"skip_counts"`
	SweepSteps              int                        `json:"sweep_steps"`
	CurrentKnobTop1Accuracy float64                    `json:"current_knob_top1_accuracy"`
	CurrentKnobMeanRank     float64                    `json:"current_knob_mean_rank"`
	Sweep                   []knobSweep                `json:"sweep"`
	Segments                map[string]segmentAccuracy `json:"segments"`
	ManualSegmentDetermined bool                       `json:"manual_segment_determined"`
	CircularBiased          bool                       `json:"circular_biased"`
	CircularityCaveat       string                     `json:"circularity_caveat"`
	Method                  string                     `json:"method"`
	ScorerCaveat            string                     `json:"scorer_caveat"`
	CoverageCaveat          string                     `json:"coverage_caveat"`
}

// bookEval is the per-book replay outcome accumulated by the pooled loop.
type bookEval struct {
	origin      string // "manual" | "auto"
	currentRank int    // rank of the applied candidate under current knobs
	sweepRanks  []int  // rank per sweep config (aligned to grid order)
}

// evaluateBook re-ranks a book's cached candidates under the current knobs and
// every sweep config, returning the applied candidate's rank in each. Returns
// (nil, skipReason) when the book cannot contribute (skipReason is counted).
//
// Pure aside from reading nothing mutable — all inputs are values/read-only.
func evaluateBook(
	book *database.Book,
	cands []metafetch.MetadataCandidate,
	origin string,
	current sweepKnobs,
	grid []sweepConfig,
) (*bookEval, string) {
	if book.MetadataSourceHash == nil || *book.MetadataSourceHash == "" {
		return nil, "no_source_hash"
	}
	appliedIdx := -1
	for i := range cands {
		if h := candidateSourceHash(cands[i]); h != "" && h == *book.MetadataSourceHash {
			appliedIdx = i
			break
		}
	}
	if appliedIdx < 0 {
		return nil, "unmatchable"
	}

	searchWords := metafetch.SignificantWords(book.Title)
	hints := hintsFromBook(book)

	fields := make([]candFields, len(cands))
	for i := range cands {
		fields[i] = fieldsFromCandidate(cands[i])
	}

	rankUnder := func(k sweepKnobs) int {
		appliedScore := rankScore(fields[appliedIdx], book, searchWords, hints, k)
		strictlyBetter := 0
		for i := range fields {
			if i == appliedIdx {
				continue
			}
			if rankScore(fields[i], book, searchWords, hints, k) > appliedScore {
				strictlyBetter++
			}
		}
		return strictlyBetter + 1
	}

	ev := &bookEval{origin: origin, currentRank: rankUnder(current)}
	ev.sweepRanks = make([]int, len(grid))
	for gi := range grid {
		ev.sweepRanks[gi] = rankUnder(grid[gi].knobs)
	}
	return ev, ""
}

// buildReport aggregates per-book evals into the final report. Pure function —
// unit-testable without any store.
func buildReport(evals []*bookEval, grid []sweepConfig, skips map[string]int, sampleSize, steps int) calibrationReport {
	rep := calibrationReport{
		SampleSize:        sampleSize,
		Evaluated:         len(evals),
		SkipCounts:        skips,
		SweepSteps:        steps,
		Segments:          map[string]segmentAccuracy{},
		CircularityCaveat: circularityCaveat,
		Method:            "one-factor-at-a-time (OFAT) sweep, ±50% around each knob's current value; pointer knobs may reach 0.",
		ScorerCaveat:      "Harness-local knob-parameterized scorer: f1-base + non-base core pinned bit-for-bit against metafetch.ScoreOneResult at default knobs; duration/transcription/series layers mirror internal/metafetch/service_scoring.go. Layer composition is harness-defined, not bit-identical to production ScoreBaseCandidates, so recommendations are directional. NOTE: the f1_min_score column is NOT a discrimination signal — F1MinScore acts as a hard competitor-filter (candidates below it are dropped), and the applied candidate almost always clears it, so raising it can only remove rivals and will trend monotonically 'higher = better'. Read that column as a filter aggressiveness knob, not a recommendation to maximize.",
		CoverageCaveat:    "Eval set covers only applied books whose applied candidate had an ASIN/ISBN (MetadataSourceHash is stamped only then); cached candidates lacking an ID can never hash-match and fall to 'unmatchable'. The sample is the first-N applied books in PebbleDB key order (bounded by sample_limit), not a random draw. Series-name boost is a no-op when the book's series relation is not loaded.",
	}
	if len(evals) == 0 {
		rep.ManualSegmentDetermined = false
		rep.CircularBiased = true
		rep.Sweep = emptySweep(grid, steps)
		return rep
	}

	// Current-knob accuracy + segment breakdown.
	var top1, rankSum int
	segTop1 := map[string]int{}
	segRankSum := map[string]int{}
	segN := map[string]int{}
	for _, e := range evals {
		if e.currentRank == 1 {
			top1++
			segTop1[e.origin]++
		}
		rankSum += e.currentRank
		segRankSum[e.origin] += e.currentRank
		segN[e.origin]++
	}
	n := float64(len(evals))
	rep.CurrentKnobTop1Accuracy = float64(top1) / n
	rep.CurrentKnobMeanRank = float64(rankSum) / n
	for origin, cnt := range segN {
		rep.Segments[origin] = segmentAccuracy{
			N:            cnt,
			Top1Accuracy: float64(segTop1[origin]) / float64(cnt),
			MeanRank:     float64(segRankSum[origin]) / float64(cnt),
		}
	}
	rep.ManualSegmentDetermined = segN["manual"] > 0
	// The overall/auto figures are always circular; the sweep is fully
	// circular-biased only when there is no manual segment to anchor it.
	rep.CircularBiased = !rep.ManualSegmentDetermined

	// Sweep table, grouped by knob preserving grid order.
	byKnob := map[string]*knobSweep{}
	var order []string
	for gi := range grid {
		g := grid[gi]
		ks, ok := byKnob[g.knob]
		if !ok {
			ks = &knobSweep{Knob: g.knob}
			byKnob[g.knob] = ks
			order = append(order, g.knob)
		}
		var t1, rsum int
		for _, e := range evals {
			if e.sweepRanks[gi] == 1 {
				t1++
			}
			rsum += e.sweepRanks[gi]
		}
		ks.Points = append(ks.Points, sweepPoint{
			Value:        g.value,
			Top1Accuracy: float64(t1) / n,
			MeanRank:     float64(rsum) / n,
		})
	}
	for _, name := range order {
		rep.Sweep = append(rep.Sweep, *byKnob[name])
	}
	return rep
}

func emptySweep(grid []sweepConfig, steps int) []knobSweep {
	byKnob := map[string]*knobSweep{}
	var order []string
	for gi := range grid {
		g := grid[gi]
		ks, ok := byKnob[g.knob]
		if !ok {
			ks = &knobSweep{Knob: g.knob}
			byKnob[g.knob] = ks
			order = append(order, g.knob)
		}
		ks.Points = append(ks.Points, sweepPoint{Value: g.value})
	}
	out := make([]knobSweep, 0, len(order))
	for _, name := range order {
		out = append(out, *byKnob[name])
	}
	return out
}

// ---------------------------------------------------------------------------
// Op runner (read-only)
// ---------------------------------------------------------------------------

// appliedBook pairs a book with the cached candidates and apply-origin the
// pooled loop needs, so all store I/O happens on the enumerating goroutine and
// the pooled workers touch only in-memory values.
type appliedBook struct {
	book   *database.Book
	cands  []metafetch.MetadataCandidate
	origin string
}

// runCalibrateScoring implements the op. READ-ONLY: it enumerates books, reads
// their caches and metadata-field-state override provenance, replays the
// scorer, and reports. It never calls any store mutation.
func (p *Plugin) runCalibrateScoring(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("database store not available")
	}
	if p.mfs == nil {
		return fmt.Errorf("metafetch service not available")
	}

	var params calibrateScoringParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	sampleLimit := params.SampleLimit
	if sampleLimit < 0 {
		sampleLimit = 0
	}
	if sampleLimit == 0 {
		sampleLimit = defaultSampleLimit
	}
	steps := params.SweepSteps
	if steps <= 0 {
		steps = defaultSweepSteps
	}
	if steps > maxSweepSteps {
		steps = maxSweepSteps
	}

	log := reporter.Logger()
	current := knobsFromConfig(config.AppConfig.MetadataScoring)
	grid := buildSweepGrid(current, steps)
	log.Info("calibrate-scoring start", "sample_limit", sampleLimit, "sweep_steps", steps, "sweep_configs", len(grid))

	skips := map[string]int{}
	var skipMu sync.Mutex
	countSkip := func(reason string) {
		skipMu.Lock()
		skips[reason]++
		skipMu.Unlock()
	}

	// --- Enumerate applied books + read caches (sequential store I/O) ---
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Collecting applied books with candidate caches…")

	var work []appliedBook
	cursor := ""
	for {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		page, err := p.store.GetAllBooksFullFrom(cursor, enumPageSize)
		if err != nil {
			return fmt.Errorf("enumerate books: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			b := page[i]
			cursor = b.ID
			if b.MetadataSourceHash == nil || *b.MetadataSourceHash == "" {
				continue // not an applied book — silently skip (not counted)
			}
			entry, _, cerr := p.mfs.GetCachedCandidates(b.ID)
			if cerr != nil {
				countSkip("cache_read_error")
				continue
			}
			if entry == nil {
				countSkip("no_cache")
				continue
			}
			if len(entry.Candidates) == 0 {
				countSkip("empty_cache")
				continue
			}
			cands := make([]metafetch.MetadataCandidate, 0, len(entry.Candidates))
			for _, raw := range entry.Candidates {
				var c metafetch.MetadataCandidate
				if err := json.Unmarshal(raw, &c); err != nil {
					countSkip("unparseable_candidate")
					continue
				}
				cands = append(cands, c)
			}
			if len(cands) == 0 {
				countSkip("empty_cache")
				continue
			}
			bookCopy := b
			work = append(work, appliedBook{
				book:   &bookCopy,
				cands:  cands,
				origin: p.applyOrigin(&bookCopy),
			})
			if sampleLimit > 0 && len(work) >= sampleLimit {
				break
			}
		}
		if sampleLimit > 0 && len(work) >= sampleLimit {
			break
		}
		if len(page) < enumPageSize {
			break
		}
	}

	sampleSize := len(work)
	if sampleSize == 0 {
		rep := buildReport(nil, grid, skips, 0, steps)
		p.logReport(log, rep)
		prog.Done("calibrate-scoring: no applied books with usable caches found")
		return nil
	}

	// --- Replay under a bounded pool (CPU-bound → NumCPU) ---
	prog = sdk.NewProgress(reporter, sampleSize)
	prog.Start(fmt.Sprintf("Replaying %d applied books across %d sweep configs…", sampleSize, len(grid)))

	var mu sync.Mutex
	evals := make([]*bookEval, 0, sampleSize)

	err := registry.RunItems(ctx, reporter, work, func(_ context.Context, item appliedBook) error {
		ev, skip := evaluateBook(item.book, item.cands, item.origin, current, grid)
		if skip != "" {
			countSkip(skip)
			return nil
		}
		mu.Lock()
		evals = append(evals, ev)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d", i+1, t)
		},
	})
	if err != nil {
		return err
	}

	rep := buildReport(evals, grid, skips, sampleSize, steps)
	p.logReport(log, rep)
	prog.Done(fmt.Sprintf("calibrate-scoring report complete: evaluated=%d current_top1=%.3f manual_segment=%t (report-only, no writes)",
		rep.Evaluated, rep.CurrentKnobTop1Accuracy, rep.ManualSegmentDetermined))
	return nil
}

// applyOrigin classifies a book's apply origin: "manual" when the book carries
// any manual metadata-field override (recorded with source "manual" in the
// metadata-field-state store — i.e. a non-nil OverrideValue), else "auto".
// A field-state read error is treated as "auto" (fail-open, never fatal).
func (p *Plugin) applyOrigin(book *database.Book) string {
	states, err := p.store.GetMetadataFieldStates(book.ID)
	if err != nil {
		return "auto"
	}
	for _, st := range states {
		if st.OverrideValue != nil && *st.OverrideValue != "" {
			return "manual"
		}
	}
	return "auto"
}

// logReport emits the report as structured log fields plus a JSON blob (the
// sdk.Reporter has no result sink, so the report travels via the operation log).
func (p *Plugin) logReport(log *slog.Logger, rep calibrationReport) {
	blob, err := json.Marshal(rep)
	if err != nil {
		log.Error("calibrate-scoring report marshal", "error", err)
		return
	}
	log.Info("calibrate-scoring report",
		"sample_size", rep.SampleSize,
		"evaluated", rep.Evaluated,
		"current_top1_accuracy", rep.CurrentKnobTop1Accuracy,
		"current_mean_rank", rep.CurrentKnobMeanRank,
		"manual_segment_determined", rep.ManualSegmentDetermined,
		"circular_biased", rep.CircularBiased,
		"skip_counts", rep.SkipCounts,
		"report_json", string(blob),
	)
	log.Info("calibrate-scoring circularity caveat", "caveat", rep.CircularityCaveat)
}
