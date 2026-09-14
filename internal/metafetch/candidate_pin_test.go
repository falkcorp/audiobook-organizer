// file: internal/metafetch/candidate_pin_test.go
// version: 1.0.0
// guid: 2a9c4e71-5b3d-4f08-8e16-d7c0b3a91f54
// last-edited: 2026-09-13

package metafetch

import (
	"encoding/json"
	"testing"
)

// The review list serves CandidateHash of the candidate it decoded from the
// cache row, and the apply recomputes it (via PinOf in Matches) from its own
// decode of the same row. If the two ever disagreed, every row pin would be
// refused as stale_candidate and a reviewed Apply would apply nothing. The
// raw row below carries the shapes most likely to break a re-marshal: a
// float written as 2.0, a field the struct does not know, key order unlike
// the struct's, and explicit empty values.
func TestCandidateHash_AgreesAcrossIndependentDecodes(t *testing.T) {
	raw := json.RawMessage(`{"unknown_provider_field":"x","score":2.0,"title":"Big Cats",` +
		`"source":"Google Books","author":"Ann Author","asin":"","duration_sec":36000,"duration_score":0.1,"description":"Book one."}`)

	var served, applied MetadataCandidate
	if err := json.Unmarshal(raw, &served); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &applied); err != nil {
		t.Fatal(err)
	}
	servedHash := CandidateHash(served)
	if servedHash == "" || servedHash != CandidateHash(applied) {
		t.Fatalf("hash differs across two decodes of one row: %q vs %q", servedHash, CandidateHash(applied))
	}

	// The UI receives the served candidate as JSON and echoes its identity
	// fields with the served hash; that pin must match the apply's decode.
	wire, err := json.Marshal(served)
	if err != nil {
		t.Fatal(err)
	}
	var echoed MetadataCandidate
	if err := json.Unmarshal(wire, &echoed); err != nil {
		t.Fatal(err)
	}
	pin := PinOf(echoed)
	pin.ContentHash = servedHash
	pin.Origin = PinOriginRow
	if !pin.Matches(applied) {
		t.Fatalf("a pin built from the served candidate and hash does not match the apply's decode: %+v", pin)
	}
	if CandidateHash(echoed) != servedHash {
		t.Fatal("hash is not stable across a serve round-trip")
	}
}
