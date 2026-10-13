// file: internal/server/batch_apply_folder_test.go
// version: 1.1.0
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
// (the folder-parse variant, source folder_person_title), so its cache row
// hashes the folder's name and reads as a stale query at apply time. The row
// proves the folder was the question for the current author; only an owner's
// single review row may apply it -- the review page's bulk button, a script's
// pin and no pin keep identity_stale, a row fetched for another author or
// marked stale is refused outright, and so is everything while
// metadata_fetch.folder_parse_person_title is off. The stand-in for a title
// with nothing to search by (source folder_title) is behind no switch and was
// identity_stale on every path before the variant: it stays so, even for a row
// pin. Synthetic names.
func TestPlanCachedApply_FolderQueryRowAppliesOnlyFromAnOwnerRowPin(t *testing.T) {
	prev := config.AppConfig.MetadataFetch
	t.Cleanup(func() { config.AppConfig.MetadataFetch = prev })

	const folder = "The Paper Garden"
	dur := 36000
	authorID, otherID := 7, 8
	cand := metafetch.MetadataCandidate{Title: folder, Author: "Mara Quill", Score: 0.95, DurationSec: 36000, Source: "Audible"}
	blob, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	bulkPin := metafetch.PinOf(cand)
	bulkPin.Origin = metafetch.PinOriginReviewBulk
	scriptPin := metafetch.PinOf(cand) // no origin

	type tc struct {
		name         string
		title        string // the book's stored title
		hashAuthor   string // the author the row was fetched for
		liveID       int
		switchOn     bool
		pin          *metafetch.CandidatePin
		staleRow     bool
		bookASIN     string
		rowASIN      string // the ASIN the row was fetched for
		wantApplies  bool
		wantReviewer bool // the pinless plan reports the book as review-only
	}
	cases := []tc{
		{name: "owner row pin applies", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, pin: rowPin(cand), wantApplies: true},
		{name: "no pin is refused but review-only", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, wantReviewer: true},
		{name: "review bulk button is refused", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, pin: &bulkPin},
		{name: "script pin is refused", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, pin: &scriptPin},
		{name: "row fetched for another author is refused even for a row pin", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: otherID, switchOn: true, pin: rowPin(cand)},
		{name: "row hashed with no author is refused for a book with an author", title: "Mara Quill", hashAuthor: "", liveID: authorID, switchOn: true, pin: rowPin(cand)},
		{name: "row marked stale is refused even for a row pin", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, pin: rowPin(cand), staleRow: true},
		{name: "row fetched for another ASIN is refused even for a row pin", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, pin: rowPin(cand), bookASIN: "B0NEWASIN1", rowASIN: "B0OLDASIN1"},
		{name: "switch off: the book is asked by its stored title, the row proves nothing", title: "Mara Quill", hashAuthor: "Mara Quill", liveID: authorID, pin: rowPin(cand)},
		{name: "the stand-in for a blank title stays identity_stale for a row pin", title: "", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true, pin: rowPin(cand)},
		{name: "the stand-in for a blank title is not review-only either", title: "", hashAuthor: "Mara Quill", liveID: authorID, switchOn: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config.AppConfig.MetadataFetch.FolderParsePersonTitle = c.switchOn
			id := c.liveID
			b := &database.Book{ID: "b1", Title: c.title, AuthorID: &id,
				FilePath: "/library/Mara Quill/The Paper Garden/The Paper Garden.m4b", Duration: &dur}
			if c.bookASIN != "" {
				b.ASIN = &c.bookASIN
			}
			books := liveAuthorBooks{
				fakeBooks: fakeBooks{"b1": b},
				authors:   map[int]*database.Author{authorID: {ID: authorID, Name: "Mara Quill"}, otherID: {ID: otherID, Name: "Someone Else"}},
			}
			entry := &metafetch.MetadataCandidateCache{BookID: "b1", Stale: c.staleRow, FetchedForASIN: c.rowASIN,
				SourceHash: metafetch.BatchSourceHash("b1", folder, c.hashAuthor), Candidates: []json.RawMessage{blob}}
			svc := realIdentitySvc{fakeApplySvc: &fakeApplySvc{}, entry: entry}
			plan := planCachedApply(svc, books, "b1", nil, c.pin)
			if plan.Gate == nil {
				t.Fatalf("gate did not run: %+v", plan)
			}
			applies := plan.Gate.Allowed || plan.OwnerReviewed
			if applies != c.wantApplies {
				t.Fatalf("applies = %v (reason %q: %s, owner-reviewed %v), want %v",
					applies, plan.Gate.Reason, plan.Gate.Detail, plan.OwnerReviewed, c.wantApplies)
			}
			if !c.wantApplies && plan.Gate.Reason != applygate.ReasonIdentityStale {
				t.Errorf("reason = %q (%s), want %q", plan.Gate.Reason, plan.Gate.Detail, applygate.ReasonIdentityStale)
			}
			if c.pin == nil && plan.reviewOnly() != c.wantReviewer {
				t.Errorf("reviewOnly = %v, want %v", plan.reviewOnly(), c.wantReviewer)
			}
		})
	}
}
