// file: internal/metafetch/service_scoring_breakdown_test.go
// version: 1.1.0
// guid: 9a4d7f21-5e83-4c06-b1d7-3f8092ac5e14
// last-edited: 2026-08-23

package metafetch

import (
	"math"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The scoring breakdown exists so a reviewer can check the machine's arithmetic
// instead of trusting it. That is only worth anything if the breakdown is
// provably a decomposition of the score the pipeline actually acts on.
//
// The golden fixtures in service_scoring_test.go pin specific totals, but they
// only cover cases someone already thought of. These are properties: they hold
// for every combination below, and would hold for combinations not enumerated.

// scoringInputs enumerates the full cross product of the fields that steer the
// pipeline: compilation titles, over-length titles, and each rich-metadata field
// independently (so the cap boundary is crossed from both sides).
func scoringInputs() []metadata.BookMetadata {
	titles := []string{
		"The Hobbit",
		"The Hobbit: The Complete Collection",                     // compilation
		"The Hobbit And Also Many Other Long Words",               // length penalty
		"The Complete Collection Of Many Other Long Words Indeed", // both
		"Utterly Unrelated Nonsense",                              // zero base
	}
	descs := []string{"", "a description"}
	covers := []string{"", "http://cover"}
	narrators := []string{"", "Rob Inglis"}
	isbns := []string{"", "9780261102217"}

	var out []metadata.BookMetadata
	for _, t := range titles {
		for _, d := range descs {
			for _, c := range covers {
				for _, n := range narrators {
					for _, i := range isbns {
						out = append(out, metadata.BookMetadata{
							Title: t, Author: "J.R.R. Tolkien",
							Description: d, CoverURL: c, Narrator: n, ISBN: i,
						})
					}
				}
			}
		}
	}
	return out
}

func searchWordsForHobbit() map[string]bool {
	return SignificantWords("The Hobbit")
}

// TestBreakdown_RecomposesToScore is the load-bearing property: replaying the
// recorded operations must reproduce the score, exactly.
//
// If this fails, the panel is showing a derivation that did not produce the
// number the pipeline used, which is worse than showing nothing.
func TestBreakdown_RecomposesToScore(t *testing.T) {
	words := searchWordsForHobbit()
	for _, r := range scoringInputs() {
		score, bd := ScoreOneResultWithBreakdown(r, words)

		if bd.Score != score {
			t.Fatalf("%q: breakdown.Score=%v, returned score=%v", r.Title, bd.Score, score)
		}
		if got := RecomposeScore(bd.Steps); math.Abs(got-score) > 1e-12 {
			t.Fatalf("%q: recompose=%.15f, score=%.15f (steps=%+v)", r.Title, got, score, bd.Steps)
		}
		if !bd.IsConsistent(1e-12) {
			t.Fatalf("%q: IsConsistent false for a breakdown that recomposes", r.Title)
		}
	}
}

// TestBreakdown_RunningMatchesPrefix guards the displayed column specifically.
// Running is what a reviewer reads down the panel; a correct final total with a
// wrong intermediate would still mislead, and recomposition alone would not
// catch it because it recomputes from Operand.
func TestBreakdown_RunningMatchesPrefix(t *testing.T) {
	words := searchWordsForHobbit()
	for _, r := range scoringInputs() {
		_, bd := ScoreOneResultWithBreakdown(r, words)
		for i := range bd.Steps {
			want := RecomposeScore(bd.Steps[:i+1])
			if got := bd.Steps[i].Running; math.Abs(got-want) > 1e-12 {
				t.Fatalf("%q step %d (%s): Running=%.15f, prefix replay=%.15f",
					r.Title, i, bd.Steps[i].ID, got, want)
			}
		}
	}
}

// TestBreakdown_MatchesPlainScorer pins the delegation. ScoreOneResult and
// ApplyNonBaseAdjustments now forward to the breakdown variants; if anyone
// re-inlines the arithmetic into either, the copies can drift and this fails.
func TestBreakdown_MatchesPlainScorer(t *testing.T) {
	words := searchWordsForHobbit()
	for _, r := range scoringInputs() {
		want := ScoreOneResult(r, words)
		got, _ := ScoreOneResultWithBreakdown(r, words)
		if want != got {
			t.Fatalf("%q: ScoreOneResult=%.15f, WithBreakdown=%.15f", r.Title, want, got)
		}

		base := computeF1Base(r, words)
		wantAdj := ApplyNonBaseAdjustments(base, r, len(words))
		gotAdj, _ := ApplyNonBaseAdjustmentsWithBreakdown(base, r, len(words))
		if wantAdj != gotAdj {
			t.Fatalf("%q: ApplyNonBaseAdjustments=%.15f, WithBreakdown=%.15f",
				r.Title, wantAdj, gotAdj)
		}
	}
}

// TestBreakdown_ZeroBaseIsExplained covers the early return. A candidate with no
// word overlap scores 0 and skips every bonus; the panel must be able to say so
// rather than render an empty box.
func TestBreakdown_ZeroBaseIsExplained(t *testing.T) {
	words := searchWordsForHobbit()
	r := metadata.BookMetadata{
		Title: "Utterly Unrelated Nonsense", Description: "d", CoverURL: "c",
		Narrator: "n", ISBN: "i",
	}
	score, bd := ScoreOneResultWithBreakdown(r, words)
	if score != 0 {
		t.Fatalf("expected zero score, got %v", score)
	}
	// The returned score and bd.Score must be the SAME number, not two
	// independently-sourced zeros. ScoreOneResultWithBreakdown derives the
	// base==0 return from the same scoreRecorder that produces bd, so this
	// can only diverge if that link is ever replaced by a hardcoded literal.
	if score != bd.Score {
		t.Fatalf("returned score (%v) and bd.Score (%v) must be the same value", score, bd.Score)
	}
	if len(bd.Steps) != 1 || bd.Steps[0].Op != ScoreOpBase {
		t.Fatalf("expected a single base step, got %+v", bd.Steps)
	}
	if bd.Steps[0].Operand != 0 || bd.Steps[0].Running != 0 {
		t.Fatalf("expected Operand=0 and Running=0, got Operand=%v Running=%v",
			bd.Steps[0].Operand, bd.Steps[0].Running)
	}
	if bd.Steps[0].Detail == "" {
		t.Fatal("zero-base step must explain itself")
	}
	// Rich metadata is present but deliberately NOT applied on this path.
	if !bd.IsConsistent(1e-12) {
		t.Fatal("zero-base breakdown must still be consistent")
	}
}

// TestBreakdown_EmptyIsNotConsistent pins the "nothing recorded" case, so an
// absent breakdown can never be mistaken for a proven zero.
func TestBreakdown_EmptyIsNotConsistent(t *testing.T) {
	if (ScoreBreakdown{Score: 0}).IsConsistent(1e-12) {
		t.Fatal("an empty breakdown must not report as consistent")
	}
}

// --- Search-path integration -------------------------------------------------
//
// The tests above prove ScoreOneResult's breakdown recomposes. That is NOT the
// number a reviewer sees. MetadataCandidate.Score carries several more layers
// (author, narrator, series, transcription, duration) and can be REPLACED
// outright by the LLM reranker or by a direct ASIN match.
//
// So the real acceptance gate is here: for every candidate the search path
// actually emits, replaying its recorded steps must reproduce its Score. If this
// fails, the evidence panel would present a derivation of a number the pipeline
// did not use.

func intPtr(v int) *int { return &v }

func assertBreakdownExplainsScore(t *testing.T, c MetadataCandidate) {
	t.Helper()
	if c.ScoreBreakdown == nil {
		t.Fatalf("%q: candidate from the search path has no breakdown", c.Title)
	}
	if got := RecomposeScore(c.ScoreBreakdown.Steps); math.Abs(got-c.Score) > 1e-12 {
		t.Fatalf("%q: steps recompose to %.15f but Score is %.15f\nsteps=%+v",
			c.Title, got, c.Score, c.ScoreBreakdown.Steps)
	}
	if c.ScoreBreakdown.Score != c.Score {
		t.Fatalf("%q: breakdown.Score=%.15f, candidate.Score=%.15f",
			c.Title, c.ScoreBreakdown.Score, c.Score)
	}
	for i := range c.ScoreBreakdown.Steps {
		want := RecomposeScore(c.ScoreBreakdown.Steps[:i+1])
		if got := c.ScoreBreakdown.Steps[i].Running; math.Abs(got-want) > 1e-12 {
			t.Fatalf("%q step %d (%s): Running=%.15f, prefix replay=%.15f",
				c.Title, i, c.ScoreBreakdown.Steps[i].ID, got, want)
		}
	}
}

func TestSearchPath_BreakdownExplainsEveryCandidateScore(t *testing.T) {
	transcribed := "Mistborn The Final Empire"
	cases := []struct {
		name    string
		book    *database.Book
		results []metadata.BookMetadata
	}{
		{
			name: "narrator present and absent, author match and mismatch",
			book: &database.Book{ID: "b1", Title: "Mistborn"},
			results: []metadata.BookMetadata{
				{Title: "Mistborn", Author: "Brandon Sanderson", Narrator: "Michael Kramer"},
				{Title: "Mistborn", Author: "Someone Else"},
				{Title: "Mistborn: The Complete Collection", Author: "Brandon Sanderson",
					Description: "d", CoverURL: "c", ISBN: "9780765311788"},
			},
		},
		{
			name: "transcription hints drive a multiplier",
			book: &database.Book{ID: "b2", Title: "Mistborn", TranscribedTitle: &transcribed},
			results: []metadata.BookMetadata{
				{Title: "Mistborn The Final Empire", Author: "Brandon Sanderson", Narrator: "Kramer"},
				{Title: "Mistborn Well of Ascension", Author: "Brandon Sanderson"},
			},
		},
		{
			name: "duration comparison, matching and diverging",
			book: &database.Book{ID: "b3", Title: "Mistborn", Duration: intPtr(86400)},
			results: []metadata.BookMetadata{
				{Title: "Mistborn", Author: "Brandon Sanderson", DurationSec: 86000},
				{Title: "Mistborn", Author: "Brandon Sanderson", DurationSec: 3600},
				{Title: "Mistborn", Author: "Brandon Sanderson"}, // unknown runtime
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := tc.book
			svc := NewService(&database.MockStore{
				GetBookByIDFunc: func(id string) (*database.Book, error) { return book, nil },
			})
			svc.SetOverrideSources([]metadata.MetadataSource{
				&mockMetadataSource{name: "test-source", results: tc.results},
			})

			resp, err := svc.SearchMetadataForBook(book.ID, book.Title)
			if err != nil {
				t.Fatalf("search failed: %v", err)
			}
			if len(resp.Results) == 0 {
				t.Fatal("expected at least one candidate")
			}
			for _, c := range resp.Results {
				assertBreakdownExplainsScore(t, c)
			}
		})
	}
}

// TestRecordRerank_ReplacesRatherThanScales pins the one op that is not an
// adjustment. The reranker substitutes its own judgement; recording it as a
// multiply would recompose correctly while telling the reviewer that some
// signal was worth a factor it never had.
func TestRecordRerank_ReplacesRatherThanScales(t *testing.T) {
	c := MetadataCandidate{
		Title: "Mistborn",
		Score: 0.9,
		ScoreBreakdown: &ScoreBreakdown{
			Score: 0.4,
			Steps: []ScoreStep{{
				ID: "base", Label: "Title/author match", Op: ScoreOpBase,
				Operand: 0.4, Running: 0.4,
			}},
		},
	}
	recordRerank(&c, 0.75, 0.2, 1.1)

	last := c.ScoreBreakdown.Steps[len(c.ScoreBreakdown.Steps)-1]
	if last.Op != ScoreOpReplace {
		t.Fatalf("rerank recorded as %q, want %q", last.Op, ScoreOpReplace)
	}
	if last.Operand != 0.9 {
		t.Fatalf("replace operand = %v, want the candidate's final score 0.9", last.Operand)
	}
	if got := RecomposeScore(c.ScoreBreakdown.Steps); got != 0.9 {
		t.Fatalf("recompose after replace = %v, want 0.9", got)
	}
	// The window bounds must be visible, because they come from OTHER candidates.
	if !strings.Contains(last.Detail, "0.200") || !strings.Contains(last.Detail, "1.100") {
		t.Fatalf("rerank detail must name the rescale window, got %q", last.Detail)
	}
}

// TestRecomposeScore_ReplaceResetsTotal is the unit-level guard on the new op.
func TestRecomposeScore_ReplaceResetsTotal(t *testing.T) {
	steps := []ScoreStep{
		{ID: "base", Op: ScoreOpBase, Operand: 0.5, Running: 0.5},
		{ID: "x", Op: ScoreOpMultiply, Operand: 4, Running: 2},
		{ID: "r", Op: ScoreOpReplace, Operand: 0.3, Running: 0.3},
		{ID: "y", Op: ScoreOpAdd, Operand: 0.1, Running: 0.4},
	}
	if got := RecomposeScore(steps); math.Abs(got-0.4) > 1e-12 {
		t.Fatalf("recompose=%v, want 0.4 (replace must discard prior total)", got)
	}
}
