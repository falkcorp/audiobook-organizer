// file: internal/metafetch/service_rankscore_test.go
// version: 1.0.1
// guid: 61e828ea-550f-4785-ab97-1e894be5a182
// last-edited: 2026-10-10

package metafetch

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	tmock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// RankScore is Score with the missing-author / missing-narrator penalties left
// out. Score itself keeps them: the apply gates, the stored candidate order and
// every Candidates[0] read it, so they behave as before RankScore existed.

func searchRanked(t *testing.T, results []metadata.BookMetadata) (map[string]MetadataCandidate, []MetadataCandidate) {
	t.Helper()
	book := &database.Book{ID: "b-rank", Title: "Sample Field Notes"}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) { return book, nil },
	})
	svc.SetOverrideSources([]metadata.MetadataSource{
		&mockMetadataSource{name: "Audible", results: results},
	})
	resp, err := svc.SearchMetadataForBook(book.ID, book.Title, "Author 07")
	require.NoError(t, err)
	out := map[string]MetadataCandidate{}
	for _, c := range resp.Results {
		assertBreakdownExplainsScore(t, c)
		require.NotNil(t, c.ScoreBreakdown)
		assert.InDelta(t, c.RankScore, c.ScoreBreakdown.RankScore, 1e-12, "candidate and breakdown agree on RankScore")
		assert.InDelta(t, c.ScoreBreakdown.RankScore, RecomposeRankScore(c.ScoreBreakdown.Steps), 1e-9,
			"RankScore must be recomposable from the recorded steps")
		out[c.ASIN] = c
	}
	return out, resp.Results
}

func stepOf(c MetadataCandidate, id string) (ScoreStep, bool) {
	for _, s := range c.ScoreBreakdown.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return ScoreStep{}, false
}

// An author-less result keeps its x0.75 in Score and drops it from RankScore.
func TestRankScore_MissingAuthor(t *testing.T) {
	got, _ := searchRanked(t, []metadata.BookMetadata{
		{ASIN: "B0SYNTH001", Title: "Sample Field Notes", Author: "Author 07", Narrator: "Narrator 03"},
		{ASIN: "B0SYNTH002", Title: "Sample Field Notes", Narrator: "Narrator 03"},
	})
	with, without := got["B0SYNTH001"], got["B0SYNTH002"]
	require.NotNil(t, with.ScoreBreakdown)
	require.NotNil(t, without.ScoreBreakdown)
	step, ok := stepOf(without, "author")
	require.True(t, ok, "the author penalty step stays in the breakdown: %+v", without.ScoreBreakdown.Steps)
	assert.Equal(t, 0.75, step.Operand)
	assert.True(t, step.RankNeutral, "and is marked as left out of the ranking score")
	// Score ratio is HEAD's (0.75 / 1.5 = 0.5); RankScore ratio is neutral (1 / 1.5).
	assert.InDelta(t, 0.75/1.5, without.Score/with.Score, 1e-9)
	assert.InDelta(t, 1.0/1.5, without.RankScore/with.RankScore, 1e-9)
}

