// file: internal/metafetch/service_scoring_test.go
// version: 1.1.0
// guid: 7e3f6a1c-9b4d-4e2a-8c1f-6d5b3a2e9f70
// last-edited: 2026-07-10

package metafetch

import (
	"context"
	"fmt"
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
// their CURRENT, pre-unification outputs across a grid straddling every
// bucket edge of BOTH systems, on both a short (2h) and a very long (40h)
// book, so the post-unification rewrite (single ratio-based durationTiers
// table) is provably behavior-preserving where required and every
// intentional change is called out inline.
//
// Additive computeDurationScore is already ratio-based, so its bucket edges
// (0.05/0.10/0.20/0.50/1.00 delta-ratio) are independent of book length and
// its outputs here define the tier table's Score column bit-for-bit — those
// cells must NOT change after unification.
//
// durationScoreMultiplier is absolute-delta-second-based today (buckets at
// 60/300/600/1200/1800/3600/7200s), so it responds identically to a given
// delta regardless of book length; once unified onto the ratio table the
// SAME delta produces a different multiplier depending on book length
// (e.g. a 61s delta is a rounding error on a 40h book but a real signal on a
// 2h book). Nearly every multiplier cell below is therefore EXPECTED to
// change post-unification — see the inline comments added in the second
// (post-swap) revision of this table for the old value / new value / why.
func TestDurationScoringGolden(t *testing.T) {
	const short2h = 2 * 3600     // 7200s
	const veryLong40h = 40 * 3600 // 144000s

	type tc struct {
		name           string
		book, cand     int
		wantMultiplier float64
		wantScore      float64
	}

	// deltaCases straddles every bucket edge of BOTH the old absolute-delta
	// multiplier (60/300/600/1200/1800/3600/7200s) and the old delta-ratio
	// additive score (0.05/0.10/0.20/0.50/1.00 ratio) buckets, one tick below
	// and one tick above each edge.
	deltaCases := []int{59, 61, 299, 301, 599, 601, 1199, 1201, 1799, 1801, 3599, 3601, 7199, 7201}

	var cases []tc
	for _, book := range []int{short2h, veryLong40h} {
		bookName := "short2h"
		if book == veryLong40h {
			bookName = "verylong40h"
		}
		for _, d := range deltaCases {
			cases = append(cases, tc{
				name: fmt.Sprintf("%s/delta=%d", bookName, d),
				book: book, cand: book + d,
			})
		}
	}

	// wantMultiplier/wantScore captured from the CURRENT (pre-unification)
	// implementation on 2026-07-10, via a throwaway probe test run against
	// this exact grid — see PR description for the raw probe output.
	current := map[string][2]float64{
		"short2h/delta=59":        {1.30, 20},
		"short2h/delta=61":        {1.20, 20},
		"short2h/delta=299":       {1.20, 20},
		"short2h/delta=301":       {1.10, 20},
		"short2h/delta=599":       {1.10, 15},
		"short2h/delta=601":       {1.05, 15},
		"short2h/delta=1199":      {1.05, 10},
		"short2h/delta=1201":      {1.00, 10},
		"short2h/delta=1799":      {1.00, 0},
		"short2h/delta=1801":      {0.90, 0},
		"short2h/delta=3599":      {0.90, 0},
		"short2h/delta=3601":      {0.75, -10},
		"short2h/delta=7199":      {0.75, -10},
		"short2h/delta=7201":      {0.50, -20},
		"verylong40h/delta=59":    {1.30, 20},
		"verylong40h/delta=61":    {1.20, 20},
		"verylong40h/delta=299":   {1.20, 20},
		"verylong40h/delta=301":   {1.10, 20},
		"verylong40h/delta=599":   {1.10, 20},
		"verylong40h/delta=601":   {1.05, 20},
		"verylong40h/delta=1199":  {1.05, 20},
		"verylong40h/delta=1201":  {1.00, 20},
		"verylong40h/delta=1799":  {1.00, 20},
		"verylong40h/delta=1801":  {0.90, 20},
		"verylong40h/delta=3599":  {0.90, 20},
		"verylong40h/delta=3601":  {0.75, 20},
		"verylong40h/delta=7199":  {0.75, 20},
		"verylong40h/delta=7201":  {0.50, 15},
	}
	for i, c := range cases {
		want := current[c.name]
		cases[i].wantMultiplier = want[0]
		cases[i].wantScore = want[1]
	}

	// Unknown-duration cases: either side <= 0 must be non-disqualifying
	// (multiplier ×1.0, score +0), never a rejection signal, on both sides
	// and for negative values too.
	cases = append(cases,
		tc{name: "unknown/book=0", book: 0, cand: 5000, wantMultiplier: 1.0, wantScore: 0},
		tc{name: "unknown/cand=0", book: 5000, cand: 0, wantMultiplier: 1.0, wantScore: 0},
		tc{name: "unknown/both=0", book: 0, cand: 0, wantMultiplier: 1.0, wantScore: 0},
		tc{name: "unknown/book=negative", book: -100, cand: 5000, wantMultiplier: 1.0, wantScore: 0},
		tc{name: "unknown/cand=negative", book: 5000, cand: -100, wantMultiplier: 1.0, wantScore: 0},
	)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMultiplier := durationScoreMultiplier(c.book, c.cand)
			gotScore := computeDurationScore(c.book, c.cand)
			assert.InDelta(t, c.wantMultiplier, gotMultiplier, 1e-9, "durationScoreMultiplier(%d, %d)", c.book, c.cand)
			assert.InDelta(t, c.wantScore, gotScore, 1e-9, "computeDurationScore(%d, %d)", c.book, c.cand)
		})
	}
}
