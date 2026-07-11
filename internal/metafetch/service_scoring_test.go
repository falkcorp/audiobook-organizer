// file: internal/metafetch/service_scoring_test.go
// version: 1.2.0
// guid: 7e3f6a1c-9b4d-4e2a-8c1f-6d5b3a2e9f70
// last-edited: 2026-07-10

package metafetch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fakeCandidateScorer is a minimal ai.MetadataCandidateScorer test double
// that returns a fixed score slice (or error) regardless of input, so tests
// can drive ScoreBaseCandidates' acceptance logic directly.
type fakeCandidateScorer struct {
	name   string
	scores []float64
	err    error
}

func (f *fakeCandidateScorer) Score(ctx context.Context, q ai.Query, cands []ai.Candidate) ([]float64, error) {
	return f.scores, f.err
}
func (f *fakeCandidateScorer) Name() string {
	if f.name == "" {
		return "embedding"
	}
	return f.name
}

// withEmbeddingScoringEnabled sets config.AppConfig.MetadataScoring.EmbeddingEnabled
// for the duration of the test and restores the prior value on cleanup.
func withEmbeddingScoringEnabled(t *testing.T, enabled bool) {
	t.Helper()
	prev := config.AppConfig.MetadataScoring.EmbeddingEnabled
	config.AppConfig.MetadataScoring.EmbeddingEnabled = enabled
	t.Cleanup(func() {
		config.AppConfig.MetadataScoring.EmbeddingEnabled = prev
	})
}

// ---------------------------------------------------------------------------
// ScoreBaseCandidates — degenerate all-zero scorer result falls back to F1
// ---------------------------------------------------------------------------

func TestScoreBaseCandidates_DegenerateAllZeroFallsBackToF1(t *testing.T) {
	withEmbeddingScoringEnabled(t, true)

	mock := &database.MockStore{}
	svc := NewService(mock)
	svc.SetMetadataScorer(&fakeCandidateScorer{
		scores: []float64{0, 0},
	})

	book := &database.Book{ID: "b1", Title: "Mistborn"}
	results := []metadata.BookMetadata{
		{Title: "Mistborn"},
		{Title: "Completely Different Book"},
	}
	searchWords := SignificantWords("Mistborn")

	scores, tier := svc.ScoreBaseCandidates(context.Background(), book, results, searchWords)

	require.Equal(t, "f1", tier, "an all-zero scorer result must be treated as failure and fall back to F1")
	require.Len(t, scores, 2)
	for i, r := range results {
		assert.Equal(t, computeF1Base(r, searchWords), scores[i], "fallback scores should match computeF1Base directly")
	}
}

// TestScoreBaseCandidates_NonDegenerateStaysOnEmbeddingTier proves the fix
// doesn't over-trigger the fallback: a scorer returning genuine non-zero
// scores must still win the "embedding" tier.
func TestScoreBaseCandidates_NonDegenerateStaysOnEmbeddingTier(t *testing.T) {
	withEmbeddingScoringEnabled(t, true)

	mock := &database.MockStore{}
	svc := NewService(mock)
	svc.SetMetadataScorer(&fakeCandidateScorer{
		scores: []float64{0.9, 0.1},
	})

	book := &database.Book{ID: "b1", Title: "Mistborn"}
	results := []metadata.BookMetadata{
		{Title: "Mistborn"},
		{Title: "Completely Different Book"},
	}
	searchWords := SignificantWords("Mistborn")

	scores, tier := svc.ScoreBaseCandidates(context.Background(), book, results, searchWords)

	require.Equal(t, "embedding", tier, "a genuine non-zero scorer result must not be discarded")
	require.Equal(t, []float64{0.9, 0.1}, scores)
}

// ---------------------------------------------------------------------------
// End-to-end: a degenerate embedding tier must not zero out search results
// (MATCH-1/BUG-1/QUAL-4 regression, service_search.go's threshold filter).
// ---------------------------------------------------------------------------

