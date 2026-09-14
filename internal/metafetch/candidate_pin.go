// file: internal/metafetch/candidate_pin.go
// version: 1.0.0
// guid: 9f4a1d63-2c7e-4b85-a0d9-5e3b8c1f6a42
// last-edited: 2026-09-13

package metafetch

import "strings"

// CandidatePin identifies the cached candidate a reviewer was LOOKING AT when
// they clicked Apply. The review lane shows the top cached candidate
// (Candidates[0], GetCacheReviewResults) and the batch apply applies the top
// cached candidate, so they are the same row unless the cache was refetched
// in between. The pin is how the server tells: it carries the fields that
// identify a provider record, and every one must still match.
//
// A refetch replaces the cache row wholesale, so strict equality is the right
// test: a different record, or the same record with different identity
// fields, is not what the owner reviewed.
type CandidatePin struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	ASIN   string `json:"asin,omitempty"`
	ISBN   string `json:"isbn,omitempty"`
	ISBN10 string `json:"isbn10,omitempty"`
	ISBN13 string `json:"isbn13,omitempty"`
}

// PinOf is the pin that identifies c.
func PinOf(c MetadataCandidate) CandidatePin {
	return CandidatePin{Source: c.Source, Title: c.Title, Author: c.Author,
		ASIN: c.ASIN, ISBN: c.ISBN, ISBN10: c.ISBN10, ISBN13: c.ISBN13}
}

// Matches reports whether c is the candidate p was taken from. Whitespace at
// the ends is ignored; everything else must be equal.
func (p CandidatePin) Matches(c MetadataCandidate) bool {
	q := PinOf(c)
	eq := func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }
	return eq(p.Source, q.Source) && eq(p.Title, q.Title) && eq(p.Author, q.Author) &&
		eq(p.ASIN, q.ASIN) && eq(p.ISBN, q.ISBN) && eq(p.ISBN10, q.ISBN10) && eq(p.ISBN13, q.ISBN13)
}

// ApplyOptions adjusts how ApplyMetadataCandidateWithOptions records an apply.
// The zero value is ApplyMetadataCandidate.
type ApplyOptions struct {
	// GateOverride, when non-empty, says the apply went through on an owner
	// review although the bulk-apply certainty gate refused it, and names the
	// refusing reasons. It is recorded on every field's change-history row (in
	// its source), on the activity entries, and as an "owner_reviewed" version
	// note. It changes nothing about which fields are written.
	GateOverride string
}

// historySource is the source label change history records for an apply.
func (o ApplyOptions) historySource(candidateSource string) string {
	if o.GateOverride == "" {
		return candidateSource
	}
	src := candidateSource
	if src == "" {
		src = "unknown source"
	}
	return src + " (owner-reviewed; certainty gate overridden: " + o.GateOverride + ")"
}
