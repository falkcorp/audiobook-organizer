// file: internal/server/batch_apply_transcribed_test.go
// version: 1.2.0
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

// A Doctor Who book with a blank title, whose intro transcription names it
// and whose top candidate is exactly that title -- the case the
// transcribed-title lift would otherwise pass -- is refused on both bulk
// paths, including the review page's bulk button, and still applies from a
// single review row the owner approved.
func TestBulkApplyPlanners_OwnerManualOnly(t *testing.T) {
	const chimes = "Doctor Who: The Chimes of Midnight"
	dur := 36000
	book := &database.Book{ID: "b1", Title: "", TranscribedTitle: strPtr(chimes),
		FilePath: "/lib/Unknown Author/Unknown Title/book.m4b", Duration: &dur}
	cand := metafetch.MetadataCandidate{Title: chimes, Author: "Robert Shearman", Score: 0.95, DurationSec: 36000, Source: "Audible"}
	stale := fmt.Errorf("%w: book b1 (stored a, current b)", metafetch.ErrStaleMetadataCache)
	bulkPin := metafetch.PinOf(cand)
	bulkPin.Origin = metafetch.PinOriginReviewBulk

	cases := []struct {
		name        string
		opResult    bool
		pin         *metafetch.CandidatePin
		wantAllowed bool
	}{
		{name: "cached, no pin (script / preview)"},
		{name: "cached, review-page bulk button", pin: &bulkPin},
		{name: "op result", opResult: true},
		{name: "cached, single review row approved", pin: rowPin(cand), wantAllowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			books := fakeBooks{"b1": book}
			var plan cachedApplyPlan
			if tc.opResult {
				c := cand
				plan = planOpResultApply(books, "b1", CandidateResult{Book: CandidateBookInfo{ID: "b1"}, Candidate: &c,
					SearchQuery: chimes, SearchQuerySource: metabatch.SearchQuerySourceTranscribedTitle}, nil)
			} else {
				blob, err := json.Marshal(cand)
				if err != nil {
					t.Fatal(err)
				}
				svc := &fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: stale, queryMatches: map[string]bool{chimes: true}}
				plan = planCachedApply(svc, books, "b1", nil, tc.pin)
			}
			if plan.Gate == nil {
				t.Fatalf("gate did not run: %+v", plan)
			}
			applies := plan.Gate.Allowed || plan.OwnerReviewed
			if applies != tc.wantAllowed {
				t.Fatalf("applies = %v (reason %q: %s, owner-reviewed %v), want %v",
					applies, plan.Gate.Reason, plan.Gate.Detail, plan.OwnerReviewed, tc.wantAllowed)
			}
			if !tc.wantAllowed && plan.Gate.Reason != applygate.ReasonOwnerManualOnly {
				t.Errorf("reason = %q (%s), want %q", plan.Gate.Reason, plan.Gate.Detail, applygate.ReasonOwnerManualOnly)
			}
		})
	}
}

// realIdentitySvc is fakeApplySvc with the REAL identity checks
// (metafetch.Service) over a real batch-shaped cache row, so a test sees what
// the hash proves rather than what a fake says.
type realIdentitySvc struct {
	*fakeApplySvc
	entry *metafetch.MetadataCandidateCache
}

func (r realIdentitySvc) GetCachedCandidates(string) (*metafetch.MetadataCandidateCache, bool, error) {
	return r.entry, true, nil
}
func (r realIdentitySvc) ValidateCachedIdentityForBook(e *metafetch.MetadataCandidateCache, b *database.Book, live []string) error {
	return (&metafetch.Service{}).ValidateCachedIdentityForBook(e, b, live)
}
func (r realIdentitySvc) CachedQueryMatchesIdentity(e *metafetch.MetadataCandidateCache, b *database.Book, live []string, q string) bool {
	return (&metafetch.Service{}).CachedQueryMatchesIdentity(e, b, live, q)
}