func TestSearchMetadataForBookWithOptions_DegenerateEmbeddingFallsBackToF1Results(t *testing.T) {
	withEmbeddingScoringEnabled(t, true)

	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Mistborn"}, nil
		},
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{
		&mockMetadataSource{
			name: "test-source",
			results: []metadata.BookMetadata{
				{Title: "Mistborn", Author: "Brandon Sanderson"},
				{Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
			},
		},
	})
	// Simulate the degenerate case: every candidate scored exactly 0 by the
	// embedding tier (e.g. a stale cross-model vector comparison). Before the
	// fix, ScoreBaseCandidates would have accepted this as tier="embedding"
	// and the threshold filter (minScore = EmbeddingMinScore, default 0.82)
	// would have dropped every candidate, returning zero results.
	svc.SetMetadataScorer(&fakeCandidateScorer{
		scores: []float64{0, 0},
	})

	resp, err := svc.SearchMetadataForBookWithOptions("b1", "Mistborn", "", "", "", SearchOptions{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotEmpty(t, resp.Results, "F1 fallback should surface non-empty results even though the embedding tier degenerated to all-zero scores")
}

// ---------------------------------------------------------------------------
// TestDurationScoringGolden — golden fixtures for INIT-3-T2 (unify duration
// scoring). durationScoreMultiplier (absolute-delta-second buckets) and
// computeDurationScore (delta-ratio buckets) used to be two independent
// bucket systems that could disagree on the same pair. This table locks in
// their outputs across a grid straddling every bucket edge of BOTH systems,
// on both a short (2h) and a very long (40h) book.
//
// Post-unification (both functions now look up the shared ratio-based
// durationTiers table via durationTier), EVERY multiplier cell that changed
// carries an inline comment with the OLD value, the NEW value, and WHY —
// this enumerated diff is the review artifact for INIT-3-T2. Every additive
// Score value below is UNCHANGED from the pre-unification implementation
// (computeDurationScore was already ratio-based, so its bucket edges
// 0.05/0.10/0.20/0.50/1.00 are independent of book length and untouched by
// the unification) — this file's git history (commit
// "test(metafetch): golden fixtures for duration scoring pre-unification")
// is the proof: the Score column here is identical to that commit's.
//
// durationScoreMultiplier was absolute-delta-second-based before this change
// (buckets at 60/300/600/1200/1800/3600/7200s), so it used to respond
// identically to a given delta regardless of book length. Now unified onto
// the ratio table, the SAME delta produces a different multiplier depending
// on book length (e.g. a 61s delta is a rounding error on a 40h book but a
// real ~0.85% signal on a 2h book) — this is the intended fix: the two
// functions can no longer disagree about the same (book, candidate) pair.
func TestDurationScoringGolden(t *testing.T) {
	const short2h = 2 * 3600      // 7200s
	const veryLong40h = 40 * 3600 // 144000s

	type tc struct {
		name           string
		book, cand     int
		wantMultiplier float64
		wantScore      float64
	}

	cases := []tc{
		// ---------------------------------------------------------------
		// short2h (book=7200s): delta straddles every OLD absolute-delta-
		// second edge (60/300/600/1200/1800/3600/7200s); ratio column shows
		// what the new table actually keys off.
		// ---------------------------------------------------------------

		// delta=59 ratio=0.0082 (<5%): UNCHANGED — both old systems already
		// agreed here (×1.30/+20), and 0.82% is still deep in the new <5% tier.
		{name: "short2h/delta=59", book: short2h, cand: short2h + 59, wantMultiplier: 1.30, wantScore: 20},

		// delta=61 ratio=0.0085 (<5%): CHANGED ×1.20→×1.30. The old absolute-
		// second bucket crossed its 60s edge and dropped a tier even though
		// 0.85% is still deep in the <5% ratio tier — the new table correctly
		// keeps ×1.30.
		{name: "short2h/delta=61", book: short2h, cand: short2h + 61, wantMultiplier: 1.30, wantScore: 20},

		// delta=299 ratio=0.0415 (<5%): CHANGED ×1.20→×1.30, same reason as
		// delta=61 — 4.15% is still under the 5% ratio edge.
		{name: "short2h/delta=299", book: short2h, cand: short2h + 299, wantMultiplier: 1.30, wantScore: 20},

		// delta=301 ratio=0.0418 (<5%): CHANGED ×1.10→×1.30. The old system
		// crossed its 300s edge into the "close" tier; the ratio is still
		// under 5%, so the new table keeps the top tier.
		{name: "short2h/delta=301", book: short2h, cand: short2h + 301, wantMultiplier: 1.30, wantScore: 20},

		// delta=599 ratio=0.0832 (5-10%): CHANGED ×1.10→×1.20. Old bucket was
		// still in its 600s tier (×1.10); ratio-wise 8.3% belongs one tier
		// higher (×1.20).
		{name: "short2h/delta=599", book: short2h, cand: short2h + 599, wantMultiplier: 1.20, wantScore: 15},

		// delta=601 ratio=0.0835 (5-10%): CHANGED ×1.05→×1.20. Old bucket
		// crossed its 600s edge into ×1.05; ratio 8.35% is squarely in the
		// new 5-10% tier (×1.20).
		{name: "short2h/delta=601", book: short2h, cand: short2h + 601, wantMultiplier: 1.20, wantScore: 15},

		// delta=1199 ratio=0.1665 (10-20%): CHANGED ×1.05→×1.10. Old bucket
		// was still in its 1200s tier (×1.05); ratio 16.65% belongs in the
		// new 10-20% tier (×1.10).
		{name: "short2h/delta=1199", book: short2h, cand: short2h + 1199, wantMultiplier: 1.10, wantScore: 10},

		// delta=1201 ratio=0.1668 (10-20%): CHANGED ×1.00→×1.10. Old bucket
		// crossed its 1200s edge into ×1.00; ratio 16.68% is in the new
		// 10-20% tier (×1.10).
		{name: "short2h/delta=1201", book: short2h, cand: short2h + 1201, wantMultiplier: 1.10, wantScore: 10},

		// delta=1799 ratio=0.2499 (20-50%): UNCHANGED — old bucket (×1.00,
		// still within its 1800s tier) happens to match the new 20-50% tier
		// (×1.00) exactly.
		{name: "short2h/delta=1799", book: short2h, cand: short2h + 1799, wantMultiplier: 1.00, wantScore: 0},

		// delta=1801 ratio=0.2501 (20-50%): CHANGED ×0.90→×1.00. Old bucket
		// crossed its 1800s edge into ×0.90; ratio 25.01% is still well
		// inside the new 20-50% "acceptable range" tier (×1.00).
		{name: "short2h/delta=1801", book: short2h, cand: short2h + 1801, wantMultiplier: 1.00, wantScore: 0},

		// delta=3599 ratio=0.4999 (20-50%): CHANGED ×0.90→×1.00. Old bucket
		// was still in its 3600s tier (×0.90); ratio 49.99% is just inside
		// the new 20-50% tier (×1.00).
		{name: "short2h/delta=3599", book: short2h, cand: short2h + 3599, wantMultiplier: 1.00, wantScore: 0},

		// delta=3601 ratio=0.5001 (50-100%): UNCHANGED — old bucket (×0.75,
		// crossing into its 7200s tier) happens to match the new 50-100%
		// tier (×0.75) exactly.
		{name: "short2h/delta=3601", book: short2h, cand: short2h + 3601, wantMultiplier: 0.75, wantScore: -10},

		// delta=7199 ratio=0.9999 (50-100%): UNCHANGED — old bucket (×0.75,
		// still within its 7200s tier) matches the new 50-100% tier (×0.75).
		{name: "short2h/delta=7199", book: short2h, cand: short2h + 7199, wantMultiplier: 0.75, wantScore: -10},

		// delta=7201 ratio=1.0001 (>100%): UNCHANGED — old bucket (×0.50,
		// past its 7200s edge) matches the new >100% catch-all tier (×0.50).
		{name: "short2h/delta=7201", book: short2h, cand: short2h + 7201, wantMultiplier: 0.50, wantScore: -20},

		// ---------------------------------------------------------------
		// verylong40h (book=144000s): the SAME absolute deltas as above are
		// now a tiny fraction of a 40h book, so nearly every multiplier cell
		// changes — this is the core bug INIT-3-T2 fixes: the old
		// absolute-second multiplier treated a 61s delta on a 40h book as a
		// meaningful mismatch (×1.20) when it is really a 0.04% rounding
		// error; the new ratio table correctly keeps it in the top tier.
		// ---------------------------------------------------------------

		// delta=59 ratio=0.00041 (<5%): UNCHANGED — both old systems already
		// agreed (×1.30/+20).
		{name: "verylong40h/delta=59", book: veryLong40h, cand: veryLong40h + 59, wantMultiplier: 1.30, wantScore: 20},

		// delta=61 ratio=0.00042 (<5%): CHANGED ×1.20→×1.30. Old bucket
		// crossed its 60s edge; ratio is a rounding error, stays top tier.
		{name: "verylong40h/delta=61", book: veryLong40h, cand: veryLong40h + 61, wantMultiplier: 1.30, wantScore: 20},

		// delta=299 ratio=0.00208 (<5%): CHANGED ×1.20→×1.30, same reason.
		{name: "verylong40h/delta=299", book: veryLong40h, cand: veryLong40h + 299, wantMultiplier: 1.30, wantScore: 20},

		// delta=301 ratio=0.00209 (<5%): CHANGED ×1.10→×1.30, same reason.
		{name: "verylong40h/delta=301", book: veryLong40h, cand: veryLong40h + 301, wantMultiplier: 1.30, wantScore: 20},

		// delta=599 ratio=0.00416 (<5%): CHANGED ×1.10→×1.30. Old bucket
		// crossed its 600s edge; ratio is still under 0.5%, top tier.
		{name: "verylong40h/delta=599", book: veryLong40h, cand: veryLong40h + 599, wantMultiplier: 1.30, wantScore: 20},

		// delta=601 ratio=0.00417 (<5%): CHANGED ×1.05→×1.30, same reason.
		{name: "verylong40h/delta=601", book: veryLong40h, cand: veryLong40h + 601, wantMultiplier: 1.30, wantScore: 20},

		// delta=1199 ratio=0.00833 (<5%): CHANGED ×1.05→×1.30. Old bucket was
		// in its 1200s tier; ratio is under 1%, top tier.
		{name: "verylong40h/delta=1199", book: veryLong40h, cand: veryLong40h + 1199, wantMultiplier: 1.30, wantScore: 20},

		// delta=1201 ratio=0.00834 (<5%): CHANGED ×1.00→×1.30, same reason.
		{name: "verylong40h/delta=1201", book: veryLong40h, cand: veryLong40h + 1201, wantMultiplier: 1.30, wantScore: 20},

		// delta=1799 ratio=0.01249 (<5%): CHANGED ×1.00→×1.30. Old bucket was
		// in its 1800s "no adjustment" tier; ratio is ~1.25%, top tier.
		{name: "verylong40h/delta=1799", book: veryLong40h, cand: veryLong40h + 1799, wantMultiplier: 1.30, wantScore: 20},

		// delta=1801 ratio=0.01251 (<5%): CHANGED ×0.90→×1.30. Old bucket
		// crossed its 1800s edge into a penalty tier; ratio is still ~1.25%,
		// top tier — this is the sharpest illustration of the old bug: a
		// 30-minute delta on a 40h book was penalized as if it were a
		// meaningful mismatch.
		{name: "verylong40h/delta=1801", book: veryLong40h, cand: veryLong40h + 1801, wantMultiplier: 1.30, wantScore: 20},

		// delta=3599 ratio=0.02499 (<5%): CHANGED ×0.90→×1.30. Old bucket was
		// in its 3600s tier; ratio is ~2.5%, top tier.
		{name: "verylong40h/delta=3599", book: veryLong40h, cand: veryLong40h + 3599, wantMultiplier: 1.30, wantScore: 20},

		// delta=3601 ratio=0.02501 (<5%): CHANGED ×0.75→×1.30. Old bucket
		// crossed its 3600s edge into a heavier penalty; ratio is still
		// ~2.5%, top tier.
		{name: "verylong40h/delta=3601", book: veryLong40h, cand: veryLong40h + 3601, wantMultiplier: 1.30, wantScore: 20},

		// delta=7199 ratio=0.04999 (<5%): CHANGED ×0.75→×1.30. Old bucket was
		// in its 7200s tier (the old system's worst-but-one tier); ratio is
		// just under 5%, still top tier.
		{name: "verylong40h/delta=7199", book: veryLong40h, cand: veryLong40h + 7199, wantMultiplier: 1.30, wantScore: 20},

		// delta=7201 ratio=0.05001 (5-10%): CHANGED ×0.50→×1.20. Old bucket
		// crossed its final 7200s edge into the worst tier (×0.50, "almost
		// certainly wrong edition"); ratio is barely over 5%, landing in the
		// new 5-10% "very close" tier (×1.20) — the clearest example of the
		// old function disagreeing with computeDurationScore's ratio-based
		// +20 ("essentially the same edition") on the identical pair.
		{name: "verylong40h/delta=7201", book: veryLong40h, cand: veryLong40h + 7201, wantMultiplier: 1.20, wantScore: 15},

		// ---------------------------------------------------------------
		// Unknown-duration cases: either side <= 0 must be non-disqualifying
		// (multiplier ×1.0, score +0), never a rejection signal, on both
		// sides and for negative values too. UNCHANGED by the unification.
		// ---------------------------------------------------------------
		{name: "unknown/book=0", book: 0, cand: 5000, wantMultiplier: 1.0, wantScore: 0},
		{name: "unknown/cand=0", book: 5000, cand: 0, wantMultiplier: 1.0, wantScore: 0},
		{name: "unknown/both=0", book: 0, cand: 0, wantMultiplier: 1.0, wantScore: 0},
		{name: "unknown/book=negative", book: -100, cand: 5000, wantMultiplier: 1.0, wantScore: 0},
		{name: "unknown/cand=negative", book: 5000, cand: -100, wantMultiplier: 1.0, wantScore: 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMultiplier := durationScoreMultiplier(c.book, c.cand)
			gotScore := computeDurationScore(c.book, c.cand)
			assert.InDelta(t, c.wantMultiplier, gotMultiplier, 1e-9, "durationScoreMultiplier(%d, %d)", c.book, c.cand)
			assert.InDelta(t, c.wantScore, gotScore, 1e-9, "computeDurationScore(%d, %d)", c.book, c.cand)
		})
	}
}
