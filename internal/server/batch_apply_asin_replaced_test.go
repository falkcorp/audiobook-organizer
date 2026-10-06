// file: internal/server/batch_apply_asin_replaced_test.go
// version: 1.0.0
// guid: 1f6d3b82-9a47-4e05-b2c8-6e0a7d4f1c95
// last-edited: 2026-10-05

package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A cached candidate with no ASIN of its own, kept after the book's ASIN was
// replaced, is refused as identity_stale by the bulk planner: the ASIN check
// passes it (nothing to compare), and it was found for a book identified by
// another record. A candidate carrying the new ASIN -- the one just applied --
// is not.
func TestPlanCachedApply_ReplacedASINRefusesASINlessCandidate(t *testing.T) {
	newASIN := "B00NEWASIN"
	book := func() fakeBooks {
		dur := 36000
		return fakeBooks{"b1": {ID: "b1", Title: "A Title", FilePath: "/lib/An Author/A Title/A Title.m4b",
			Duration: &dur, ASIN: &newASIN}}
	}
	plain := metafetch.MetadataCandidate{Title: "A Title", Author: "An Author", Score: 0.95, DurationSec: 36000}

	control := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, plain)}, book(), "b1", nil, nil)
	if control.Reason != "" {
		t.Fatalf("fixture: the unstamped row must apply, got %q (%v)", control.Reason, control.Err)
	}

	p := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, plain), fetchedForASIN: "B00OLDASIN"}, book(), "b1", nil, nil)
	if p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Reason != applygate.ReasonIdentityStale {
		t.Fatalf("plan = %q gate %+v, want gate_blocked / identity_stale", p.Reason, p.Gate)
	}
	if !strings.Contains(p.Gate.Detail, "B00OLDASIN") {
		t.Errorf("detail %q should name the ASIN the row was fetched for", p.Gate.Detail)
	}
	// identity_stale is not lifted by an owner review: a bulk pin still refuses.
	if pb := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, plain), fetchedForASIN: "B00OLDASIN"}, book(), "b1", nil, bulkPin(plain)); pb.OwnerReviewed || pb.Reason != applySkipGateBlocked {
		t.Fatalf("bulk pin over identity_stale: %+v", pb)
	}

	applied := plain
	applied.ASIN = "b00newasin"
	ok := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, applied), fetchedForASIN: "B00OLDASIN"}, book(), "b1", nil, nil)
	if ok.Gate != nil && ok.Gate.Reason == applygate.ReasonIdentityStale {
		t.Fatalf("a candidate carrying the book's new ASIN must not read as stale: %+v", ok.Gate)
	}
}

// A transcription explains a stale QUERY; it must not lift a row fetched for
// an ASIN the book no longer carries. Planet Hulk: a blank-titled book whose
// row was fetched for its transcribed title passes the identity leg by the
// lift (control), and is refused once its ASIN was replaced since.
func TestPlanCachedApply_TranscriptionDoesNotLiftReplacedASIN(t *testing.T) {
	const hulk = "Marvel's Planet Hulk"
	asin := "B00HULKNEW"
	mk := func() fakeBooks {
		dur := 36000
		return fakeBooks{"b1": {ID: "b1", Title: "", TranscribedTitle: strPtr(hulk), ASIN: &asin,
			FilePath: "/lib/Unknown Author/Unknown Title/book.m4b", Duration: &dur}}
	}
	cand := metafetch.MetadataCandidate{Title: hulk, Author: "Greg Pak", Score: 0.95, DurationSec: 36000, Source: "Google Books"}
	blob, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	stale := fmt.Errorf("%w: book b1 (stored a, current b)", metafetch.ErrStaleMetadataCache)

	base := planCachedApply(&fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: stale,
		queryMatches: map[string]bool{hulk: true}}, mk(), "b1", nil, nil)
	if base.Gate == nil || base.Gate.Reason == applygate.ReasonIdentityStale {
		t.Fatalf("fixture: the transcription must lift the stale query, gate %+v", base.Gate)
	}

	p := planCachedApply(&fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: stale,
		queryMatches: map[string]bool{hulk: true}, fetchedForASIN: "B00HULKOLD"}, mk(), "b1", nil, nil)
	if p.Gate == nil || p.Gate.Reason != applygate.ReasonIdentityStale {
		t.Fatalf("a replaced ASIN was lifted by the transcription: gate %+v", p.Gate)
	}
	if !strings.Contains(p.Gate.Detail, "ASIN changed") {
		t.Errorf("detail %q should say the ASIN changed", p.Gate.Detail)
	}
}

// The transcription auto-apply refuses the same row (transcriptionCacheIdentity).
func TestTranscriptionCacheIdentity_RefusesReplacedASIN(t *testing.T) {
	asin := "B00NEWASIN"
	dur := 36000
	books := fakeBooks{"b1": {ID: "b1", Title: "A Title", FilePath: "/lib/An Author/A Title/A Title.m4b", Duration: &dur, ASIN: &asin}}
	cand := metafetch.MetadataCandidate{Title: "A Title", Author: "An Author", Score: 0.95}
	entry := &metafetch.MetadataCandidateCache{BookID: "b1", FetchedForASIN: "B00OLDASIN"}
	b, _ := books.GetBookByID("b1")
	if err := transcriptionCacheIdentity(&fakeApplySvc{}, books, entry, b, cand); err == nil {
		t.Fatal("a row fetched for a replaced ASIN must be refused")
	}
	entry.FetchedForASIN = ""
	if err := transcriptionCacheIdentity(&fakeApplySvc{}, books, entry, b, cand); err != nil {
		t.Fatalf("control: an unstamped row passes, got %v", err)
	}
}