// Calibration: the has-narrator vs no-narrator pair of an Audible and an Open
// Library row for the same title and author. The narrator factor alone was
// 1.15/0.85 = 1.353 in Score and is 1.15 in RankScore; the whole score also
// carries the additive rich-metadata bonus (+0.05, "Present: narrator").
func TestRankScore_MissingNarrator_Calibration(t *testing.T) {
	got, _ := searchRanked(t, []metadata.BookMetadata{
		{ASIN: "B0SYNTH011", Title: "Sample Field Notes", Author: "Author 07", Narrator: "Narrator 03"},
		{ASIN: "B0SYNTH012", Title: "Sample Field Notes", Author: "Author 07"},
	})
	audible, ol := got["B0SYNTH011"], got["B0SYNTH012"]
	step, ok := stepOf(ol, "narrator_present")
	require.True(t, ok)
	assert.Equal(t, 0.85, step.Operand)
	assert.True(t, step.RankNeutral)
	has, ok := stepOf(audible, "narrator_present")
	require.True(t, ok)
	assert.Equal(t, 1.15, has.Operand)
	assert.False(t, has.RankNeutral)

	t.Logf("narrator factor: Score %.4f, RankScore %.4f", has.Operand/step.Operand, has.Operand)
	t.Logf("whole score: Score ratio %.4f, RankScore ratio %.4f",
		audible.Score/ol.Score, audible.RankScore/ol.RankScore)
	assert.InDelta(t, 1.05*1.15/0.85, audible.Score/ol.Score, 1e-9, "Score is HEAD's")
	assert.InDelta(t, 1.05*1.15, audible.RankScore/ol.RankScore, 1e-9, "RankScore leaves the 0.85 out")
}

// The search's stored order is HEAD's: by Score. A row whose RankScore leads
// still sits where its Score puts it.
func TestRankScore_StoredOrderIsByScore(t *testing.T) {
	_, ordered := searchRanked(t, []metadata.BookMetadata{
		{ASIN: "B0SYNTH031", Title: "Sample Field Notes", Author: "Author 07", Narrator: "Narrator 03"},
		{ASIN: "B0SYNTH032", Title: "Sample Field Notes", Author: "Author 07"},
		{ASIN: "B0SYNTH033", Title: "Sample Field Notes", Narrator: "Narrator 03"},
	})
	for i := 1; i < len(ordered); i++ {
		assert.GreaterOrEqual(t, ordered[i-1].Score, ordered[i].Score, "stored order is Score descending (HEAD)")
	}
}

func TestScoreRecorder_MulAbsenceRecomposes(t *testing.T) {
	rec := newScoreRecorder(0.8, "Title match", "")
	rec.mulAbsence("author", "Author missing", 0.75, "absent")
	rec.mul("narrator_match", "Narrator match", 1.3, "")
	rec.add("rich_metadata", "Rich metadata", 0.05, "", false)
	bd := rec.breakdown()
	assert.InDelta(t, (0.8*0.75*1.3)+0.05, bd.Score, 1e-12)
	assert.InDelta(t, (0.8*1.3)+0.05, bd.RankScore, 1e-12)
	assert.InDelta(t, bd.Score, RecomposeScore(bd.Steps), 1e-12)
	assert.InDelta(t, bd.RankScore, RecomposeRankScore(bd.Steps), 1e-12)
	assert.True(t, bd.IsConsistent(1e-9))
}

func TestScoreBrowseAnswer_MissingAuthorRanksNeutral(t *testing.T) {
	r := metadata.BookMetadata{ASIN: "B0SYNTH021", Title: "Sample Field Notes"}
	c := scoreBrowseAnswer(r, "Audible", "", nil, "Author 07", "", 0)
	assert.InDelta(t, browseNoTitleBase*0.75, c.Score, 1e-12, "Score keeps the 0.75")
	assert.InDelta(t, browseNoTitleBase, c.RankScore, 1e-12)
	assert.InDelta(t, c.RankScore, RecomposeRankScore(c.ScoreBreakdown.Steps), 1e-12)
}

// SortForDisplay orders by RankScore, keeps the looked-up-ASIN class first,
// treats a row with no rank_score (cached before it existed) as its Score, and
// does not touch its input.
func TestSortForDisplay(t *testing.T) {
	asin := &ScoreBreakdown{Steps: []ScoreStep{{ID: "base", Op: ScoreOpBase, Operand: 1}, {ID: "asin_match", Op: ScoreOpMultiply, Operand: 2}}}
	in := []MetadataCandidate{
		{Title: "X", Score: 1.05, RankScore: 1.05},
		{Title: "Y", Score: 0.9775, RankScore: 1.15},
		{Title: "old", Score: 1.10}, // no rank_score
		{Title: "own ASIN", Score: 0.5, RankScore: 0.5, ScoreBreakdown: asin},
	}
	snapshot := fmt.Sprint(in)
	out := SortForDisplay(in)
	var titles []string
	for _, c := range out {
		titles = append(titles, c.Title)
	}
	assert.Equal(t, []string{"own ASIN", "Y", "old", "X"}, titles)
	assert.Equal(t, snapshot, fmt.Sprint(in), "the stored order is not modified")
}

