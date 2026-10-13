// file: internal/server/batch_apply_folder_test.go
// version: 1.0.0
// guid: 0c58d2e7-4b19-4f60-a3d1-9e7f2b8a6c35
// last-edited: 2026-10-12

package server

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A book titled with its own author's name is searched by its work folder
// (the folder-parse variant), so its cache row hashes the folder's name and
// reads as a stale query at apply time. The row proves the folder was the
// question for the current author; only an owner's single review row may apply
// it -- the review page's bulk button, a script's pin and no pin keep
// identity_stale, and a row fetched for another author is refused outright.
// Synthetic names.
func TestPlanCachedApply_FolderQueryRowAppliesOnlyFromAnOwnerRowPin(t *testing.T) {
	prev := config.AppConfig.MetadataFetch
	t.Cleanup(func() { config.AppConfig.MetadataFetch = prev })
	config.AppConfig.MetadataFetch.FolderParsePersonTitle = true

	const folder = "The Paper Garden"
	dur := 36000
	authorID, otherID := 7, 8
	book := func(liveID int) *database.Book {
		id := liveID
		return &database.Book{ID: "b1", Title: "Mara Quill", AuthorID: &id,
			FilePath: "/library/Mara Quill/The Paper Garden/The Paper Garden.m4b", Duration: &dur}
	}
	books := func(liveID int) liveAuthorBooks {
		return liveAuthorBooks{
			fakeBooks: fakeBooks{"b1": book(liveID)},
			authors:   map[int]*database.Author{authorID: {ID: authorID, Name: "Mara Quill"}, otherID: {ID: otherID, Name: "Someone Else"}},
		}
	}
	cand := metafetch.MetadataCandidate{Title: folder, Author: "Mara Quill", Score: 0.95, DurationSec: 36000, Source: "Audible"}
	blob, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	entry := func(hashAuthor string) *metafetch.MetadataCandidateCache {
		return &metafetch.MetadataCandidateCache{BookID: "b1",
			SourceHash: metafetch.BatchSourceHash("b1", folder, hashAuthor), Candidates: []json.RawMessage{blob}}
	}
	bulkPin := metafetch.PinOf(cand)
	bulkPin.Origin = metafetch.PinOriginReviewBulk
	scriptPin := metafetch.PinOf(cand) // no origin

	cases := []struct {
		name         string
		hashAuthor   string
		liveID       int
		pin          *metafetch.CandidatePin
		wantApplies  bool
		wantReviewer bool // the pinless plan reports the book as review-only
	}{
		{name: "owner row pin applies", hashAuthor: "Mara Quill", liveID: authorID, pin: rowPin(cand), wantApplies: true},
		{name: "no pin is refused but review-only", hashAuthor: "Mara Quill", liveID: authorID, wantReviewer: true},
		{name: "review bulk button is refused", hashAuthor: "Mara Quill", liveID: authorID, pin: &bulkPin},
		{name: "script pin is refused", hashAuthor: "Mara Quill", liveID: authorID, pin: &scriptPin},
		{name: "row fetched for another author is refused even for a row pin", hashAuthor: "Mara Quill", liveID: otherID, pin: rowPin(cand)},
		{name: "row hashed with the stored title (not the folder) is refused", hashAuthor: "", liveID: authorID, pin: rowPin(cand)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := realIdentitySvc{fakeApplySvc: &fakeApplySvc{}, entry: entry(tc.hashAuthor)}
			plan := planCachedApply(svc, books(tc.liveID), "b1", nil, tc.pin)
			if plan.Gate == nil {
				t.Fatalf("gate did not run: %+v", plan)
			}
			applies := plan.Gate.Allowed || plan.OwnerReviewed
			if applies != tc.wantApplies {
				t.Fatalf("applies = %v (reason %q: %s, owner-reviewed %v), want %v",
					applies, plan.Gate.Reason, plan.Gate.Detail, plan.OwnerReviewed, tc.wantApplies)
			}
			if !tc.wantApplies && plan.Gate.Reason != applygate.ReasonIdentityStale {
				t.Errorf("reason = %q (%s), want %q", plan.Gate.Reason, plan.Gate.Detail, applygate.ReasonIdentityStale)
			}
			if tc.pin == nil && plan.reviewOnly() != tc.wantReviewer {
				t.Errorf("reviewOnly = %v, want %v", plan.reviewOnly(), tc.wantReviewer)
			}
		})
	}
}
