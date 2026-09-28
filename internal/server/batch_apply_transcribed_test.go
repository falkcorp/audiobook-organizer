// file: internal/server/batch_apply_transcribed_test.go
// version: 1.0.0
// guid: 6a7c9ec1-ca94-4bd5-b088-7920aaeb18af
// last-edited: 2026-09-28

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The bulk apply's two planners hand the gate a transcribed search only when
// the book as it is NOW would be searched by exactly that transcription and
// (cached path) the cache row was fetched for it. Then a candidate matching
// the transcription passes and says why; everything else keeps its refusal.
func TestBulkApplyPlanners_TranscribedTitleEvidence(t *testing.T) {
	const hulk = "Marvel's Planet Hulk"
	dur := 36000
	book := func(mod func(*database.Book)) *database.Book {
		b := &database.Book{ID: "b1", Title: "", TranscribedTitle: strPtr(hulk),
			FilePath: "/lib/Greg Pak/Unknown Title/book.m4b", Duration: &dur}
		if mod != nil {
			mod(b)
		}
		return b
	}
	cand := func(mod func(*metafetch.MetadataCandidate)) metafetch.MetadataCandidate {
		c := metafetch.MetadataCandidate{Title: hulk, Author: "Greg Pak", Score: 0.95, DurationSec: 36000, Source: "Audible"}
		if mod != nil {
			mod(&c)
		}
		return c
	}
	stale := fmt.Errorf("%w: book b1 (stored a, current b)", metafetch.ErrStaleMetadataCache)

	cases := []struct {
		name         string
		book         *database.Book
		cand         metafetch.MetadataCandidate
		opResult     *CandidateResult // nil: the cached path
		identityErr  error
		queryMatches map[string]bool
		wantAllowed  bool
		wantReason   string
	}{
		{name: "cached: row fetched for the transcription passes", book: book(nil), cand: cand(nil),
			identityErr: stale, queryMatches: map[string]bool{hulk: true}, wantAllowed: true},
		{name: "cached: ASIN conflict still refused", book: book(func(b *database.Book) { b.ASIN = strPtr("B000000001") }),
			cand:        cand(func(c *metafetch.MetadataCandidate) { c.ASIN = "B000000002" }),
			identityErr: stale, queryMatches: map[string]bool{hulk: true}, wantReason: applygate.ReasonASINConflict},
		{name: "cached: row fetched for another query stays stale", book: book(nil), cand: cand(nil),
			identityErr: stale, wantReason: applygate.ReasonIdentityStale},
		{name: "cached: non-stale identity error stays stale", book: book(nil), cand: cand(nil),
			identityErr: errors.New("read failed"), queryMatches: map[string]bool{hulk: true}, wantReason: applygate.ReasonIdentityStale},
		{name: "cached: book got a real title since the fetch stays stale",
			book: book(func(b *database.Book) { b.Title = "Something Else" }), cand: cand(nil),
			identityErr: stale, queryMatches: map[string]bool{hulk: true}, wantReason: applygate.ReasonIdentityStale},
		{name: "op result: searched by the transcription passes", book: book(nil), cand: cand(nil),
			opResult:    &CandidateResult{Book: CandidateBookInfo{ID: "b1"}, SearchQuery: hulk, SearchQuerySource: metabatch.SearchQuerySourceTranscribedTitle},
			wantAllowed: true},
		{name: "op result: transcribed search, candidate for another title stays stale", book: book(nil),
			cand:       cand(func(c *metafetch.MetadataCandidate) { c.Title = "World War Hulk" }),
			opResult:   &CandidateResult{Book: CandidateBookInfo{ID: "b1"}, SearchQuery: hulk, SearchQuerySource: metabatch.SearchQuerySourceTranscribedTitle},
			wantReason: applygate.ReasonIdentityStale},
		{name: "op result: transcription since changed stays stale",
			book: book(func(b *database.Book) { b.TranscribedTitle = strPtr("World War Hulk") }), cand: cand(nil),
			opResult:   &CandidateResult{Book: CandidateBookInfo{ID: "b1"}, SearchQuery: hulk, SearchQuerySource: metabatch.SearchQuerySourceTranscribedTitle},
			wantReason: applygate.ReasonIdentityStale},
		{name: "op result: folder-title search of a chapter-titled book stays stale",
			book: book(func(b *database.Book) {
				b.Title, b.TranscribedTitle, b.FilePath = "Chapter 3", nil, "/lib/Greg Pak/Planet Hulk/03.m4b"
			}),
			cand: cand(func(c *metafetch.MetadataCandidate) { c.Title = "Planet Hulk" }),
			opResult: &CandidateResult{Book: CandidateBookInfo{ID: "b1", Title: "Chapter 3"}, SearchQuery: "Planet Hulk",
				SearchQuerySource: metabatch.SearchQuerySourceFolderTitle},
			wantReason: applygate.ReasonIdentityStale},
		{name: "op result: folder-title search of a blank-titled book stays stale",
			book: book(func(b *database.Book) { b.TranscribedTitle, b.FilePath = nil, "/lib/Greg Pak/Planet Hulk/book.m4b" }),
			cand: cand(func(c *metafetch.MetadataCandidate) { c.Title = "Planet Hulk" }),
			opResult: &CandidateResult{Book: CandidateBookInfo{ID: "b1"}, SearchQuery: "Planet Hulk",
				SearchQuerySource: metabatch.SearchQuerySourceFolderTitle},
			wantReason: applygate.ReasonIdentityStale},
		{name: "op result: legacy row with no title and no query stays stale", book: book(nil), cand: cand(nil),
			opResult:   &CandidateResult{Book: CandidateBookInfo{ID: "b1"}},
			wantReason: applygate.ReasonIdentityStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			books := fakeBooks{"b1": tc.book}
			var plan cachedApplyPlan
			if tc.opResult != nil {
				c := tc.cand
				cr := *tc.opResult
				cr.Candidate = &c
				plan = planOpResultApply(books, "b1", cr, nil)
			} else {
				blob, err := json.Marshal(tc.cand)
				if err != nil {
					t.Fatal(err)
				}
				svc := &fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: tc.identityErr, queryMatches: tc.queryMatches}
				plan = planCachedApply(svc, books, "b1", nil, nil)
			}
			if plan.Gate == nil {
				t.Fatalf("gate did not run: %+v", plan)
			}
			if plan.Gate.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", plan.Gate.Allowed, plan.Gate.Reason, plan.Gate.Detail, tc.wantAllowed)
			}
			if tc.wantReason != "" && plan.Gate.Reason != tc.wantReason {
				t.Errorf("reason = %q (%s), want %q", plan.Gate.Reason, plan.Gate.Detail, tc.wantReason)
			}
			if tc.wantAllowed {
				ev := plan.Candidate.IdentityEvidence
				if ev == nil || ev.Kind != metafetch.IdentityEvidenceTranscribedTitle || ev.Query != hulk {
					t.Fatalf("candidate evidence = %+v, want transcribed_title %q", ev, hulk)
				}
				line := batchApplyAppliedLine("b1", applyOutcome{Applied: true, Gate: plan.Gate}.withPlan(plan))
				if !strings.Contains(line, `identity: found by searching the transcribed title`) {
					t.Errorf("applied line does not say why it passed: %s", line)
				}
			}
		})
	}
}
