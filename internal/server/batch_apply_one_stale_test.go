// file: internal/server/batch_apply_one_stale_test.go
// version: 1.0.0
// guid: 7b0e5c3a-4d92-4f61-9a38-2c1d6e8f0b47
// last-edited: 2026-10-09

package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A cache row marked Stale (the book was retitled after the fetch) is refused
// as identity_stale at the bulk planner, even for a candidate that would pass
// every other leg, and an owner-review pin does not lift it.
func TestPlanCachedApply_StaleRowRefusesCandidate(t *testing.T) {
	book := func() fakeBooks {
		dur := 36000
		return fakeBooks{"b1": {ID: "b1", Title: "Title 000123", FilePath: "/lib/Author 07/Title 000123/Title 000123.m4b",
			Duration: &dur}}
	}
	plain := metafetch.MetadataCandidate{Title: "Title 000123", Author: "Author 07", Score: 0.95, DurationSec: 36000}

	control := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, plain)}, book(), "b1", nil, nil)
	if control.Reason != "" {
		t.Fatalf("fixture: the unflagged row must apply, got %q (%v)", control.Reason, control.Err)
	}

	p := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, plain), staleRow: true}, book(), "b1", nil, nil)
	if p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Reason != applygate.ReasonIdentityStale {
		t.Fatalf("plan = %q gate %+v, want gate_blocked / identity_stale", p.Reason, p.Gate)
	}
	if !strings.Contains(p.Gate.Detail, "fp-synthetic") {
		t.Errorf("detail %q should name the question the row was stale against", p.Gate.Detail)
	}
	if pb := planCachedApply(&fakeApplySvc{candidates: candidateJSON(t, plain), staleRow: true}, book(), "b1", nil, bulkPin(plain)); pb.OwnerReviewed || pb.Reason != applySkipGateBlocked {
		t.Fatalf("bulk pin over a stale row: %+v", pb)
	}
}

// A transcription explains a stale QUERY; it must not lift a stale-row
// refusal: the book has a transcribed title that matches the candidate and
// the row's query drifted (the control passes the identity leg), yet the
// flagged row is still refused as identity_stale.
func TestPlanCachedApply_TranscriptionDoesNotLiftStaleRow(t *testing.T) {
	const transcribed = "Title 000456"
	mk := func() fakeBooks {
		dur := 36000
		return fakeBooks{"b1": {ID: "b1", Title: "", TranscribedTitle: strPtr(transcribed),
			FilePath: "/lib/Unknown Author/Unknown Title/book.m4b", Duration: &dur}}
	}
	cand := metafetch.MetadataCandidate{Title: transcribed, Author: "Author 07", Score: 0.95, DurationSec: 36000, Source: "Google Books"}
	blob, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	drift := fmt.Errorf("%w: book b1 (stored a, current b)", metafetch.ErrStaleMetadataCache)

	base := planCachedApply(&fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: drift,
		queryMatches: map[string]bool{transcribed: true}}, mk(), "b1", nil, nil)
	if base.Gate == nil || base.Gate.Reason == applygate.ReasonIdentityStale {
		t.Fatalf("fixture: the transcription must lift the drifted query, gate %+v", base.Gate)
	}

	p := planCachedApply(&fakeApplySvc{candidates: []json.RawMessage{blob}, identityErr: drift,
		queryMatches: map[string]bool{transcribed: true}, staleRow: true}, mk(), "b1", nil, nil)
	if p.Gate == nil || p.Gate.Reason != applygate.ReasonIdentityStale {
		t.Fatalf("a stale row was lifted by the transcription: gate %+v", p.Gate)
	}
}
