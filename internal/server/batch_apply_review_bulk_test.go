// file: internal/server/batch_apply_review_bulk_test.go
// version: 1.1.0
// guid: 5f2c8a91-3d6e-4b17-9a40-c7e1b5d3f820
// last-edited: 2026-10-07
//
// Owner ruling 2026-09-27: EVERY apply button on the /review page (single
// row, Apply selected, Apply page, Apply high confidence, group Apply All) is
// the owner's manual apply and overrides the certainty gate the way the
// single-row Apply does. Bulk buttons pin each book's candidate with origin
// review_bulk, or send the hashless review_bulk owner marker for a book the
// lane held no hash for. Scripts, API callers and automatic applies stay
// fully gated.

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// bulkPin is the pin a review-page bulk button sends for a loaded row.
func bulkPin(c metafetch.MetadataCandidate) *metafetch.CandidatePin {
	p := metafetch.PinOf(c)
	p.Origin = metafetch.PinOriginReviewBulk
	return &p
}

// ownerMarker is the hashless marker a bulk button sends for a book whose row
// the lane had not loaded.
func ownerMarker() *metafetch.CandidatePin {
	return &metafetch.CandidatePin{Origin: metafetch.PinOriginReviewBulk}
}

// refusedByAuthorAndTranscription is a book the gate refuses on
// transcription_mismatch AND author_not_in_path: its files were transcribed
// as another title, and nothing names the candidate's author.
func refusedByAuthorAndTranscription() (fakeBooks, metafetch.MetadataCandidate) {
	tenHours := 36000
	heard := "Something Else Entirely"
	books := fakeBooks{"b1": {
		ID: "b1", Title: "Moon Book", FilePath: "/lib/Unknown Author/Moon Book/Moon Book.m4b",
		Duration: &tenHours, TranscribedTitle: &heard,
	}}
	cand := metafetch.MetadataCandidate{Title: "Moon Book", Author: "Zed Quill", Source: "Audible", Score: 0.99, DurationSec: 36000}
	return books, cand
}

func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

// The fixture must really refuse on both legs, or the override tests below
// prove nothing; a pinless (script/API) request stays hard-gated.
func TestReviewBulk_FixtureRefusesAndPinlessIsGated(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, nil, "")
	if out.Applied || out.OwnerReviewed || out.Reason != applySkipGateBlocked {
		t.Fatalf("pinless: outcome %+v, want gate_blocked", out)
	}
	reasons := out.Gate.RefusingReasons()
	for _, want := range []string{applygate.ReasonTranscriptionMismatch, applygate.ReasonAuthorNotInPath} {
		if !hasReason(reasons, want) {
			t.Fatalf("fixture refusals %v lack %s", reasons, want)
		}
	}
}

// A bulk pin and the hashless marker both lift author_not_in_path and
// transcription_mismatch, and the apply records the override in the change
// history exactly like a row override (OwnerReviewed + every refusing reason).
// Only the row pin overwrites; bulk stays fill-only (A3#3).
func TestReviewBulk_OverridesCertaintyLegsAndRecordsIt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pin          func(metafetch.MetadataCandidate) *metafetch.CandidatePin
		wantFillOnly bool
	}{
		{"bulk pin", bulkPin, true},
		{"owner marker", func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return ownerMarker() }, true},
		{"row pin", rowPin, false},
	} {
		name, pin := tc.name, tc.pin
		t.Run(name, func(t *testing.T) {
			books, cand := refusedByAuthorAndTranscription()
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin(cand), "")
			if !out.Applied || !out.OwnerReviewed {
				t.Fatalf("outcome %+v, want applied as owner-reviewed", out)
			}
			if len(svc.applyOpts) != 1 || !svc.applyOpts[0].OwnerReviewed {
				t.Fatalf("apply opts %+v, want OwnerReviewed", svc.applyOpts)
			}
			for _, want := range []string{applygate.ReasonTranscriptionMismatch, applygate.ReasonAuthorNotInPath} {
				if !strings.Contains(svc.applyOpts[0].GateOverride, want) {
					t.Errorf("GateOverride %q does not record %s", svc.applyOpts[0].GateOverride, want)
				}
			}
			if svc.applyOpts[0].FillOnly != tc.wantFillOnly {
				t.Errorf("FillOnly = %v, want %v", svc.applyOpts[0].FillOnly, tc.wantFillOnly)
			}
		})
	}
}

// asin_conflict stays hard for every owner-review origin.
func TestReviewBulk_ASINConflictStillBlocks(t *testing.T) {
	for name, pin := range map[string]func(metafetch.MetadataCandidate) *metafetch.CandidatePin{
		"bulk pin":     bulkPin,
		"owner marker": func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return ownerMarker() },
	} {
		t.Run(name, func(t *testing.T) {
			books, cand := refusedByAuthorAndTranscription()
			cand.ASIN = "B00NEWASIN"
			old := "B00OLDASIN"
			books["b1"].ASIN = &old
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin(cand), "")
			if out.Applied || out.OwnerReviewed || out.Reason != applySkipGateBlocked {
				t.Fatalf("outcome %+v, want gate_blocked", out)
			}
			if !hasReason(out.Gate.RefusingReasons(), applygate.ReasonASINConflict) {
				t.Fatalf("refusals %v lack asin_conflict", out.Gate.RefusingReasons())
			}
		})
	}
}

