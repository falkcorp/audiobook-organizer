// file: internal/server/rank_score_differential_test.go
// version: 1.0.0
// guid: 437d8538-51b0-43d0-b31b-9181ba6ba5a2
// last-edited: 2026-10-10

package server

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// RankScore (the score without the missing-author / missing-narrator
// penalties) orders the interactive Search/Browse dialog and nothing else.
// These tests turn each reviewer probe of PR #3900 into a differential check:
// the stored candidate order and every gate verdict are the ones HEAD produced,
// whatever RankScore says. They hold trivially while Score is unchanged; they
// are here so a later change that lets RankScore into a gate, a pick or a sort
// of the stored row fails.

// xyRow is "Big Cats 1" with two candidates in HEAD's stored order:
//
//	X: narrated, wrong volume       Score 1.05    RankScore 1.05  (stored first)
//	Y: no narrator, right volume    Score 0.9775  RankScore 1.15  (1.15 x 0.85 at HEAD)
//
// Ordered by RankScore Y would lead and pass; HEAD's top row is X, which the
// sequence guard refuses.
func xyRow() (x, y metafetch.MetadataCandidate) {
	x = metafetch.MetadataCandidate{Source: "Audible", Title: "Big Cats 3", SeriesPosition: "3",
		Narrator: "Narrator 03", Score: 1.05, RankScore: 1.05, DurationSec: 36000}
	y = metafetch.MetadataCandidate{Source: "Audible", Title: "Big Cats 1", SeriesPosition: "1",
		Score: 0.9775, RankScore: 1.15, DurationSec: 36000}
	return x, y
}

func tenHourBooks() fakeBooks {
	tenHours := 36000
	return fakeBooks{"b1": {ID: "b1", Title: "Big Cats 1", FilePath: "/lib/A/Big Cats/Big Cats 1.m4b", Duration: &tenHours}}
}

func TestRankScore_XYTopRow_CachedApplyRefusesLikeHEAD(t *testing.T) {
	x, y := xyRow()
	solo := &fakeApplySvc{candidates: candidateJSON(t, y)}
	if out := applyCachedCandidateForBook(solo, tenHourBooks(), "b1", false, nil); !out.Applied {
		// Y alone clears the 0.90 floor at HEAD's Score (0.9775) and is the
		// right volume: gating it instead of X would apply a book HEAD refused.
		t.Fatalf("fixture: Y alone must apply, got reason=%q gate=%+v", out.Reason, out.Gate)
	}
	svc := &fakeApplySvc{candidates: append(candidateJSON(t, x), candidateJSON(t, y)...)}
	out := applyCachedCandidateForBook(svc, tenHourBooks(), "b1", false, nil)
	if out.Applied || out.Gate == nil || out.Gate.Reason != applygate.ReasonSequenceMismatch {
		t.Fatalf("applied=%v gate=%+v, want refused with %q (the stored top row is the wrong volume)",
			out.Applied, out.Gate, applygate.ReasonSequenceMismatch)
	}
}

// The reviewer's rerank case: A (no author, no narrator) is 0.905 neutral and
// 0.905 x 0.75 x 0.85 = 0.577 at HEAD; B (every field) is 0.86 both ways. The
// floor is 0.90. HEAD's stored scores are untouched, so both are refused.
func TestRankScore_RerankAB_GateRefusesLikeHEAD(t *testing.T) {
	a := metafetch.MetadataCandidate{Source: "Audible", Title: "Big Cats 1", SeriesPosition: "1", Score: 0.905 * 0.75 * 0.85, RankScore: 0.905, DurationSec: 36000}
	b := metafetch.MetadataCandidate{Source: "Audible", Title: "Big Cats 1", SeriesPosition: "1", Author: "Author 07", Narrator: "Narrator 03", Score: 0.86, RankScore: 0.86, DurationSec: 36000}
	for name, c := range map[string]metafetch.MetadataCandidate{"A": a, "B": b} {
		svc := &fakeApplySvc{candidates: candidateJSON(t, c)}
		out := applyCachedCandidateForBook(svc, tenHourBooks(), "b1", false, nil)
		if out.Applied || out.Gate == nil || out.Gate.Reason != applygate.ReasonScoreBelowFloor {
			t.Fatalf("%s: applied=%v gate=%+v, want %q", name, out.Applied, out.Gate, applygate.ReasonScoreBelowFloor)
		}
	}
}

// A row cached before RankScore existed carries no rank_score. Its direct-ASIN
// lookup candidate (replace step, no asin_match multiply) is refused on its
// Score exactly as at HEAD, and adding a high rank_score to it changes nothing.
func TestRankScore_PreDeployDirectASINRow_VerdictIndependentOfRankScore(t *testing.T) {
	direct := metafetch.MetadataCandidate{Source: "Audnexus (Audible)", Title: "Big Cats 1", SeriesPosition: "1",
		Score: 0.6, DurationSec: 36000,
		ScoreBreakdown: &metafetch.ScoreBreakdown{Score: 0.6, Steps: []metafetch.ScoreStep{
			{ID: "base", Op: metafetch.ScoreOpBase, Operand: 0.5, Running: 0.5},
			{ID: "asin_match", Op: metafetch.ScoreOpReplace, Operand: 0.6, Running: 0.6},
		}}}
	var reasons []string
	for _, rank := range []float64{0, 1.4} {
		c := direct
		c.RankScore = rank
		out := applyCachedCandidateForBook(&fakeApplySvc{candidates: candidateJSON(t, c)}, tenHourBooks(), "b1", false, nil)
		if out.Applied || out.Gate == nil {
			t.Fatalf("rank %v: applied=%v gate=%+v, want refused", rank, out.Applied, out.Gate)
		}
		reasons = append(reasons, out.Gate.Reason)
	}
	if reasons[0] != applygate.ReasonScoreBelowFloor || reasons[0] != reasons[1] {
		t.Fatalf("verdicts %v, want score_below_floor both ways", reasons)
	}
}

func TestRankScore_StoredFirstRowIsWhatEverySiteReads(t *testing.T) {
	x, y := xyRow()
	both := append(candidateJSON(t, x), candidateJSON(t, y)...)

	// Op-results pick.
	entry := mustCandidateCache(t, "b1", x)
	entry.Candidates = both
	res := candidateResultFromEntry(&database.MockStore{}, CandidateBookInfo{}, "b1", "Big Cats 1", entry)
	if res.Candidate == nil || res.Candidate.Title != "Big Cats 3" {
		t.Fatalf("op-results candidate %+v, want the stored first row (Big Cats 3)", res.Candidate)
	}

	// Claim loader.
	_, cand, err := cachedClaimLoader(&fakeApplySvc{candidates: both}, tenHourBooks())("b1")
	if err != nil || cand == nil || cand.Title != "Big Cats 3" {
		t.Fatalf("claim = %+v err=%v, want the stored first row (Big Cats 3)", cand, err)
	}

	// Transcription auto-match.
	book := &database.Book{ID: "b-rank", Title: "Big Cats 1"}
	tentry := mustCandidateCache(t, "b-rank", x)
	tentry.SearchFingerprint = metafetch.FingerprintPrefix + "x"
	tentry.Candidates = both
	store, _ := newTOCTOUCacheStore(t, book, tentry, tentry)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	top, found, err := s.SearchTranscriptionCandidate(context.Background(), "b-rank", "", "")
	if err != nil || !found || top.Title != "Big Cats 3" {
		t.Fatalf("transcription top = %+v found=%v err=%v, want the stored first row (Big Cats 3)", top, found, err)
	}
}
