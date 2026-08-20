// file: internal/metafetch/service_scoring_breakdown_test.go
// version: 1.0.0
// guid: 9a4d7f21-5e83-4c06-b1d7-3f8092ac5e14
// last-edited: 2026-08-20

package metafetch

import (
	"math"
	"testing"

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
	if len(bd.Steps) != 1 || bd.Steps[0].Op != ScoreOpBase {
		t.Fatalf("expected a single base step, got %+v", bd.Steps)
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