// partial_book stays hard for every owner-review origin.
func TestReviewBulk_PartialBookStillBlocks(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "The Long Road", Author: "Jane Roe", ASIN: "B0TESTROAD", Score: 0.99}
	books := fakeBooks{
		"b1": {ID: "b1", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/The Long Road.m4b"},
		"b2": {ID: "b2", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/CD 2"},
	}
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	claims, err := buildClaimIndex(context.Background(), []string{"b1", "b2"}, cachedClaimLoader(svc, books))
	if err != nil {
		t.Fatal(err)
	}
	for name, pin := range map[string]*metafetch.CandidatePin{"bulk pin": bulkPin(cand), "owner marker": ownerMarker()} {
		p := planCachedApply(svc, books, "b1", claims, pin)
		if p.OwnerReviewed || p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
			t.Fatalf("%s: plan reason=%q owner=%v gate=%+v, want partial_book refusal", name, p.Reason, p.OwnerReviewed, p.Gate)
		}
	}
}

// A hash-carrying bulk pin is staleness-checked exactly like a row pin; a
// hashless pin of any origin but review_bulk is not a marker and never matches.
func TestReviewBulk_StalenessChecks(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}

	shown := bulkPin(cand)
	shown.Title = "A different record"
	if p := planCachedApply(svc, books, "b1", nil, shown); p.Reason != applySkipStaleCandidate {
		t.Fatalf("stale bulk pin: reason %q, want %s", p.Reason, applySkipStaleCandidate)
	}
	hashlessRow := &metafetch.CandidatePin{Origin: metafetch.PinOriginRow}
	if p := planCachedApply(svc, books, "b1", nil, hashlessRow); p.Reason != applySkipStaleCandidate {
		t.Fatalf("hashless row pin: reason %q, want %s", p.Reason, applySkipStaleCandidate)
	}
	if p := planCachedApply(svc, books, "b1", nil, &metafetch.CandidatePin{Origin: "bulk"}); p.Reason != applySkipStaleCandidate {
		t.Fatalf("hashless unknown-origin pin: reason %q, want %s", p.Reason, applySkipStaleCandidate)
	}
}

// The marker never lifts the owner's own "no match": nobody was shown this
// book's candidate. A hash-checked bulk pin does, like a row pin.
func TestReviewBulk_NoMatchMark(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	nm := "no_match"
	books["b1"].MetadataReviewStatus = &nm
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	if p := planCachedApply(svc, books, "b1", nil, ownerMarker()); p.Reason != applySkipMarkedNoMatch {
		t.Fatalf("marker on a no-match book: reason %q, want %s", p.Reason, applySkipMarkedNoMatch)
	}
	if p := planCachedApply(svc, books, "b1", nil, bulkPin(cand)); p.Reason == applySkipMarkedNoMatch {
		t.Fatalf("hash-checked bulk pin must override the owner's no-match mark like a row pin")
	}
}

// The op-results apply takes no pin at all: its plan is never owner-reviewed,
// whatever the book. (planOpResultApply has no pin parameter; this pins that
// its dry run cannot claim a review either.)
func TestReviewBulk_OpResultPathIsNeverOwnerReviewed(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	plan := planOpResultApply(books, "b1", CandidateResult{Candidate: &cand, Book: CandidateBookInfo{ID: "b1", Title: "Moon Book"}}, nil)
	if plan.OwnerReviewed || plan.ReviewApproved || plan.Pinnable || plan.Reason != applySkipGateBlocked {
		t.Fatalf("op-results plan %+v, want hard gate_blocked and not pinnable", plan)
	}
}

// The same book with a filled description: a review_bulk apply (pin or
// marker) lifts the gate but leaves the filled description; a row apply
// replaces it. Driven through the real metafetch apply.
func TestReviewBulk_FillOnlyKeepsFilledDescription(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pin      func(metafetch.MetadataCandidate) *metafetch.CandidatePin
		wantDesc string
	}{
		{"bulk pin", bulkPin, "Owner's description"},
		{"owner marker", func(metafetch.MetadataCandidate) *metafetch.CandidatePin { return ownerMarker() }, "Owner's description"},
		{"row pin", rowPin, "Provider description"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			books, cand := refusedByAuthorAndTranscription()
			cand.Description = "Provider description"
			plan := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, cand)}, books, "b1", nil, tc.pin(cand))
			if !plan.OwnerReviewed || plan.Reason != "" {
				t.Fatalf("plan reason %q owner %v, want the gate lifted", plan.Reason, plan.OwnerReviewed)
			}
			desc := "Owner's description"
			book := *books["b1"]
			book.Description = &desc
			var updated *database.Book
			store := &database.MockStore{
				GetBookByIDFunc: func(string) (*database.Book, error) { c := book; return &c, nil },
				UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
					c := *b
					updated = &c
					return &c, nil
				},
			}
			_, err := metafetch.NewService(store).ApplyMetadataCandidateWithOptions("b1", cand, nil, plan.applyOptions())
			if err != nil && !errors.Is(err, metafetch.ErrApplyHistoryIncomplete) {
				t.Fatalf("apply: %v", err)
			}
			if updated == nil || updated.Description == nil {
				t.Fatalf("no row committed or description cleared: %+v", updated)
			}
			if *updated.Description != tc.wantDesc {
				t.Fatalf("description = %q, want %q", *updated.Description, tc.wantDesc)
			}
		})
	}
}