// A row the batch fetch wrote for the transcribed query proves the identity
// only for the author it was fetched with. GetBookByID leaves Book.Author nil,
// and an empty snapshot used to count as a current author form, so a row
// hashed with no author -- or one fetched before the author changed -- still
// passed. Both are refused now; the row for the current author passes.
func TestPlanCachedApply_TranscribedRowNeedsTheCurrentAuthor(t *testing.T) {
	const hulk = "Marvel's Planet Hulk"
	dur := 36000
	pakID, otherID := 7, 8
	cases := []struct {
		name        string
		hashAuthor  string
		liveID      int
		wantAllowed bool
	}{
		{name: "row for the current author passes", hashAuthor: "Greg Pak", liveID: pakID, wantAllowed: true},
		{name: "author changed since the fetch is refused", hashAuthor: "Greg Pak", liveID: otherID},
		{name: "row hashed with no author is refused for a book with an author", hashAuthor: "", liveID: pakID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.liveID
			book := &database.Book{ID: "b1", Title: "", TranscribedTitle: strPtr(hulk), AuthorID: &id,
				FilePath: "/lib/Greg Pak/Unknown Title/book.m4b", Duration: &dur}
			books := liveAuthorBooks{
				fakeBooks: fakeBooks{"b1": book},
				authors: map[int]*database.Author{pakID: {ID: pakID, Name: "Greg Pak"},
					otherID: {ID: otherID, Name: "Someone Else"}},
			}
			cand := metafetch.MetadataCandidate{Title: hulk, Author: "Greg Pak", Score: 0.95, DurationSec: 36000, Source: "Audible"}
			blob, err := json.Marshal(cand)
			if err != nil {
				t.Fatal(err)
			}
			svc := realIdentitySvc{fakeApplySvc: &fakeApplySvc{}, entry: &metafetch.MetadataCandidateCache{BookID: "b1",
				SourceHash: metafetch.BatchSourceHash("b1", hulk, tc.hashAuthor), Candidates: []json.RawMessage{blob}}}
			plan := planCachedApply(svc, books, "b1", nil, nil)
			if plan.Gate == nil {
				t.Fatalf("gate did not run: %+v", plan)
			}
			if plan.Gate.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", plan.Gate.Allowed, plan.Gate.Reason, plan.Gate.Detail, tc.wantAllowed)
			}
			if !tc.wantAllowed && plan.Gate.Reason != applygate.ReasonIdentityStale {
				t.Errorf("reason = %q (%s), want %q", plan.Gate.Reason, plan.Gate.Detail, applygate.ReasonIdentityStale)
			}
		})
	}
}

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
		{name: "cached: heard \"X, by A\", candidate X by B stays stale",
			book:        book(func(b *database.Book) { b.TranscribedAuthor = strPtr("Greg Pak") }),
			cand:        cand(func(c *metafetch.MetadataCandidate) { c.Author = "Someone Else" }),
			identityErr: stale, queryMatches: map[string]bool{hulk: true}, wantReason: applygate.ReasonIdentityStale},
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
			if !tc.wantAllowed {
				// A refusal never reads as an accepted identity: no evidence
				// at all on identity_stale, and on any other refusal the
				// wording says another check refused.
				line := batchApplyRefusedLine("b1", applyOutcome{Reason: plan.Reason, Gate: plan.Gate}.withPlan(plan))
				if strings.Contains(line, "; identity: ") {
					t.Errorf("refused line reads as an accepted identity: %s", line)
				}
				if plan.Gate.Reason == applygate.ReasonIdentityStale && plan.Candidate.IdentityEvidence != nil {
					t.Errorf("evidence stamped on an identity_stale refusal: %+v", plan.Candidate.IdentityEvidence)
				}
				if plan.Candidate.IdentityEvidence != nil && !strings.Contains(line, "but the refusal above stands") {
					t.Errorf("refused line with evidence does not say the refusal stands: %s", line)
				}
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