// Rerank A/B (reviewer probe). A names no author and no narrator: 0.905
// neutral, 0.905 x 0.75 x 0.85 = 0.577 at HEAD. B has every field: 0.86. With
// Score as at HEAD, A is outside the 0.05 rerank window, so the LLM is never
// asked and nothing is rescaled: stored order and Scores are HEAD's, and B
// (0.86) stays under the 0.90 apply floor.
func TestRerank_RankScoreDoesNotMoveTheWindowOrTheScores(t *testing.T) {
	orig := config.AppConfig.MetadataScoring
	config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.05
	config.AppConfig.MetadataScoring.LLMRerankTopK = 5
	t.Cleanup(func() { config.AppConfig.MetadataScoring = orig })

	a := MetadataCandidate{Title: "A", Source: "Audible", Score: 0.905 * 0.75 * 0.85, RankScore: 0.905}
	b := MetadataCandidate{Title: "B", Source: "Audible", Author: "Author 07", Narrator: "Narrator 03", Score: 0.86, RankScore: 0.86}
	llm := mocks.NewMockMetadataCandidateScorer(t) // any call fails the test
	svc := NewService(&database.MockStore{})
	svc.SetMetadataLLMScorer(llm)
	got := svc.RerankTopK(context.TODO(), &database.Book{}, []MetadataCandidate{b, a})
	require.Len(t, got, 2)
	assert.Equal(t, "B", got[0].Title)
	assert.Equal(t, 0.86, got[0].Score)
	assert.Equal(t, "A", got[1].Title)
	assert.InDelta(t, 0.905*0.75*0.85, got[1].Score, 1e-12)
	assert.Less(t, got[0].Score, 0.90, "B stays under the apply floor, as at HEAD")
}

// The final re-sort in RerankTopK follows the rescaled Score, not RankScore.
// P (Score 0.90, penalised: RankScore 1.20) and Q (Score 0.88, RankScore 0.88)
// are inside epsilon. The LLM prefers Q, so Q is rescaled to 0.90 and P to 0.88:
// Score order [Q, P] while RankScore order is [P (1.173), Q (0.90)].
func TestRerank_FinalSortFollowsScoreNotRankScore(t *testing.T) {
	orig := config.AppConfig.MetadataScoring
	config.AppConfig.MetadataScoring.LLMRerankEpsilon = 0.05
	config.AppConfig.MetadataScoring.LLMRerankTopK = 5
	t.Cleanup(func() { config.AppConfig.MetadataScoring = orig })

	p := MetadataCandidate{Title: "P", Source: "Audible", Score: 0.90, RankScore: 1.20}
	q := MetadataCandidate{Title: "Q", Source: "Audible", Score: 0.88, RankScore: 0.88}
	llm := mocks.NewMockMetadataCandidateScorer(t)
	llm.EXPECT().Score(tmock.Anything, tmock.Anything, tmock.Anything).Return([]float64{0.0, 1.0}, nil).Once()
	svc := NewService(&database.MockStore{})
	svc.SetMetadataLLMScorer(llm)
	got := svc.RerankTopK(context.TODO(), &database.Book{}, []MetadataCandidate{p, q})
	require.Len(t, got, 2)
	assert.Equal(t, []string{"Q", "P"}, []string{got[0].Title, got[1].Title}, "stored order follows the rescaled Score")
	assert.InDelta(t, 0.90, got[0].Score, 1e-12)
	assert.InDelta(t, 0.88, got[1].Score, 1e-12)
	assert.Greater(t, got[1].RankValue(), got[0].RankValue(), "fixture: RankScore order disagrees with Score order")
}

