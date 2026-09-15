// file: internal/metafetch/candidate_pin.go
// version: 1.8.0
// guid: 9f4a1d63-2c7e-4b85-a0d9-5e3b8c1f6a42
// last-edited: 2026-09-14

package metafetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// PinOriginRow is the only pin origin that earns an owner-review override:
// the owner clicked Apply on ONE review row, looking at that row's candidate.
// Bulk buttons (Apply page, Apply high confidence, group Apply All, Apply
// selected) send no pins at all, and a pin with any other origin is checked
// for staleness but gets the ordinary hard gate.
const PinOriginRow = "row"

// CandidatePin identifies the cached candidate a reviewer was LOOKING AT when
// they clicked Apply. The review lane shows the top cached candidate
// (Candidates[0], GetCacheReviewResults) and the batch apply applies the top
// cached candidate, so they are the same row unless the cache was refetched
// in between. The pin is how the server tells.
//
// ContentHash (CandidateHash of the candidate the review list served) is what
// makes the pin strong: two provider records with no ASIN/ISBN, the same
// source and the same title would otherwise satisfy each other's pin. The
// identity fields are kept alongside so a refusal can say what changed.
//
// A refetch replaces the cache row wholesale, so strict equality is the right
// test: a different record, or the same record with any field changed, is not
// what the owner reviewed.
type CandidatePin struct {
	Origin      string `json:"origin,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Source      string `json:"source"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	ASIN        string `json:"asin,omitempty"`
	ISBN        string `json:"isbn,omitempty"`
	ISBN10      string `json:"isbn10,omitempty"`
	ISBN13      string `json:"isbn13,omitempty"`
}

// CandidateHash is the hex SHA-256 of c's canonical JSON: encoding/json over
// the decoded struct, which emits fields in declaration order and map keys
// sorted, so the review list (which serves it) and the apply (which checks
// it) compute the same value from the same cache row. "" only if c cannot be
// encoded, which a decoded candidate always can.
func CandidateHash(c MetadataCandidate) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PinOf is the pin that identifies c, content hash included. Origin is left
// empty: only the review lane's single-row Apply sets PinOriginRow.
func PinOf(c MetadataCandidate) CandidatePin {
	return CandidatePin{ContentHash: CandidateHash(c), Source: c.Source, Title: c.Title, Author: c.Author,
		ASIN: c.ASIN, ISBN: c.ISBN, ISBN10: c.ISBN10, ISBN13: c.ISBN13}
}

// IsRowReview reports whether p records a single-row owner review.
func (p CandidatePin) IsRowReview() bool { return p.Origin == PinOriginRow }

// Matches reports whether c is the candidate p was taken from: the content
// hash must be present and equal, and so must every identity field
// (whitespace at the ends ignored). A pin without a hash never matches.
func (p CandidatePin) Matches(c MetadataCandidate) bool {
	q := PinOf(c)
	if p.ContentHash == "" || p.ContentHash != q.ContentHash {
		return false
	}
	eq := func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }
	return eq(p.Source, q.Source) && eq(p.Title, q.Title) && eq(p.Author, q.Author) &&
		eq(p.ASIN, q.ASIN) && eq(p.ISBN, q.ISBN) && eq(p.ISBN10, q.ISBN10) && eq(p.ISBN13, q.ISBN13)
}

// ApplyOptions adjusts how ApplyMetadataCandidateWithOptions records an apply.
// The zero value is ApplyMetadataCandidate.
type ApplyOptions struct {
	// OwnerReviewed says the apply went through on an owner review although
	// the bulk-apply certainty gate refused it. It alone decides that the
	// override is recorded on every field's change-history row (in its
	// source), on the activity entries, and as an "owner_reviewed" version
	// note, and that a failed history write is returned to the caller. It
	// changes nothing about which fields are written.
	OwnerReviewed bool
	// GateOverride names the refusing reasons for those records. It is only a
	// label: an empty value on a reviewed apply records "owner_reviewed"
	// rather than skipping the history requirement or the note.
	GateOverride string
	// FillOnly makes the apply fill the book's descriptive fields without
	// overwriting any that already hold a value (StripFilledFields). Every
	// batch and automatic apply sets it (owner decision A3#3) except a
	// hand-picked one: the single-book apply, and a batch row the owner
	// approved in the review lane, whether the gate passed or refused it
	// (owner ruling 2026-09-14), leave it false and may overwrite. The rename
	// preflight and the bulk-apply preview must get the same value the apply
	// does. It is independent of OwnerReviewed, which only labels a lifted
	// gate refusal.
	//
	// It is also what marks an apply as AUTOMATIC (nobody picked this
	// candidate): a FillOnly apply records the match (review status,
	// MetadataSource, MetadataSourceHash) only when the book ends up holding
	// the candidate's title, while a hand-picked apply (FillOnly false)
	// records it whatever title the book keeps, because the person asserted
	// the match. A new automatic caller must set FillOnly.
	//
	// It covers the DESCRIPTIVE fields only (StripFilledFields leaves the
	// identity fields alone), so it does not stop a title overwrite: a caller
	// that must not replace a filled title drops "title" from its fields
	// allowlist, as maintenance.auto-match-transcribed does.
	FillOnly bool
	// RefuseEmptyWrite makes the apply return ErrNothingToApply, writing
	// nothing (no review status, version note or source stamp), when the
	// fields allowlist, the fill-only strip and the field locks leave no
	// column for the candidate to change. An automatic apply that narrows its
	// allowlist sets it, so a match with nothing left to write is a skip
	// rather than a book stamped as matched. A hand-picked apply leaves it
	// false.
	//
	// The check runs after the apply body and counts both a changed book
	// column and a changed author credit list (AuthorCredits.Changed) as a
	// change: on a book that already has an author, an applied co-author adds
	// a book_authors credit, written under the store's lock, without changing
	// a column. That apply proceeds and commits (with history, so undo can
	// remove the credit) rather than reporting nothing to apply.
	RefuseEmptyWrite bool
}

// ErrNothingToApply is returned by an apply with RefuseEmptyWrite when no
// field would change. Nothing was written.
var ErrNothingToApply = errors.New("metadata apply: no field left to write")

// ErrMarkedNoMatch is returned by an automatic apply or fetch (nobody picked a
// candidate) for a book its owner marked "no match": the owner rejected every
// match, so nothing is written or searched. Bulk callers report the book as
// skipped, not failed. A person-picked apply is the owner overriding their own
// mark and is not refused.
var ErrMarkedNoMatch = errors.New("metadata: book is marked no match")

// IsMarkedNoMatch reports whether the owner marked this book "no match"
// (MetadataReviewStatus == "no_match", written by Service.MarkNoMatch).
func IsMarkedNoMatch(status *string) bool {
	return status != nil && *status == "no_match"
}

// overrideLabel is the refusing-reasons label recorded for an owner-reviewed
// apply, never empty.
func (o ApplyOptions) overrideLabel() string {
	if o.GateOverride == "" {
		return ownerReviewedLabel
	}
	return o.GateOverride
}

// ownerReviewedLabel matches applygate.ReasonOwnerReviewed; metafetch does
// not import applygate.
const ownerReviewedLabel = "owner_reviewed"

// historySource is the source label change history records for an apply.
func (o ApplyOptions) historySource(candidateSource string) string {
	if !o.OwnerReviewed {
		return candidateSource
	}
	src := candidateSource
	if src == "" {
		src = "unknown source"
	}
	return src + " (owner-reviewed; certainty gate overridden: " + o.overrideLabel() + ")"
}
