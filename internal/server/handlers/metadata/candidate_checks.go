// file: internal/server/handlers/metadata/candidate_checks.go
// version: 1.1.0
// guid: 7d2c5e91-3a8b-4f60-b1e4-9c0f6a2d8e57
// last-edited: 2026-10-10

package metadatahandler

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The single-book dialog applies the candidate the person picked, and the
// certainty gate does not stand between them (applygate's package comment).
// Two of the gate's checks still matter here, because a book's cached
// candidates now survive an ASIN change: a kept candidate can name another
// ASIN than the book carries (asin_conflict), or carry none and have been
// fetched for an ASIN the book no longer has, or marked stale
// (metafetch.CandidateIdentityStale).
// The dialog shows both on each candidate, and the apply refuses an
// asin_conflict unless the person explicitly overrides it for the ASIN they
// were shown -- the dialog's form of the review lane's hash-checked pin.

// candidateApplyCheck is what the dialog shows on a candidate before it is
// applied. The zero value (no field set) is omitted from the response.
type candidateApplyCheck struct {
	// ASINConflict: the candidate names an ASIN and the book carries another
	// (applygate.CheckASIN). Applying it is refused without an override.
	ASINConflict bool `json:"asin_conflict,omitempty"`
	// IdentityStale: the candidate was fetched for an ASIN the book no longer
	// carries and does not carry the book's current one. Shown as a warning;
	// the apply does not refuse it, since the person is choosing.
	IdentityStale bool `json:"identity_stale,omitempty"`
	// OwnerRejected: the owner rejected this candidate for this book (POST
	// /metadata/batch-reject-candidates). Applying it is refused, here as on
	// every bulk path (applygate.ReasonOwnerRejected); un-reject it first.
	OwnerRejected bool `json:"owner_rejected,omitempty"`
	// BookASIN is the book's ASIN the checks ran against: the value an
	// override of the conflict must send back (applyRequest.OverrideASIN).
	BookASIN string `json:"book_asin,omitempty"`
	// Detail says what disagreed, in words.
	Detail string `json:"detail,omitempty"`
}

func (c candidateApplyCheck) empty() bool {
	return !c.ASINConflict && !c.IdentityStale && !c.OwnerRejected
}

// dialogCandidate is a search result as the dialog receives it: the candidate's
// own fields, flattened, plus ApplyCheck when something disagrees. A client
// that posts the candidate back to apply-metadata sends apply_check too; the
// apply decodes a plain MetadataCandidate and ignores it.
type dialogCandidate struct {
	metafetch.MetadataCandidate
	ApplyCheck *candidateApplyCheck `json:"apply_check,omitempty"`
}

// checkCandidate runs the dialog's checks on one candidate. entry is the cache
// row the candidate came from, nil for a fresh search (whose candidates were
// found for the book as it is, so only the ASIN comparison applies).
//
// rejected is the book's owner rejections (nil: none known).
func checkCandidate(book *database.Book, entry *metafetch.MetadataCandidateCache, rejected metafetch.RejectedSet, c *metafetch.MetadataCandidate) candidateApplyCheck {
	var out candidateApplyCheck
	if book == nil || c == nil {
		return out
	}
	if rejected.HasCandidate(c) {
		out.OwnerRejected = true
	}
	if book.ASIN != nil {
		out.BookASIN = strings.TrimSpace(*book.ASIN)
	}
	var details []string
	if r := applygate.CheckASIN(book, c); r.Outcome == applygate.OutcomeBlock {
		out.ASINConflict = true
		details = append(details, r.Detail)
	}
	if err := metafetch.CandidateIdentityStale(entry, book, c); err != nil {
		out.IdentityStale = true
		details = append(details, err.Error())
	}
	if out.OwnerRejected {
		details = append(details, "you rejected this candidate for this book; un-reject it to apply it")
	}
	out.Detail = strings.Join(details, "; ")
	return out
}

// withApplyChecks returns results as dialog candidates, each carrying its
// checks against book. A nil book (it could not be read) returns them
// unchecked rather than failing the search.
func withApplyChecks(book *database.Book, entry *metafetch.MetadataCandidateCache, rejected metafetch.RejectedSet, results []metafetch.MetadataCandidate) []dialogCandidate {
	out := make([]dialogCandidate, len(results))
	for i := range results {
		out[i].MetadataCandidate = results[i]
		if chk := checkCandidate(book, entry, rejected, &results[i]); !chk.empty() {
			out[i].ApplyCheck = &chk
		}
	}
	return out
}

// applyReasonASINConflict is the 409 reason the dialog reads for a refused
// conflicting apply; the gate's own reason string.
const applyReasonASINConflict = applygate.ReasonASINConflict

// errASINConflict refuses a single-book apply whose candidate names another
// ASIN than the book carries, without an override for that book ASIN.
type errASINConflict struct {
	BookASIN      string
	CandidateASIN string
	Detail        string
}

func (e *errASINConflict) Error() string {
	return fmt.Sprintf("the candidate's ASIN conflicts with the book's (%s); confirm the override to apply it anyway", e.Detail)
}

// asinConflictRefusal returns the refusal for applying cand to book, or nil.
// override is the book ASIN the person was shown the conflict against
// (candidateApplyCheck.BookASIN): it lifts the refusal only while the book
// still carries exactly that ASIN, so an override given for one conflict never
// covers a different one that appeared since -- on the queued path the apply
// runs after the scan moves on, and the book may have changed meanwhile.
func asinConflictRefusal(book *database.Book, cand *metafetch.MetadataCandidate, override string) *errASINConflict {
	if book == nil || cand == nil {
		return nil
	}
	r := applygate.CheckASIN(book, cand)
	if r.Outcome != applygate.OutcomeBlock {
		return nil
	}
	cur := strings.TrimSpace(*book.ASIN) // non-nil: CheckASIN blocked on it
	if o := strings.TrimSpace(override); o != "" && strings.EqualFold(o, cur) {
		return nil
	}
	return &errASINConflict{BookASIN: cur, CandidateASIN: strings.TrimSpace(cand.ASIN), Detail: r.Detail}
}

// errOwnerRejected refuses a single-book apply of a candidate the owner
// rejected for the book. Unlike an ASIN conflict there is no override on the
// apply: the owner un-rejects the candidate (POST
// /metadata/batch-unreject-candidates) and then applies it.
type errOwnerRejected struct {
	Source, Title string
}

func (e *errOwnerRejected) Error() string {
	return fmt.Sprintf("you rejected the %s candidate %q for this book; un-reject it to apply it", e.Source, e.Title)
}

// ownerRejectedRefusal returns the refusal for applying cand to bookID, nil
// when the owner did not reject it, or the read error: an unreadable
// rejection list refuses the apply rather than reading as "not rejected".
func ownerRejectedRefusal(r metafetch.RejectedCandidateReader, bookID string, cand *metafetch.MetadataCandidate) error {
	rejected, err := metafetch.LoadRejectedCandidates(r, bookID)
	if err != nil {
		return fmt.Errorf("%s: %w", applygate.ReasonOwnerRejectionCheckFailed, err)
	}
	if rejected.HasCandidate(cand) {
		return &errOwnerRejected{Source: cand.Source, Title: cand.Title}
	}
	return nil
}