// When a rerank does run, Score is rescaled exactly as before and RankScore
// keeps its distance from it.
func TestRerank_RescalesScoreAsBeforeAndCarriesRankScore(t *testing.T) {
	pre := 0.8
	c := MetadataCandidate{Title: "P", Score: 0.9, RankScore: pre / 0.75,
		ScoreBreakdown: &ScoreBreakdown{Score: pre, RankScore: pre / 0.75,
			Steps: []ScoreStep{{ID: "base", Op: ScoreOpBase, Operand: pre, Running: pre}}}}
	recordRerank(&c, 0.5, 0.5, 0.9, pre)
	want := 0.9 * (pre / 0.75) / pre
	assert.Equal(t, 0.9, c.Score, "Score is the caller's rescaled value, untouched by recordRerank")
	assert.InDelta(t, want, c.RankScore, 1e-12)
	assert.InDelta(t, want, c.ScoreBreakdown.RankScore, 1e-12)
	assert.InDelta(t, want, RecomposeRankScore(c.ScoreBreakdown.Steps), 1e-12)
	assert.InDelta(t, 0.9, RecomposeScore(c.ScoreBreakdown.Steps), 1e-12)
}

// Merge with a rejected tier ("Sample Cats 3 / 1" shape, the reviewer's Big
// Cats probe): the merge ranks by (tier, Score). The owner-rejected volume-3
// row has the HIGHEST Score and RankScore, yet stays last, and two usable rows
// of the same tier keep Score order even where RankScore says otherwise -- both
// exactly as at HEAD. RankScore plays no part in the stored order.
func TestMergeCandidateRows_TiersAndScoreOrderIgnoreRankScore(t *testing.T) {
	mk := func(title string, score, rank float64) json.RawMessage {
		raw, err := json.Marshal(MetadataCandidate{Source: "Audible", Title: title, Score: score, RankScore: rank})
		require.NoError(t, err)
		return raw
	}
	rank := func(c MetadataCandidate) int {
		if c.Title == "Sample Cats 3" {
			return 3 // owner-rejected
		}
		return 0
	}
	got := mergeCandidateRows(
		[]json.RawMessage{mk("Sample Cats 3", 0.99, 1.4), mk("Sample Cats 2", 0.92, 1.3)},
		[]json.RawMessage{mk("Sample Cats 1", 0.95, 0.95)}, rank)
	require.Equal(t, []string{"Audible:Sample Cats 1", "Audible:Sample Cats 2", "Audible:Sample Cats 3"}, candSources(t, got))
}

// A row cached before RankScore existed decodes to RankScore 0, and every
// reader of it sees HEAD's Score.
func TestRankValue_PreDeployRowFallsBackToScore(t *testing.T) {
	var c MetadataCandidate
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t","source":"Audible","score":0.7}`), &c))
	assert.Equal(t, 0.0, c.RankScore)
	assert.Equal(t, 0.7, c.RankValue())
	c.RankScore = 0.9
	assert.Equal(t, 0.9, c.RankValue())
	assert.False(t, math.IsNaN(c.RankValue()))
}

// recordRerank with a candidate that has no RankScore: the ranking score
// falls back to the PRE-rerank score's ratio (1), not to the rescaled Score.
func TestRecordRerank_NoRankScoreFallsBackToPreScore(t *testing.T) {
	c := MetadataCandidate{Title: "N", Score: 0.9,
		ScoreBreakdown: &ScoreBreakdown{Score: 0.5, Steps: []ScoreStep{{ID: "base", Op: ScoreOpBase, Operand: 0.5, Running: 0.5}}}}
	recordRerank(&c, 0.5, 0.5, 0.9, 0.5)
	assert.InDelta(t, 0.9, c.RankScore, 1e-12)
}
