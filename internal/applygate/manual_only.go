// file: internal/applygate/manual_only.go
// version: 1.1.0
// guid: a2f62ab5-314e-427a-8ca7-de28de936b75
// last-edited: 2026-09-28

package applygate

import (
	"regexp"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// manualOnlyRe matches the libraries the owner curates by hand: Doctor Who,
// Big Finish and Torchwood. Owner rule (standing): these are never touched by
// a bulk apply or bulk merge -- the owner applies them manually, by explicit
// book id. Separators between words vary across rips ("Doctor.Who",
// "Doctor_Who", "DoctorWho"), so any run of separators, or none, is accepted.
var manualOnlyRe = regexp.MustCompile(`(?i)\b(doctor[\s._-]*who|big[\s._-]*finish|torchwood)\b`)

// IsOwnerManualOnly reports whether a book with this path or series name
// belongs to a manual-only library and must be left out of every bulk apply or
// bulk merge.
func IsOwnerManualOnly(path, seriesName string) bool {
	return manualOnlyRe.MatchString(path) || manualOnlyRe.MatchString(seriesName)
}

// ReasonOwnerManualOnly refuses a bulk apply of a book the owner applies by
// hand (IsOwnerManualOnly). It is a hard reason: no owner-review pin from a
// bulk button lifts it (OwnerReviewOverridable).
const ReasonOwnerManualOnly = "owner_manual_only"

// ManualOnlyGuard is the bulk-apply input to the owner-manual-only check in
// EvaluateTranscribed. The zero value is a single-book caller (the apply
// dialog, one review row the owner approved, metadata.upgrade which checks
// its own way): no check.
type ManualOnlyGuard struct {
	// Bulk is set by a caller applying without a human choosing this one book
	// (a bulk apply, including the review page's bulk buttons).
	Bulk bool
	// StoreDetail is the caller's store-backed finding ("" = none): what its
	// read of the book's series name and every book_file path matched, or
	// that the read failed (a failed read counts as manual-only, since reading
	// it as "not manual-only" would loosen the rule).
	StoreDetail string
}

// ManualOnlyDetail reports why a bulk apply of candidate c onto book must be
// refused as owner-manual-only, or "" when nothing marks it. It checks
// everything the gate holds without a store read: the book's path and title,
// the query it was found by (ts.Query: a blank-titled Big Finish book whose
// intro says "Doctor Who: The Chimes of Midnight"), and the candidate's own
// title and series (Audible answering with a Doctor Who record). The caller's
// store-backed finding (series row, book_file paths) comes in g.StoreDetail.
func ManualOnlyDetail(book *database.Book, c *metafetch.MetadataCandidate, ts TranscribedSearch, g ManualOnlyGuard) string {
	if !g.Bulk {
		return ""
	}
	if g.StoreDetail != "" {
		return g.StoreDetail
	}
	checks := []struct{ what, value string }{
		{"path", book.FilePath},
		{"title", book.Title},
		{"search query", ts.Query},
	}
	if c != nil {
		checks = append(checks,
			struct{ what, value string }{"candidate title", c.Title},
			struct{ what, value string }{"candidate series", c.Series},
		)
	}
	for _, ch := range checks {
		if manualOnlyRe.MatchString(ch.value) {
			return "Doctor Who / Big Finish / Torchwood are applied by hand, one book at a time; " +
				ch.what + " " + strconv.Quote(ch.value)
		}
	}
	return ""
}
